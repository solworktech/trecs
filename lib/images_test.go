package lib

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// markedRecording writes a recording of three commands, which begin at 1s, 11s and 21s (the prompt, the typing 0.5s after it, the
// output a second after it), and returns its path.
func markedRecording(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	var b strings.Builder
	enc := json.NewEncoder(&b)
	_ = enc.Encode(RecordingMetaLine{Type: "meta", Width: 80, Height: 24})
	for k := 0; k < 3; k++ {
		s := int64(1000 + k*10000)
		for _, f := range []TerminalFrame{
			{Timestamp: s, Data: "\x1b]133;A\x07$ \x1b]133;B\x07"},
			{Timestamp: s + 500, Data: "echo hi"},
			{Timestamp: s + 900, Data: "\r\n\x1b]133;C\x07"},
			{Timestamp: s + 1000, Data: "hi\r\n"},
			{Timestamp: s + 1500, Data: "\x1b]133;D;0\x07"},
		} {
			_ = enc.Encode(recordingLine{Timestamp: f.Timestamp, Data: f.Data})
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 5), 120, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func loadCommands(t *testing.T, path string) []Command {
	t.Helper()
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	return cmds
}

func allTimestamps(cmds []Command) []int64 {
	var ts []int64
	for _, c := range cmds {
		if c.HasPrompt {
			ts = append(ts, c.PromptFrame.Timestamp)
		}
		for _, f := range c.InputFrames {
			ts = append(ts, f.Timestamp)
		}
		for _, f := range c.OutputFrames {
			ts = append(ts, f.Timestamp)
		}
	}
	return ts
}

func newImage(t *testing.T, at int64, seconds int, name string) Annotation {
	t.Helper()
	info, err := InspectImage(name, pngBytes(t, 20, 10))
	if err != nil {
		t.Fatal(err)
	}
	return Annotation{Timestamp: at, DurationSeconds: seconds, Image: info.Stored, ImageName: info.Name}
}

func TestInspectImageKnowsWhatItIsByItsContents(t *testing.T) {
	var jb, gb bytes.Buffer
	_ = jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 8, 6)), nil)
	_ = gif.Encode(&gb, image.NewPaletted(image.Rect(0, 0, 4, 3), color.Palette{color.Black, color.White}), nil)
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00"), 0x2f, 0x00, 0x00, 0x1f, 0x00, 0x00) // 48x32
	for _, c := range []struct {
		name string
		data []byte
		ext  string
		w, h int
	}{
		{"/home/me/pictures/shot.PNG", pngBytes(t, 20, 10), "png", 20, 10},
		{"photo.bin", jb.Bytes(), "jpg", 8, 6}, // the name says nothing
		{"anim.gif", gb.Bytes(), "gif", 4, 3},
		{"x.webp", webp, "webp", 48, 32},
	} {
		info, err := InspectImage(c.name, c.data)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !strings.HasSuffix(info.Stored, "."+c.ext) || !ValidStoredImageName(info.Stored) || info.W != c.w || info.H != c.h {
			t.Errorf("%s: %+v", c.name, info)
		}
		if strings.ContainsAny(info.Name, "/\\") {
			t.Errorf("the directory is kept in %q", info.Name)
		}
		if ImageMIME(info.Stored) != ImageTypes[c.ext] {
			t.Errorf("%s: type %q", c.name, ImageMIME(info.Stored))
		}
	}
	a, _ := InspectImage("a.png", pngBytes(t, 20, 10))
	b, _ := InspectImage("elsewhere/b.png", pngBytes(t, 20, 10))
	c, _ := InspectImage("c.png", pngBytes(t, 21, 10))
	if a.Stored != b.Stored || a.Stored == c.Stored {
		t.Errorf("the name is of the contents: %s %s %s", a.Stored, b.Stored, c.Stored)
	}
}

func TestInspectImageRefuses(t *testing.T) {
	big := append(pngBytes(t, 20, 10), make([]byte, MaxImageBytes)...)
	huge := pngBytes(t, 20, 10)
	for name, data := range map[string][]byte{
		"empty":    nil,
		"text":     []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"),
		"too big":  big,
		"truncate": pngBytes(t, 20, 10)[:20],
		"huge":     append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\xff\xff\x00\x00\xff\xff\x08\x06\x00\x00\x00"), huge[33:]...),
	} {
		if _, err := InspectImage(name, data); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}

func TestStoredImageNamesCannotLeaveTheDirectory(t *testing.T) {
	for _, bad := range []string{"", "../x.png", "img-../../etc/passwd", "img-0123456789ab.svg", "img-0123456789ab.png/x", "IMG-0123456789ab.png", "/img-0123456789ab.png", "img-0123456789a.png"} {
		if ValidStoredImageName(bad) {
			t.Errorf("%q is a name", bad)
		}
		if _, err := ImageFilePath("/rec/terminal.jsonl", bad); err == nil {
			t.Errorf("%q has a path", bad)
		}
	}
	if p, err := ImageFilePath("/rec/terminal.jsonl", "img-0123456789ab.png"); err != nil || p != "/rec/images/img-0123456789ab.png" {
		t.Errorf("%q %v", p, err)
	}
}

func TestAddImageMakesRoomAndRoundTrips(t *testing.T) {
	path := markedRecording(t)
	cmds := loadCommands(t, path)
	if len(cmds) != 3 {
		t.Fatalf("%d commands", len(cmds))
	}
	before := allTimestamps(cmds)
	last := LastTimestamp(cmds)

	// 12.2 s is in the second command, which begins at 11 s: its output (12.0) is before, its end (12.5) after
	img := newImage(t, 12200, 4, "/home/me/shot.png")
	out, err := AddImage(cmds, img)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds[1].Images) != 0 || allTimestamps(cmds)[3] != before[3] {
		t.Error("the commands that were given were changed")
	}
	after := allTimestamps(out)
	if len(after) != len(before) {
		t.Fatal("frames came or went")
	}
	for i := range before {
		want := before[i]
		if want >= 12200 {
			want += 4000
		}
		if after[i] != want {
			t.Errorf("frame %d: %d, want %d", i, after[i], want)
		}
	}
	if got := LastTimestamp(out); got != last+4000 {
		t.Errorf("the recording is %d, want %d", got, last+4000)
	}
	if len(out[1].Images) != 1 || len(out[0].Images)+len(out[2].Images) != 0 {
		t.Errorf("it is with the command its time is in: %d %d %d", len(out[0].Images), len(out[1].Images), len(out[2].Images))
	}

	// written, and read back
	if err := RebuildRecording(path, path+".backup", out); err != nil {
		t.Fatal(err)
	}
	_, anns, _, err := LoadFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	var got *Annotation
	for i := range anns {
		if anns[i].Kind == AnnotationKindImage {
			got = &anns[i]
		}
	}
	if got == nil || got.Timestamp != 12200 || got.DurationSeconds != 4 || got.Image != img.Image || got.ImageName != "shot.png" || got.Text != "display image: shot.png" || got.ID == "" {
		t.Fatalf("read back %+v", got)
	}
	again := loadCommands(t, path)
	if len(again[1].Images) != 1 || again[1].Images[0].Image != img.Image {
		t.Errorf("held by the second command: %d %d %d", len(again[0].Images), len(again[1].Images), len(again[2].Images))
	}
	if LastTimestamp(again) != last+4000 {
		t.Errorf("duration %d", LastTimestamp(again))
	}
	// the file's own order: annotations first, frames after, and the frames in time order
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	seenFrame := false
	var prev int64
	for _, l := range lines {
		var r recordingLine
		_ = json.Unmarshal([]byte(l), &r)
		switch r.Type {
		case "annotation":
			if seenFrame {
				t.Error("an annotation after a frame")
			}
		case "meta":
		default:
			seenFrame = true
			if r.Timestamp < prev {
				t.Errorf("frames out of order at %d", r.Timestamp)
			}
			prev = r.Timestamp
		}
	}
}

func TestRemoveImageGivesTheTimeBack(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	before := allTimestamps(cmds)
	out, err := AddImage(cmds, newImage(t, 12200, 4, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	id := out[1].Images[0].ID
	back, old, err := RemoveImage(out, id)
	if err != nil {
		t.Fatal(err)
	}
	if old.ID != id {
		t.Error("what was taken out")
	}
	for i, ts := range allTimestamps(back) {
		if ts != before[i] {
			t.Errorf("frame %d: %d, was %d", i, ts, before[i])
		}
	}
	if len(RecordingImages(back)) != 0 {
		t.Error("still there")
	}
	if _, _, err := RemoveImage(back, id); err == nil {
		t.Error("taken out twice")
	}
}

func TestReplaceImage(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	before := allTimestamps(cmds)
	out, _ := AddImage(cmds, newImage(t, 12200, 4, "a.png"))
	id := out[1].Images[0].ID
	created := out[1].Images[0].CreatedAt

	// longer: what follows moves by the difference only
	longer := out[1].Images[0]
	longer.DurationSeconds = 10
	got, err := ReplaceImage(out, id, longer)
	if err != nil {
		t.Fatal(err)
	}
	for i, ts := range allTimestamps(got) {
		want := before[i]
		if want >= 12200 {
			want += 10000
		}
		if ts != want {
			t.Errorf("longer, frame %d: %d, want %d", i, ts, want)
		}
	}
	if im := got[1].Images[0]; im.ID != id || im.CreatedAt != created || im.DurationSeconds != 10 {
		t.Errorf("%+v", im)
	}

	// moved to a later time (a moment of the recording as it is: 25 s, which is 21 s of the recording without the 4 s)
	moved := out[1].Images[0]
	moved.Timestamp = 25000
	got, err = ReplaceImage(out, id, moved)
	if err != nil {
		t.Fatal(err)
	}
	// (it is at the moment the third command begins, which the image is before: the second is the last that had begun)
	if len(got[1].Images) != 1 || got[1].Images[0].Timestamp != 21000 {
		t.Fatalf("at 21 s, with the second: %d %d %d", len(got[0].Images), len(got[1].Images), len(got[2].Images))
	}
	for i, ts := range allTimestamps(got) {
		want := before[i]
		if want >= 21000 {
			want += 4000
		}
		if ts != want {
			t.Errorf("moved, frame %d: %d, want %d", i, ts, want)
		}
	}

	// a time inside itself is where it was
	inside := out[1].Images[0]
	inside.Timestamp = 14000
	got, err = ReplaceImage(out, id, inside)
	if err != nil || got[1].Images[0].Timestamp != 12200 {
		t.Errorf("%v %+v", err, got[1].Images)
	}
}

func TestImagesDoNotOverlap(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	out, _ := AddImage(cmds, newImage(t, 12200, 4, "a.png"))
	for _, c := range []struct {
		at   int64
		ok   bool
		what string
	}{
		{13000, false, "inside the first"},
		{12201, false, "just inside"},
		{12200, true, "at its start: it goes before"},
		{16200, true, "at its end"},
		{5000, true, "before"},
		{-1, false, "before the recording"},
		{LastTimestamp(out) + 1, false, "after the recording"},
		{LastTimestamp(out), true, "at the last frame"},
	} {
		_, err := AddImage(out, newImage(t, c.at, 2, "b.png"))
		if (err == nil) != c.ok {
			t.Errorf("%s (%d): %v", c.what, c.at, err)
		}
	}
	for _, d := range []int{0, -3, ImageMaxSeconds + 1} {
		if _, err := AddImage(cmds, newImage(t, 5000, d, "c.png")); err == nil {
			t.Errorf("a duration of %d", d)
		}
	}
	bad := newImage(t, 5000, 3, "c.png")
	bad.Image = "../../etc/passwd"
	if _, err := AddImage(cmds, bad); err == nil {
		t.Error("a name that leads out")
	}
}

func TestImageAtIsHalfOpen(t *testing.T) {
	imgs := []Annotation{{Timestamp: 5000, DurationSeconds: 3, ID: "a"}, {Timestamp: 8000, DurationSeconds: 2, ID: "b"}}
	for pos, want := range map[int64]string{4999: "", 5000: "a", 7999: "a", 8000: "b", 9999: "b", 10000: ""} {
		got, ok := ImageAt(imgs, pos)
		if (want == "") == ok || got.ID != want {
			t.Errorf("at %d: %q %v", pos, got.ID, ok)
		}
	}
}

// A picture put in the quiet after a command is not the command's: deleting the command takes the command's time, and the picture
// stays with its time.
func TestDeletingACommandKeepsThePictureThatFollowsIt(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	// in the quiet after the second command's last frame (12.5 s), before the third begins (21 s): the second holds it
	out, err := AddImage(cmds, newImage(t, 15000, 6, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out[1].Images) != 1 {
		t.Fatal("held by the second")
	}
	got := CompressTimestamps(out, map[int]bool{1: true})
	imgs := RecordingImages(got)
	if len(got) != 2 || len(imgs) != 1 {
		t.Fatalf("%d commands, %d images", len(got), len(imgs))
	}
	// what was deleted is the second command's span (11 s to 12.5 s); the picture is that much earlier, and the third begins
	// where it did before the picture was put in, less the span: the picture's time is still there
	if imgs[0].Timestamp != 15000-1500 {
		t.Errorf("the picture is at %d, want %d", imgs[0].Timestamp, 15000-1500)
	}
	if want := out[2].PromptFrame.Timestamp - 1500; got[1].PromptFrame.Timestamp != want {
		t.Errorf("the third begins at %d, want %d", got[1].PromptFrame.Timestamp, want)
	}
	// and one in the command goes with it
	inside, err := AddImage(cmds, newImage(t, 11500, 2, "b.png"))
	if err != nil {
		t.Fatal(err)
	}
	if kept := RecordingImages(CompressTimestamps(inside, map[int]bool{1: true})); len(kept) != 0 {
		t.Errorf("a picture inside the deleted command was kept: %+v", kept)
	}
}

func TestMergeCopiesTheImages(t *testing.T) {
	a, b := markedRecording(t), markedRecording(t)
	cmds := loadCommands(t, a)
	data := pngBytes(t, 20, 10)
	info, _ := InspectImage("a.png", data)
	out, err := AddImage(cmds, Annotation{Timestamp: 12200, DurationSeconds: 4, Image: info.Stored, ImageName: info.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildRecording(a, a+".backup", out); err != nil {
		t.Fatal(err)
	}
	if err := SaveImage(a, data, info); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "merged.jsonl")
	if err := MergeRecordings([]string{a, b}, dest, 1000, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(filepath.Dir(dest), ImagesDir, info.Stored)); err != nil || !bytes.Equal(got, data) {
		t.Errorf("the image beside the merged recording: %v", err)
	}
	_, anns, _, _ := LoadFrames(dest)
	n := 0
	for _, x := range anns {
		if x.Kind == AnnotationKindImage && x.Image == info.Stored && x.ImageName == "a.png" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d images in the merged recording", n)
	}
}

func TestMarkdownShowsTheImage(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	out, _ := AddImage(cmds, newImage(t, 12200, 4, "a [odd].png"))
	md := MarkdownFromCommands("t", out)
	if !strings.Contains(md, "**[00:12]**") || !strings.Contains(md, "![display image: a (odd).png](images/img-") {
		t.Errorf("\n%s", md)
	}
}

func TestAnOldReaderSeesAnImageAsANote(t *testing.T) {
	// the line is an annotation, with a caption for a text: a reader that does not know of kind "image" attaches it to the command as it
	// would a note (and does not read it as a frame, which would put an empty one at the end of the recording)
	path := markedRecording(t)
	cmds := loadCommands(t, path)
	out, _ := AddImage(cmds, newImage(t, 12200, 4, "a.png"))
	if err := RebuildRecording(path, path+".backup", out); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(l, `"kind":"image"`) {
			var m map[string]any
			_ = json.Unmarshal([]byte(l), &m)
			if m["type"] != "annotation" || m["text"] != "display image: a.png" || m["timestamp"] != float64(12200) || m["durationSeconds"] != float64(4) {
				t.Errorf("%s", l)
			}
			return
		}
	}
	t.Error("no image line")
}

func TestImageFilesAreTheOnesTheRecordingShowsAndSaysWhichAreMissing(t *testing.T) {
	path := markedRecording(t)
	cmds := loadCommands(t, path)
	data := pngBytes(t, 20, 10)
	info, _ := InspectImage("a.png", data)
	other, _ := InspectImage("b.png", pngBytes(t, 22, 10))
	out, err := AddImage(cmds, Annotation{Timestamp: 5000, DurationSeconds: 2, Image: info.Stored, ImageName: info.Name})
	if err != nil {
		t.Fatal(err)
	}
	if out, err = AddImage(out, Annotation{Timestamp: 15000, DurationSeconds: 2, Image: other.Stored, ImageName: other.Name}); err != nil {
		t.Fatal(err)
	}
	if err := RebuildRecording(path, path+".backup", out); err != nil {
		t.Fatal(err)
	}
	if err := SaveImage(path, data, info); err != nil { // the other one is not kept
		t.Fatal(err)
	}
	paths, missing, err := ImageFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != info.Stored || len(missing) != 1 || missing[0] != other.Stored {
		t.Errorf("paths %v, missing %v", paths, missing)
	}
	// none, for a recording without images
	if paths, missing, err := ImageFiles(markedRecording(t)); err != nil || len(paths) != 0 || len(missing) != 0 {
		t.Errorf("%v %v %v", paths, missing, err)
	}
}

// What a sound has to lose when commands are deleted is what the compression takes out of the recording: the commands' spans,
// and not the picture that follows one.
func TestRemovedSpansAreWhatCompressTimestampsTakesOut(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	out, err := AddImage(cmds, newImage(t, 15000, 6, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	deleted := map[int]bool{1: true}
	spans := RemovedSpans(out, deleted)
	if len(spans) != 1 || spans[0].AtMs != out[1].PromptFrame.Timestamp || spans[0].DurMs != 1500 {
		t.Fatalf("spans %+v", spans)
	}
	got := CompressTimestamps(out, deleted)
	if shift := out[2].PromptFrame.Timestamp - got[1].PromptFrame.Timestamp; shift != spans[0].DurMs {
		t.Errorf("the third moved %d, the span is %d", shift, spans[0].DurMs)
	}
	if RemovedSpans(out, nil) != nil {
		t.Error("nothing deleted, nothing removed")
	}
}

// The first command deleted: what followed it goes to the first that stays.
func TestAPictureAfterADeletedFirstCommandGoesToTheNext(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	first := commandLastTimestamp(cmds[0])
	out, err := AddImage(cmds, newImage(t, first+500, 3, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	got := CompressTimestamps(out, map[int]bool{0: true})
	imgs := RecordingImages(got)
	if len(imgs) != 1 || imgs[0].Timestamp != first+500-(first-commandFirstTimestamp(cmds[0])) {
		t.Errorf("%+v", imgs)
	}
}
