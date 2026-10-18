package lib

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// pngOSC is an inline image of w x h pixels, as a program sends it: args are the arguments after "File=".
func pngOSC(t testing.TB, w, h int, args string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / max(w-1, 1)), uint8(y * 255 / max(h-1, 1)), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return "1337;File=" + args + ":" + base64.StdEncoding.EncodeToString(b.Bytes())
}

func osc(body string) string { return "\x1b]" + body + "\a" }

func TestInlineImageSizeFollowsItsArguments(t *testing.T) {
	cases := []struct {
		args string
		w, h int
	}{
		{"inline=1", 40, 20},                          // its own size
		{"inline=1;width=100px;height=50px", 100, 50}, // pixels
		{"inline=1;width=5;height=2", 50, 40},         // cells of 10 x 20
		{"inline=1;width=auto;height=2", 80, 40},      // 2 cells high, the width keeping the shape
		{"inline=1;width=200px", 200, 100},            // one given: the other keeps the shape
		{"inline=1;height=40px", 80, 40},
		{"inline=1;width=200px;preserveAspectRatio=0", 200, 20}, // not kept: the height is the image's own
		{"inline=1;width=50%", 600, 300},                        // of a terminal of 120 columns
	}
	for _, c := range cases {
		w, h, err := PictureSize(pngOSC(t, 40, 20, c.args), 10, 20, 120, 40)
		if err != nil || w != c.w || h != c.h {
			t.Errorf("%s: %dx%d, %v; want %dx%d", c.args, w, h, err, c.w, c.h)
		}
	}
	if _, _, err := PictureSize("1337;File=inline=1:bm90IGFuIGltYWdl", 10, 20, 80, 24); err == nil {
		t.Error("something that is not an image has a size")
	}
}

func TestDecodePictureOfAnInlineImage(t *testing.T) {
	img, err := DecodePicture(pngOSC(t, 6, 4, "inline=1"))
	if err != nil || img.W != 6 || img.H != 4 {
		t.Fatalf("%v %+v", err, img)
	}
	if px := img.Pix[(3*6+5)*4:]; px[0] != 255 || px[1] != 255 || px[2] != 90 || px[3] != 255 {
		t.Errorf("bottom right pixel %v", px[:4])
	}
}

func TestInlineImageTakesItsCellsAndMovesTheCursor(t *testing.T) {
	s := newImgScreen(80, 24)
	s.SetCellSize(10, 20)
	s.Feed("ab" + osc(pngOSC(t, 30, 40, "inline=1")) + "next")
	ps := s.ImagePlacements()
	if len(ps) != 1 || ps[0].Col != 2 || ps[0].Row != 0 || ps[0].Cols != 3 || ps[0].Rows != 2 {
		t.Fatalf("%+v", ps)
	}
	if got := lines(s)[2]; strings.TrimRight(got, " ") != "  next" {
		t.Errorf("the cursor should be below the picture, in its column: %q", got)
	}
}

func TestInlineImageThatDoesNotMoveTheCursorLeavesItWhereItWas(t *testing.T) {
	s := newImgScreen(80, 24)
	s.SetCellSize(10, 20)
	s.Feed("\x1b[3;5H" + osc(pngOSC(t, 30, 40, "inline=1;doNotMoveCursor=1")))
	ps := s.ImagePlacements()
	if len(ps) != 1 || ps[0].Col != 4 || ps[0].Row != 2 || ps[0].Cols != 3 || ps[0].Rows != 2 {
		t.Fatalf("%+v", ps)
	}
	s.Feed("X") // written where the cursor was, over the picture's first cell
	if ps := s.ImagePlacements(); len(ps) != 2 {
		t.Errorf("text written over the corner should have left two pieces: %d placements", len(ps))
	}
}

func TestInlineImageAtTheBottomWithoutMovingTheCursorIsClippedNotScrolled(t *testing.T) {
	s := newImgScreen(80, 24)
	s.SetCellSize(10, 20)
	s.Feed("top\x1b[23;1H" + osc(pngOSC(t, 30, 100, "inline=1;doNotMoveCursor=1"))) // 5 rows from row 23 of 24
	if got := strings.TrimRight(lines(s)[0], " "); got != "top" {
		t.Errorf("the screen scrolled: %q", got)
	}
	if ps := s.ImagePlacements(); len(ps) != 1 || ps[0].Rows != 2 || ps[0].BoxRows != 2 {
		t.Errorf("%+v", ps)
	}
}

func TestInlineImageErasedAndRedrawn(t *testing.T) {
	// what tdf does for every page: erase the area, draw the image where the cursor is, leave the cursor
	s := newImgScreen(80, 24)
	s.SetCellSize(10, 20)
	img := osc(pngOSC(t, 30, 40, "inline=1;doNotMoveCursor=1"))
	s.Feed("\x1b[2;5H" + img)
	s.Feed("\x1b[2;5H\x1b[3X\x1b[1B\x1b[3X\x1b[1A") // erase it
	if n := len(s.ImagePlacements()); n != 0 {
		t.Fatalf("%d placements after erasing", n)
	}
	s.Feed(img)
	if n := len(s.ImagePlacements()); n != 1 {
		t.Errorf("%d placements after drawing again", n)
	}
}

func TestInlineImageCutAcrossFramesAndNotShownOnesAreSkipped(t *testing.T) {
	whole := "hi" + osc(pngOSC(t, 30, 40, "inline=1")) + "!"
	for _, cut := range []int{1, 3, 7, 30, len(whole) / 2, len(whole) - 3, len(whole) - 1} {
		s := newImgScreen(80, 24)
		s.SetCellSize(10, 20)
		s.Feed(whole[:cut])
		s.Feed(whole[cut:])
		if ps := s.ImagePlacements(); len(ps) != 1 || ps[0].Cols != 3 {
			t.Fatalf("cut at %d: %+v", cut, ps)
		}
		if got := strings.Join(strings.Fields(strings.Join(lines(s), " ")), ""); got != "hi!" {
			t.Errorf("cut at %d: %q", cut, got)
		}
	}
	s := newImgScreen(80, 24)
	s.Feed(osc(pngOSC(t, 30, 40, "name=YQ==")) + osc("1337;SetUserVar=a=YQ==") + "x")
	if len(s.ImagePlacements()) != 0 {
		t.Error("a file that is not to be shown was drawn")
	}
}
