package lib

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif"  // the formats an inline image may be in
	_ "image/jpeg" // (the standard library's)
	_ "image/png"
	"strconv"
	"strings"
)

// Inline images, the protocol iTerm2 made and WezTerm, mintty, Konsole and others speak (it is what `tdf`, `imgcat`, `viu` and
// other programs use when the terminal says it can):
//
//	ESC ] 1337 ; File = name=...;size=...;width=...;height=...;inline=1 ; ... : base64 BEL
//
// The data is an image file - PNG, JPEG, GIF - and the arguments say how it is shown:
//
//	inline=1            show it (otherwise the file is a download, and is not for the screen)
//	width, height       N: cells, Npx: pixels, N%: of the terminal, auto: the image's own (the default)
//	preserveAspectRatio 0 stretches it to the box; 1 (the default) keeps its shape
//	doNotMoveCursor=1   the cursor stays where it was; otherwise it goes below the picture
//
// Everything below works on the part between "ESC ]" and the terminator ("1337;File=..."), which is what the screen
// keeps of a picture, as it keeps the contents of a sixel's Device Control String.

const itermPrefix = "1337;File="

// MaxInlineImageBytes is the longest inline image kept, as for sixel graphics (see recorder/dcsfilter.go).
const MaxInlineImageBytes = MaxSixelBytes

var errNotITerm = errors.New("inline image: not an iTerm2 image")

// IsITermImage reports whether the contents of an OSC are an inline image.
func IsITermImage(osc string) bool { return strings.HasPrefix(osc, itermPrefix) }

// parseITerm splits "1337;File=args:data".
func parseITerm(osc string) (args map[string]string, data string, err error) {
	if !IsITermImage(osc) {
		return nil, "", errNotITerm
	}
	rest := osc[len(itermPrefix):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return nil, "", errNotITerm
	}
	args = map[string]string{}
	for _, kv := range strings.Split(rest[:colon], ";") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			args[strings.ToLower(k)] = v
		}
	}
	return args, rest[colon+1:], nil
}

// ITermMovesCursor says whether the picture takes the cursor below it (doNotMoveCursor=1 leaves it where it was).
func ITermMovesCursor(osc string) bool {
	args, _, err := parseITerm(osc)
	return err == nil && args["donotmovecursor"] != "1"
}

// ITermShown says whether the file is to be shown (inline=1), not saved.
func ITermShown(osc string) bool {
	args, _, err := parseITerm(osc)
	return err == nil && args["inline"] == "1"
}

func decodeITermImage(osc string) (image.Image, map[string]string, error) {
	args, data, err := parseITerm(osc)
	if err != nil {
		return nil, nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimRight(data, "="+"\r\n "))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(data, "="+"\r\n "))
		if err != nil {
			return nil, nil, err
		}
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	return img, args, err
}

// itermDim is one of width and height in pixels: spec as the protocol writes it, natural the image's own, cell the size of a
// cell, term the size of the terminal in cells. ok is false for auto or none.
func itermDim(spec string, natural, cell, term int) (px int, ok bool) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "" || spec == "auto":
		return natural, false
	case strings.HasSuffix(spec, "px"):
		n, err := strconv.Atoi(strings.TrimSuffix(spec, "px"))
		return n, err == nil && n > 0
	case strings.HasSuffix(spec, "%"):
		n, err := strconv.Atoi(strings.TrimSuffix(spec, "%"))
		return term * cell * n / 100, err == nil && n > 0
	}
	n, err := strconv.Atoi(spec)
	return n * cell, err == nil && n > 0
}

// ITermSize is the size in pixels the picture is shown at, given the cell and the terminal size (in cells).
func ITermSize(osc string, cellW, cellH, termCols, termRows int) (w, h int, err error) {
	args, data, err := parseITerm(osc)
	if err != nil {
		return 0, 0, err
	}
	// only the head of the file is needed for its size: the base64 of a few kilobytes
	head := strings.TrimRight(data[:min(len(data), 1<<16)], "=")
	head = head[:len(head)/4*4]
	raw, err := base64.StdEncoding.DecodeString(head)
	if err != nil && len(raw) == 0 {
		return 0, 0, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil && len(data) > 1<<16 { // a header beyond the first 48 KB (a large EXIF block): the whole file
		img, _, derr := decodeITermImage(osc)
		if derr != nil {
			return 0, 0, derr
		}
		b := img.Bounds()
		cfg, err = image.Config{Width: b.Dx(), Height: b.Dy()}, nil
	}
	if err != nil {
		return 0, 0, err
	}
	nw, nh := cfg.Width, cfg.Height
	w, wset := itermDim(args["width"], nw, cellW, termCols)
	h, hset := itermDim(args["height"], nh, cellH, termRows)
	keep := args["preserveaspectratio"] != "0"
	switch {
	case wset && !hset && keep && nw > 0:
		h = (w*nh + nw/2) / nw
	case hset && !wset && keep && nh > 0:
		w = (h*nw + nh/2) / nh
	}
	if w <= 0 || h <= 0 || w > sixelMaxDim || h > sixelMaxDim || w*h > sixelMaxPixels {
		return 0, 0, errors.New("inline image: too large or empty")
	}
	return w, h, nil
}

// DecodeITerm is the picture as pixels, at the size of the file (see ITermSize for the size it is shown at).
func DecodeITerm(osc string) (*SixelImage, error) {
	img, _, err := decodeITermImage(osc)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() > sixelMaxDim || b.Dy() > sixelMaxDim || b.Dx()*b.Dy() > sixelMaxPixels {
		return nil, errors.New("inline image: too large")
	}
	out := &SixelImage{W: b.Dx(), H: b.Dy(), Pix: make([]byte, b.Dx()*b.Dy()*4)}
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA() // alpha-premultiplied, 16 bits
			if a > 0 {
				r, g, bl = r*0xffff/a, g*0xffff/a, bl*0xffff/a
			}
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = byte(r>>8), byte(g>>8), byte(bl>>8), byte(a>>8)
			i += 4
		}
	}
	return out, nil
}

// ---- pictures of either kind -----------------------------------------------------------------------------------------

// PictureSize is the size in pixels a picture is shown at: src is the contents of a sixel's Device Control String or of an
// inline image's OSC, whichever it is. cellW, cellH and the terminal's size (in cells) matter for the latter only.
func PictureSize(src string, cellW, cellH, termCols, termRows int) (w, h int, err error) {
	if IsITermImage(src) {
		return ITermSize(src, cellW, cellH, termCols, termRows)
	}
	return SixelSize(src)
}

// DecodePicture draws a picture of either kind, at the size of its file or graphic.
func DecodePicture(src string) (*SixelImage, error) {
	if IsITermImage(src) {
		return DecodeITerm(src)
	}
	return DecodeSixel(src)
}
