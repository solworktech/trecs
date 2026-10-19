package lib

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Images in a recording.
//
// An image is shown over the terminal at a time, for a while, and playback goes on after it: it is an annotation line of the
// recording (type "annotation") with kind "image", as a cue card is one with kind "cue" - a reader that does not know of it reads
// it as the note it also looks like (its text is "display image: name.png"), where a line of a type it did not know would be read as
// an empty frame. The line has
//
//	timestamp        when it is shown, in milliseconds
//	durationSeconds  for how long
//	image            the name the file is kept under: img-<hash of its contents>.<ext>, in the images directory beside the recording
//	imageName        the name the file had, without its directory (the directory would mean nothing to someone watching on the web)
//
// Unlike every other annotation an image takes time of its own: inserting one at a time T for D seconds moves everything that is at
// or after T - frames, prompts, annotations, other images - D seconds later, so that the recording is still, in the gap, what it was
// at T. Deleting it moves them back (InsertGap and RemoveGap). Because playback is the position moving through time, an image is on
// show exactly while the position is in [T, T+D), whether playback plays through it, is moved into it, or is paused in it, and no
// timer is needed to take it away.
const (
	AnnotationKindImage = "image"

	// ImagesDir is the directory beside a recording that its images are kept in.
	ImagesDir = "images"

	// MaxImageBytes is the largest image file: the server will not take a larger one either.
	MaxImageBytes = 2 << 20

	// MaxImagePixels and MaxImageDim keep a small file that is a huge picture out: a browser, or this, would decode it.
	MaxImagePixels = 50_000_000
	MaxImageDim    = 16384

	ImageDefaultSeconds = 5
	ImageMaxSeconds     = 600
)

// ImageCaption is how an image is named wherever it is listed.
func ImageCaption(name string) string { return "display image: " + name }

// ImageTypes are the kinds of image a recording takes, by the extension they are stored with.
var ImageTypes = map[string]string{"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}

var storedImageName = regexp.MustCompile(`^img-[0-9a-f]{12}\.(png|jpg|gif|webp)$`)

// ValidStoredImageName reports whether name is one that an image is kept under - and so is safe to join to a directory.
func ValidStoredImageName(name string) bool { return storedImageName.MatchString(name) }

// ImageMIME is the content type of a stored image, by its name; "" if it is not a valid name.
func ImageMIME(stored string) string {
	if !ValidStoredImageName(stored) {
		return ""
	}
	return ImageTypes[strings.TrimPrefix(filepath.Ext(stored), ".")]
}

// ImageInfo is what is known of an image file.
type ImageInfo struct {
	Stored string // img-<hash>.<ext>
	Name   string // the base name it had
	MIME   string
	W, H   int
	Size   int
}

// InspectImage checks image data (what the file is is decided by its contents, never by its name): a PNG, JPEG, GIF or WebP, not over
// MaxImageBytes, with a picture size that is not absurd. name is its original file name, of which only the base name is kept.
func InspectImage(name string, data []byte) (ImageInfo, error) {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	info := ImageInfo{Name: base, Size: len(data)}
	switch {
	case len(data) == 0:
		return info, errors.New("the file is empty")
	case len(data) > MaxImageBytes:
		return info, fmt.Errorf("the file is %s, and the most is %s", byteSize(len(data)), byteSize(MaxImageBytes))
	}
	var ext string
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		ext = "png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		ext = "jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		ext = "gif"
	case len(data) >= 16 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		ext = "webp"
	default:
		return info, errors.New("not a PNG, JPEG, GIF or WebP image")
	}
	var err error
	if ext == "webp" {
		info.W, info.H, err = webpSize(data)
	} else {
		var cfg image.Config
		if cfg, _, err = image.DecodeConfig(bytes.NewReader(data)); err == nil {
			info.W, info.H = cfg.Width, cfg.Height
		}
	}
	if err != nil {
		return info, fmt.Errorf("the image cannot be read: %w", err)
	}
	if info.W <= 0 || info.H <= 0 || info.W > MaxImageDim || info.H > MaxImageDim || info.W*info.H > MaxImagePixels {
		return info, fmt.Errorf("the picture is %dx%d, which is too large (or empty)", info.W, info.H)
	}
	sum := sha256.Sum256(data)
	info.Stored = "img-" + hex.EncodeToString(sum[:6]) + "." + ext
	info.MIME = ImageTypes[ext]
	return info, nil
}

func byteSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// ByteSize is a size as it is told to a person: 1.4 MB, 310 KB.
func ByteSize(n int) string { return byteSize(n) }

// webpSize reads the size of a WebP picture from its header (the standard library has no decoder for it).
func webpSize(b []byte) (w, h int, err error) {
	if len(b) < 30 {
		return 0, 0, errors.New("truncated WebP")
	}
	switch string(b[12:16]) {
	case "VP8X":
		return 1 + (int(b[24]) | int(b[25])<<8 | int(b[26])<<16), 1 + (int(b[27]) | int(b[28])<<8 | int(b[29])<<16), nil
	case "VP8 ":
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, errors.New("not a WebP picture")
		}
		return (int(b[26]) | int(b[27])<<8) & 0x3fff, (int(b[28]) | int(b[29])<<8) & 0x3fff, nil
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, errors.New("not a WebP picture")
		}
		v := uint32(b[21]) | uint32(b[22])<<8 | uint32(b[23])<<16 | uint32(b[24])<<24
		return int(v&0x3fff) + 1, int(v>>14&0x3fff) + 1, nil
	}
	return 0, 0, errors.New("not a WebP picture")
}

// ReadImageFile reads and inspects an image file from disk.
func ReadImageFile(path string) ([]byte, ImageInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, ImageInfo{}, err
	}
	if fi.IsDir() {
		return nil, ImageInfo{}, errors.New("that is a directory")
	}
	if fi.Size() > MaxImageBytes { // before it is read
		return nil, ImageInfo{Name: filepath.Base(path)}, fmt.Errorf("the file is %s, and the most is %s", byteSize(int(fi.Size())), byteSize(MaxImageBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ImageInfo{}, err
	}
	info, err := InspectImage(path, data)
	return data, info, err
}

// ImagesPath is the directory beside a recording that holds its images.
func ImagesPath(recordingPath string) string {
	return filepath.Join(filepath.Dir(recordingPath), ImagesDir)
}

// ImageFilePath is where a stored image of a recording is; an error for a name that could lead out of the directory.
func ImageFilePath(recordingPath, stored string) (string, error) {
	if !ValidStoredImageName(stored) {
		return "", fmt.Errorf("%q is not the name of an image", stored)
	}
	return filepath.Join(ImagesPath(recordingPath), stored), nil
}

// SaveImage keeps an image beside the recording (once: it is named by what is in it, so the same picture inserted twice is one file).
func SaveImage(recordingPath string, data []byte, info ImageInfo) error {
	dest, err := ImageFilePath(recordingPath, info.Stored)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dest); err == nil && fi.Size() == int64(len(data)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".img-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// ReferencedImages are the stored names of the images the annotations show, each once, in the order they are first used. A name that
// is not valid is left out.
func ReferencedImages(annotations []Annotation) []string {
	var names []string
	seen := map[string]bool{}
	for _, a := range annotations {
		if a.Kind == AnnotationKindImage && ValidStoredImageName(a.Image) && !seen[a.Image] {
			seen[a.Image] = true
			names = append(names, a.Image)
		}
	}
	return names
}

// ---- time ------------------------------------------------------------------------------------------------------------------------

// ImageDurationMs is how long an image is shown.
func ImageDurationMs(a Annotation) int64 {
	s := a.DurationSeconds
	if s < 1 {
		s = ImageDefaultSeconds
	}
	return int64(s) * 1000
}

// ImageEnd is when an image goes: it is shown from its Timestamp to this, not including it.
func ImageEnd(a Annotation) int64 { return a.Timestamp + ImageDurationMs(a) }

// ImageAt is the image that is on show at a position of playback, if there is one.
func ImageAt(images []Annotation, pos int64) (Annotation, bool) {
	for _, a := range images {
		if pos >= a.Timestamp && pos < ImageEnd(a) {
			return a, true
		}
	}
	return Annotation{}, false
}

// RecordingImages are the images of the commands, in the order of their times.
func RecordingImages(commands []Command) []Annotation {
	var all []Annotation
	for _, c := range commands {
		all = append(all, c.Images...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp < all[j].Timestamp })
	return all
}

// moved is a time after a gap of gapMs has been made at at (positive), or closed there (negative).
func moved(ts, at, gapMs int64) int64 {
	switch {
	case gapMs >= 0:
		if ts >= at {
			return ts + gapMs
		}
	default: // closing: what was inside the gap goes to its start
		if ts >= at-gapMs {
			return ts + gapMs
		}
		if ts > at {
			return at
		}
	}
	return ts
}

func shiftFrames(frames []TerminalFrame, at, gapMs int64) []TerminalFrame {
	if frames == nil {
		return nil
	}
	out := make([]TerminalFrame, len(frames))
	for i, f := range frames {
		f.Timestamp = moved(f.Timestamp, at, gapMs)
		out[i] = f
	}
	return out
}

func shiftAnnotations(in []Annotation, at, gapMs int64) []Annotation {
	if in == nil {
		return nil
	}
	out := make([]Annotation, len(in))
	for i, a := range in {
		a.Timestamp = moved(a.Timestamp, at, gapMs)
		out[i] = a
	}
	return out
}

func shiftCommands(commands []Command, at, gapMs int64) []Command {
	out := make([]Command, len(commands))
	for i, c := range commands {
		if c.HasPrompt {
			c.PromptFrame.Timestamp = moved(c.PromptFrame.Timestamp, at, gapMs)
		}
		c.InputFrames = shiftFrames(c.InputFrames, at, gapMs)
		c.OutputFrames = shiftFrames(c.OutputFrames, at, gapMs)
		c.StartTime = moved(c.StartTime, at, gapMs)
		c.EndTime = moved(c.EndTime, at, gapMs)
		c.TimedAnnotations = shiftAnnotations(c.TimedAnnotations, at, gapMs)
		c.Images = shiftAnnotations(c.Images, at, gapMs)
		// a note and a card are anchored to the command's start, which is written from it when the recording is
		out[i] = c
	}
	return out
}

// InsertGap returns the commands with everything that is at or after at moved gapMs later. The commands are not changed.
func InsertGap(commands []Command, at, gapMs int64) []Command {
	return shiftCommands(commands, at, max(gapMs, 0))
}

// RemoveGap is InsertGap undone: what is at or after at+gapMs moves gapMs earlier.
func RemoveGap(commands []Command, at, gapMs int64) []Command {
	return shiftCommands(commands, at, -max(gapMs, 0))
}

// ---- adding, replacing, taking out ------------------------------------------------------------------------------------------------

// LastTimestamp is the time of the last frame of the commands: the length of the recording.
func LastTimestamp(commands []Command) int64 {
	var last int64
	for _, c := range commands {
		last = max(last, commandLastTimestamp(c), c.EndTime)
		if c.HasPrompt {
			last = max(last, c.PromptFrame.Timestamp)
		}
	}
	return last
}

// imageHost is the command a time is in: the last that had begun, or the first if the time is before them all (as a timed
// annotation's is, and as a reader of the file finds it).
func imageHost(commands []Command, ts int64) int {
	host := 0
	for i := range commands {
		if commandFirstTimestamp(commands[i]) <= ts {
			host = i
		} else {
			break
		}
	}
	return host
}

// CheckImagePlace says whether an image could be shown for a duration from a time: nothing is wrong if err is nil. ignoreID is an image
// that is about to be taken out (it is being edited) and does not count.
func CheckImagePlace(commands []Command, ts, durationSeconds int64, ignoreID string) error {
	switch {
	case ts < 0:
		return errors.New("the time is before the recording")
	case ts > LastTimestamp(commands):
		return fmt.Errorf("the recording is only %s long", ClockText(LastTimestamp(commands)))
	case durationSeconds < 1 || durationSeconds > ImageMaxSeconds:
		return fmt.Errorf("the duration is from 1 to %d seconds", ImageMaxSeconds)
	}
	for _, o := range RecordingImages(commands) {
		if o.ID != ignoreID && ts > o.Timestamp && ts < ImageEnd(o) {
			return fmt.Errorf("%s is inside the image shown from %s to %s: use %s, when it is over, or a time before it",
				ClockText(ts), ClockText(o.Timestamp), ClockText(ImageEnd(o)), ClockText(ImageEnd(o)))
		}
	}
	return nil
}

// AddImage inserts an image: everything at or after its time moves later by its duration, and the image goes to the command its
// time is in. The commands are not changed; the ones returned are.
func AddImage(commands []Command, img Annotation) ([]Command, error) {
	if len(commands) == 0 {
		return nil, errors.New("there are no commands to put an image with")
	}
	if img.Kind = AnnotationKindImage; !ValidStoredImageName(img.Image) {
		return nil, fmt.Errorf("%q is not the name of an image", img.Image)
	}
	if err := CheckImagePlace(commands, img.Timestamp, int64(img.DurationSeconds), img.ID); err != nil {
		return nil, err
	}
	if img.ID == "" {
		img.ID = fmt.Sprintf("img_%x", time.Now().UnixNano())
	}
	if img.CreatedAt == "" {
		img.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if img.ImageName == "" {
		img.ImageName = img.Image
	}
	img.Text = ImageCaption(img.ImageName)
	out := InsertGap(commands, img.Timestamp, ImageDurationMs(img))
	host := imageHost(out, img.Timestamp)
	out[host].Images = append(out[host].Images, img)
	sort.SliceStable(out[host].Images, func(i, j int) bool { return out[host].Images[i].Timestamp < out[host].Images[j].Timestamp })
	return out, nil
}

// FindImage is the image with an id, and the command that holds it.
func FindImage(commands []Command, id string) (Annotation, int, bool) {
	for i, c := range commands {
		for _, a := range c.Images {
			if a.ID == id {
				return a, i, true
			}
		}
	}
	return Annotation{}, -1, false
}

// RemoveImage takes an image out, and closes the gap it made: everything after it moves earlier by its duration.
func RemoveImage(commands []Command, id string) ([]Command, Annotation, error) {
	old, host, ok := FindImage(commands, id)
	if !ok {
		return nil, Annotation{}, fmt.Errorf("there is no image %s", id)
	}
	out := make([]Command, len(commands))
	copy(out, commands)
	out[host].Images = nil
	for _, a := range commands[host].Images {
		if a.ID != id {
			out[host].Images = append(out[host].Images, a)
		}
	}
	return RemoveGap(out, old.Timestamp, ImageDurationMs(old)), old, nil
}

// ReplaceImage changes an image - its time, its duration, its file - keeping its id and date. The time is a moment of the recording
// as it is now: the same moment is meant by it after the image has been taken out of where it was (a time after the old image is
// earlier by its duration without it; a time inside it is where it was).
func ReplaceImage(commands []Command, id string, upd Annotation) ([]Command, error) {
	old, _, ok := FindImage(commands, id)
	if !ok {
		return nil, fmt.Errorf("there is no image %s", id)
	}
	ts := upd.Timestamp
	switch end := ImageEnd(old); {
	case ts >= end:
		ts -= ImageDurationMs(old)
	case ts > old.Timestamp:
		ts = old.Timestamp
	}
	without, _, err := RemoveImage(commands, id)
	if err != nil {
		return nil, err
	}
	upd.ID, upd.CreatedAt, upd.Timestamp = old.ID, old.CreatedAt, ts
	out, err := AddImage(without, upd)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- a picture for a terminal --------------------------------------------------------------------------------------------------

// MaxPreviewPixels is the largest picture that is decoded to be drawn in a terminal: beyond it the pixels, held as they have to be,
// would take too much memory for a preview.
const MaxPreviewPixels = 16_000_000

// DecodeImageFit decodes a PNG, JPEG or GIF (its first picture) and scales it to fit in w x h pixels, keeping its shape, and
// not to more than twice its own size. A WebP, or a picture over MaxPreviewPixels, is an error: the caller shows its details
// instead.
func DecodeImageFit(data []byte, w, h int) (*SixelImage, error) {
	if w < 1 || h < 1 {
		return nil, errors.New("no room for a picture")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > MaxPreviewPixels {
		return nil, errors.New("the picture is too large to be previewed")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	img := &SixelImage{W: b.Dx(), H: b.Dy(), Pix: make([]byte, b.Dx()*b.Dy()*4)}
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA() // alpha-premultiplied, 16 bits
			if a > 0 {
				r, g, bl = r*0xffff/a, g*0xffff/a, bl*0xffff/a
			}
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(r>>8), byte(g>>8), byte(bl>>8), byte(a>>8)
			i += 4
		}
	}
	tw, th := FitSize(img.W, img.H, w, h)
	return img.Resize(tw, th), nil
}

// FitSize is the size a picture of w x h is shown at in a box of boxW x boxH: as large as the box has room for, in its own shape, and
// no more than twice its own size.
func FitSize(w, h, boxW, boxH int) (int, int) {
	if w < 1 || h < 1 || boxW < 1 || boxH < 1 {
		return 1, 1
	}
	scale := min(float64(boxW)/float64(w), float64(boxH)/float64(h), 2)
	return max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
}

// ---- uploading ---------------------------------------------------------------------------------------------------------------------

// ImageFiles are the files of the images a recording shows, as paths in the images directory beside it; missing are the names of those
// that are shown but are not there. Only the annotation lines of the file are read, so it is cheap on a long recording.
func ImageFiles(recordingPath string) (paths, missing []string, err error) {
	f, err := os.Open(recordingPath)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	var annotations []Annotation
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxRecordingLine)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"image"`)) { // every line of an image has the word, as its kind
			continue
		}
		var raw recordingLine
		if json.Unmarshal(line, &raw) == nil && raw.Type == "annotation" && raw.Kind == AnnotationKindImage {
			annotations = append(annotations, Annotation{Kind: raw.Kind, Image: raw.Image})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	for _, name := range ReferencedImages(annotations) {
		p, err := ImageFilePath(recordingPath, name)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			missing = append(missing, name)
			continue
		}
		paths = append(paths, p)
	}
	return paths, missing, nil
}
