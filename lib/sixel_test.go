package lib

import (
	"strings"
	"testing"
)

func px(img *SixelImage, x, y int) [4]byte {
	o := (y*img.W + x) * 4
	return [4]byte{img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]}
}

func TestDecodeSixelBasics(t *testing.T) {
	// four red columns of six pixels, then (next band) two blue ones
	img, err := DecodeSixel("0;1;0q#1;2;100;0;0#2;2;0;0;100#1~~~~-#2~~")
	if err != nil {
		t.Fatal(err)
	}
	if img.W != 4 || img.H != 12 {
		t.Fatalf("size %dx%d", img.W, img.H)
	}
	if got := px(img, 3, 5); got != [4]byte{255, 0, 0, 255} {
		t.Errorf("red: %v", got)
	}
	if got := px(img, 1, 6); got != [4]byte{0, 0, 255, 255} {
		t.Errorf("blue: %v", got)
	}
	if got := px(img, 3, 6); got[3] != 0 {
		t.Errorf("nobody painted this one: %v", got)
	}
}

func TestSixelBitsAreTopToBottomAndRepeatsWork(t *testing.T) {
	// '@' is 1: only the top pixel; 'A' is 2: the second; !5 repeats
	img, err := DecodeSixel("q#0;2;100;100;100#0@A!5B")
	if err != nil {
		t.Fatal(err)
	}
	if img.W != 7 {
		t.Fatalf("width %d", img.W)
	}
	if px(img, 0, 0)[3] == 0 || px(img, 0, 1)[3] != 0 {
		t.Error("'@' is the top pixel only")
	}
	if px(img, 1, 1)[3] == 0 || px(img, 1, 0)[3] != 0 {
		t.Error("'A' is the second pixel only")
	}
	for x := 2; x < 7; x++ {
		if px(img, x, 1)[3] == 0 {
			t.Errorf("column %d of the repeat is missing", x)
		}
	}
}

func TestSixelCarriageReturnPaintsAnotherColourOnTheSameBand(t *testing.T) {
	img, err := DecodeSixel("q#1;2;100;0;0#2;2;0;100;0#1@@$#2AA")
	if err != nil {
		t.Fatal(err)
	}
	if px(img, 0, 0) != [4]byte{255, 0, 0, 255} || px(img, 0, 1) != [4]byte{0, 255, 0, 255} {
		t.Errorf("%v %v", px(img, 0, 0), px(img, 0, 1))
	}
}

func TestSixelRasterAttributesSetTheSizeAndTheDefaultPaletteIsThere(t *testing.T) {
	img, err := DecodeSixel("q\"1;1;20;30#1~")
	if err != nil {
		t.Fatal(err)
	}
	if img.W != 20 || img.H != 30 {
		t.Errorf("%dx%d", img.W, img.H)
	}
	if px(img, 0, 0) != [4]byte{51, 51, 204, 255} { // register 1 of the VT340's palette: blue
		t.Errorf("default register 1: %v", px(img, 0, 0))
	}
}

func TestSixelHLSColour(t *testing.T) {
	// hue 120 on the DEC circle is red (its 0 is blue)
	img, err := DecodeSixel("q#3;1;120;50;100#3~")
	if err != nil {
		t.Fatal(err)
	}
	if c := px(img, 0, 0); c[0] < 250 || c[1] > 5 || c[2] > 5 {
		t.Errorf("not red: %v", c)
	}
}

func TestSixelRefusesTheEnormousAndTheNotSixel(t *testing.T) {
	if _, err := DecodeSixel("q!9999~"); err == nil {
		t.Error("9999 columns")
	}
	if _, err := DecodeSixel("q" + strings.Repeat("~-", 800)); err == nil {
		t.Error("4800 rows")
	}
	if _, err := DecodeSixel("$qmore"); err != ErrNotSixel {
		t.Errorf("%v", err)
	}
	if _, _, err := SixelSize("1000p"); err != ErrNotSixel {
		t.Errorf("%v", err)
	}
	if IsSixel("+q4d6f") || !IsSixel("0;0;8q#0~") || !IsSixel("q") {
		t.Error("IsSixel")
	}
	if got, _, err := SixelSize("q"); err != nil || got != 0 {
		t.Errorf("an empty graphic: %d %v", got, err)
	}
}

func gradient(w, h int) *SixelImage {
	img := &SixelImage{W: w, H: h, Pix: make([]byte, w*h*4)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 4
			if x >= 3 && x < 6 && y >= 4 && y < 9 { // a hole
				continue
			}
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = byte(x*255/(w-1)), byte(y*255/(h-1)), byte((x+y)*255/(w+h-2)), 255
		}
	}
	return img
}

func TestEncodeDecodeRoundTripKeepsAFewColoursExactly(t *testing.T) {
	img := &SixelImage{W: 13, H: 14, Pix: make([]byte, 13*14*4)}
	cols := [][3]byte{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}}
	for y := 0; y < 14; y++ {
		for x := 0; x < 13; x++ {
			if (x+y)%7 == 6 {
				continue // unpainted
			}
			c := cols[(x/4+y/5)%3]
			o := (y*13 + x) * 4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = c[0], c[1], c[2], 255
		}
	}
	enc := EncodeSixel(img)
	if !strings.HasPrefix(enc, "\x1bP0;1;0q") || !strings.HasSuffix(enc, "\x1b\\") {
		t.Fatalf("framing: %q", enc[:12])
	}
	back, err := DecodeSixel(strings.TrimSuffix(strings.TrimPrefix(enc, "\x1bP"), "\x1b\\"))
	if err != nil {
		t.Fatal(err)
	}
	if back.W != 13 || back.H != 14 {
		t.Fatalf("%dx%d", back.W, back.H)
	}
	for i := range img.Pix {
		if img.Pix[i] != back.Pix[i] {
			t.Fatalf("byte %d: %d != %d", i, img.Pix[i], back.Pix[i])
		}
	}
}

func TestEncodeReducesManyColoursClose(t *testing.T) {
	img := gradient(40, 30) // far more than 255 colours
	enc := EncodeSixel(img)
	back, err := DecodeSixel(strings.TrimSuffix(strings.TrimPrefix(enc, "\x1bP"), "\x1b\\"))
	if err != nil {
		t.Fatal(err)
	}
	if back.W != 40 || back.H != 30 {
		t.Fatalf("%dx%d", back.W, back.H)
	}
	worst := 0
	for i := 0; i < 40*30; i++ {
		if (img.Pix[i*4+3] == 0) != (back.Pix[i*4+3] == 0) {
			t.Fatalf("pixel %d: the hole moved", i)
		}
		if img.Pix[i*4+3] == 0 {
			continue
		}
		for c := 0; c < 3; c++ {
			d := int(img.Pix[i*4+c]) - int(back.Pix[i*4+c])
			if d < 0 {
				d = -d
			}
			worst = max(worst, d)
		}
	}
	if worst > 40 {
		t.Errorf("a colour is off by %d", worst)
	}
}

func TestResizeAndCrop(t *testing.T) {
	img := &SixelImage{W: 4, H: 4, Pix: make([]byte, 64)}
	for i := 0; i < 16; i++ {
		v := byte(0)
		if i%4 >= 2 {
			v = 200
		}
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = v, v, v, 255
	}
	half := img.Resize(2, 2)
	if px(half, 0, 0)[0] != 0 || px(half, 1, 1)[0] != 200 {
		t.Errorf("down: %v %v", px(half, 0, 0), px(half, 1, 1))
	}
	if up := img.Resize(8, 8); px(up, 3, 0)[0] != 0 || px(up, 4, 0)[0] != 200 {
		t.Errorf("up: %v %v", px(up, 3, 0), px(up, 4, 0))
	}
	c := img.Crop(2, 1, 5, 2)
	if c.W != 2 || c.H != 2 || px(c, 0, 0)[0] != 200 {
		t.Errorf("crop: %dx%d %v", c.W, c.H, px(c, 0, 0))
	}
	if e := img.Crop(9, 9, 2, 2); e.W != 0 {
		t.Error("crop outside")
	}
	// a mostly unpainted area stays unpainted
	hole := &SixelImage{W: 2, H: 1, Pix: []byte{9, 9, 9, 255, 0, 0, 0, 0}}
	if px(hole.Resize(1, 1), 0, 0)[3] != 255 && px(hole.Resize(1, 1), 0, 0)[3] != 0 {
		t.Error("alpha is 0 or 255")
	}
}
