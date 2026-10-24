package lib

import (
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gifRecording writes a recording of cols x rows characters with the given frames (a timestamp in milliseconds, then its data).
func gifRecording(t *testing.T, cols, rows int, frames ...any) string {
	t.Helper()
	lines := []string{mustJSON(t, RecordingMetaLine{Type: "meta", Width: cols, Height: rows})}
	for i := 0; i+1 < len(frames); i += 2 {
		lines = append(lines, mustJSON(t, TerminalFrame{Timestamp: int64(frames[i].(int)), Data: frames[i+1].(string)}))
	}
	path := filepath.Join(t.TempDir(), "r.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// exportGIF makes the GIF of a recording and reads it back.
func exportGIF(t *testing.T, path string, o GIFOptions) *gif.GIF {
	t.Helper()
	out := filepath.Join(filepath.Dir(path), "r.gif")
	if _, err := ExportToGIF(path, out, o); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatalf("not a GIF: %v", err)
	}
	return g
}

// pictureAt is what the GIF shows after its n-th picture (each picture is only what changed, laid over the one before).
func pictureAt(g *gif.GIF, n int) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	for i := 0; i <= n; i++ {
		draw.Draw(canvas, g.Image[i].Bounds(), g.Image[i], g.Image[i].Bounds().Min, draw.Src)
	}
	return canvas
}

func totalDelay(g *gif.GIF) time.Duration {
	d := 0
	for _, x := range g.Delay {
		d += x
	}
	return time.Duration(d) * 10 * time.Millisecond
}

// cellRect is where the cell at column x, row y is in a GIF drawn with the default font size.
func cellRect(t *testing.T, x, y int) image.Rectangle {
	t.Helper()
	f, err := loadGIFFonts("", gifDefaultFontSize)
	if err != nil {
		t.Fatal(err)
	}
	return image.Rect(f.cw+x*f.cw, f.cw+y*f.ch, f.cw+(x+1)*f.cw, f.cw+(y+1)*f.ch)
}

func rgba(r, g, b uint8) color.RGBA { return color.RGBA{r, g, b, 255} }

func TestGIFHasTheSizeOfTheTerminalAndTheLengthOfTheRecording(t *testing.T) {
	path := gifRecording(t, 20, 5, 0, "hello", 1000, "\r\nworld", 1500, "!")
	g := exportGIF(t, path, GIFOptions{})
	r := cellRect(t, 19, 4)
	if g.Config.Width != r.Max.X+r.Dx() || g.Config.Height != r.Max.Y+r.Dx() {
		t.Errorf("the picture is %dx%d, and 20x5 characters (with a margin) are %dx%d", g.Config.Width, g.Config.Height, r.Max.X+r.Dx(), r.Max.Y+r.Dx())
	}
	if len(g.Image) != 3 {
		t.Errorf("%d pictures, want 3 (one for each time the screen changed)", len(g.Image))
	}
	// 1.5 s of recording, then the last picture is held for 3 s
	if got := totalDelay(g); got < 4400*time.Millisecond || got > 4600*time.Millisecond {
		t.Errorf("the GIF lasts %v, want 4.5 s", got)
	}
	for i, d := range g.Delay {
		if d < 2 {
			t.Errorf("picture %d stays %d hundredths of a second: viewers show less than 2 as 10", i, d)
		}
	}
	if g.LoopCount != 0 {
		t.Errorf("loop count %d: it should repeat for ever", g.LoopCount)
	}
}

func TestGIFCutsLongPausesAndKeepsThemOnRequest(t *testing.T) {
	path := gifRecording(t, 20, 3, 0, "a", 60000, "b")
	if got := totalDelay(exportGIF(t, path, GIFOptions{Hold: -1})); got < 1900*time.Millisecond || got > 2100*time.Millisecond {
		t.Errorf("a pause of a minute is %v in the GIF, want the 2 s that are the limit", got)
	}
	if got := totalDelay(exportGIF(t, path, GIFOptions{Hold: -1, IdleLimit: -1})); got < 59900*time.Millisecond || got > 60100*time.Millisecond {
		t.Errorf("with no limit the pause is %v, want a minute", got)
	}
	if got := totalDelay(exportGIF(t, path, GIFOptions{Hold: -1, IdleLimit: 500 * time.Millisecond})); got < 400*time.Millisecond || got > 600*time.Millisecond {
		t.Errorf("with a limit of half a second the pause is %v", got)
	}
}

func TestGIFSpeedAndHold(t *testing.T) {
	path := gifRecording(t, 20, 3, 0, "a", 4000, "b")
	g := exportGIF(t, path, GIFOptions{Hold: -1, IdleLimit: -1, Speed: 2})
	if got := totalDelay(g); got < 1900*time.Millisecond || got > 2100*time.Millisecond {
		t.Errorf("4 s at twice the speed is %v", got)
	}
	g = exportGIF(t, path, GIFOptions{Hold: 5 * time.Second, IdleLimit: -1})
	if last := time.Duration(g.Delay[len(g.Delay)-1]) * 10 * time.Millisecond; last != 5*time.Second {
		t.Errorf("the last picture stays %v, want the 5 s asked for", last)
	}
}

func TestGIFLimitsPicturesPerSecond(t *testing.T) {
	var frames []any
	for i := 0; i < 100; i++ { // a character every 10 ms, for a second
		frames = append(frames, i*10, "x")
	}
	path := gifRecording(t, 120, 3, frames...)
	if n := len(exportGIF(t, path, GIFOptions{FPS: 10, Hold: -1}).Image); n < 9 || n > 11 {
		t.Errorf("%d pictures for a second at 10 a second", n)
	}
	if n := len(exportGIF(t, path, GIFOptions{FPS: 50, Hold: -1}).Image); n < 45 || n > 51 {
		t.Errorf("%d pictures for a second at 50 a second", n)
	}
}

func TestGIFDrawsColoursAndText(t *testing.T) {
	path := gifRecording(t, 10, 2, 0, "\x1b[41m \x1b[0m\x1b[38;2;10;200;30m█\x1b[0m\x1b[?25l")
	g := exportGIF(t, path, GIFOptions{})
	pic := pictureAt(g, len(g.Image)-1)
	red, _ := parseHexRGB(ansi16Hex[1])
	if got := pic.At(cellRect(t, 0, 0).Min.X+2, cellRect(t, 0, 0).Min.Y+2); got != rgba(red[0], red[1], red[2]) {
		t.Errorf("a red background is %v", got)
	}
	if got := pic.At(cellRect(t, 1, 0).Min.X+2, cellRect(t, 1, 0).Min.Y+2); got != rgba(10, 200, 30) {
		t.Errorf("a full block in 24-bit colour is %v", got)
	}
	bg, _ := parseHexRGB(gifDefaultBG)
	if got := pic.At(cellRect(t, 5, 1).Min.X+2, cellRect(t, 5, 1).Min.Y+2); got != rgba(bg[0], bg[1], bg[2]) {
		t.Errorf("an empty cell is %v, not the background", got)
	}
}

func TestGIFTextIsDrawnWithTheForeground(t *testing.T) {
	path := gifRecording(t, 10, 2, 0, "\x1b[?25lMMM")
	g := exportGIF(t, path, GIFOptions{})
	pic := pictureAt(g, len(g.Image)-1)
	fg, _ := parseHexRGB(gifDefaultFG)
	n := 0
	r := cellRect(t, 1, 0)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if pic.At(x, y) == rgba(fg[0], fg[1], fg[2]) {
				n++
			}
		}
	}
	if n < 10 {
		t.Errorf("an M has %d pixels in the text colour", n)
	}
}

func TestGIFBlocksFillTheirCellsExactly(t *testing.T) {
	path := gifRecording(t, 10, 2, 0, "\x1b[?25l\x1b[32m▀▄▌▐▖░")
	pic := pictureAt(exportGIF(t, path, GIFOptions{}), 0)
	green := pic.At(cellRect(t, 0, 0).Min.X, cellRect(t, 0, 0).Min.Y)
	bg, _ := parseHexRGB(gifDefaultBG)
	back := rgba(bg[0], bg[1], bg[2])
	if green == back {
		t.Fatal("the upper half is not drawn")
	}
	at := func(col int, fx, fy float64) color.Color { // a point in a cell, as a fraction of its size
		r := cellRect(t, col, 0)
		return pic.At(r.Min.X+int(fx*float64(r.Dx())), r.Min.Y+int(fy*float64(r.Dy())))
	}
	for _, c := range []struct {
		col    int
		fx, fy float64
		want   color.Color
		name   string
	}{
		{0, 0.5, 0.1, green, "the top of an upper half"}, {0, 0.5, 0.9, back, "the bottom of an upper half"},
		{1, 0.5, 0.1, back, "the top of a lower half"}, {1, 0.5, 0.9, green, "the bottom of a lower half"},
		{2, 0.1, 0.5, green, "the left of a left half"}, {2, 0.9, 0.5, back, "the right of a left half"},
		{3, 0.1, 0.5, back, "the left of a right half"}, {3, 0.9, 0.5, green, "the right of a right half"},
		{0, 0.5, 0.45, green, "just above the middle of an upper half"}, {0, 0.5, 0.55, back, "just below the middle of an upper half"},
		{1, 0.5, 0.45, back, "just above the middle of a lower half"}, {1, 0.5, 0.55, green, "just below the middle of a lower half"},
		{2, 0.45, 0.5, green, "just left of the middle of a left half"}, {2, 0.55, 0.5, back, "just right of the middle of a left half"},
		{3, 0.45, 0.5, back, "just left of the middle of a right half"}, {3, 0.55, 0.5, green, "just right of the middle of a right half"},
		{4, 0.1, 0.9, green, "the lower left quadrant"}, {4, 0.9, 0.9, back, "the lower right of a lower left quadrant"},
	} {
		if got := at(c.col, c.fx, c.fy); got != c.want {
			t.Errorf("%s is %v, want %v", c.name, got, c.want)
		}
	}
	if got := at(5, 0.5, 0.5); got == back || got == green {
		t.Errorf("a light shade is %v: it should be between the two", got)
	}
	// no gap between a block and the cell beside it
	r0, r1 := cellRect(t, 0, 0), cellRect(t, 1, 0)
	if r0.Max.X != r1.Min.X {
		t.Fatalf("cells are not adjacent: %v %v", r0, r1)
	}
}

func TestGIFBoxLinesJoinAcrossCells(t *testing.T) {
	path := gifRecording(t, 10, 3, 0, "\x1b[?25l\x1b[33m┌─┐\r\n│ │\r\n└─┘")
	pic := pictureAt(exportGIF(t, path, GIFOptions{}), 0)
	bg, _ := parseHexRGB(gifDefaultBG)
	back := rgba(bg[0], bg[1], bg[2])
	mid := cellRect(t, 1, 0) // the middle of the top line: a row of pixels in the colour across the whole cell, with none above or below it
	filled := func(y int) bool {
		for x := mid.Min.X; x < mid.Max.X; x++ {
			if pic.At(x, y) == back {
				return false
			}
		}
		return true
	}
	rows := 0
	for y := mid.Min.Y; y < mid.Max.Y; y++ {
		if filled(y) {
			rows++
		}
	}
	if rows < 1 || rows > 3 {
		t.Errorf("a horizontal line is %d pixels thick", rows)
	}
	// the vertical line of the second row reaches both its top and its bottom edge, so it meets the corners
	v := cellRect(t, 0, 1)
	for _, y := range []int{v.Min.Y, v.Max.Y - 1} {
		hit := false
		for x := v.Min.X; x < v.Max.X; x++ {
			hit = hit || pic.At(x, y) != back
		}
		if !hit {
			t.Errorf("the vertical line stops short of row %d of its cell", y-v.Min.Y)
		}
	}
	// and the corner has something where the two lines meet: down from the top corner's middle and right from it
	c := cellRect(t, 0, 0)
	if pic.At(c.Min.X+c.Dx()/2, c.Max.Y-1) == back || pic.At(c.Max.X-1, c.Min.Y+c.Dy()/2) == back {
		t.Error("the corner does not reach the edges of its cell")
	}
}

func TestGIFCursorIsDrawnUnlessLeftOut(t *testing.T) {
	path := gifRecording(t, 10, 2, 0, "ab")
	fg, _ := parseHexRGB(gifDefaultFG)
	at := func(g *gif.GIF) color.Color {
		r := cellRect(t, 2, 0)
		return pictureAt(g, len(g.Image)-1).At(r.Min.X+1, r.Min.Y+1)
	}
	if got := at(exportGIF(t, path, GIFOptions{})); got != rgba(fg[0], fg[1], fg[2]) {
		t.Errorf("the cursor is %v", got)
	}
	bg, _ := parseHexRGB(gifDefaultBG)
	if got := at(exportGIF(t, path, GIFOptions{NoCursor: true})); got != rgba(bg[0], bg[1], bg[2]) {
		t.Errorf("without the cursor the cell is %v", got)
	}
}

func TestGIFTerminalSmallerThanTheEditorsMinimumKeepsItsWidth(t *testing.T) {
	// 10 columns: a line of 15 letters wraps at the 10th, as it did when it was recorded
	path := gifRecording(t, 10, 3, 0, "\x1b[?25lMMMMMMMMMMMMMMM")
	g := exportGIF(t, path, GIFOptions{})
	r := cellRect(t, 9, 2)
	if g.Config.Width != r.Max.X+r.Dx() {
		t.Errorf("the picture is %d wide, want %d", g.Config.Width, r.Max.X+r.Dx())
	}
	fg, _ := parseHexRGB(gifDefaultFG)
	pic := pictureAt(g, 0)
	second := cellRect(t, 2, 1)
	n := 0
	for y := second.Min.Y; y < second.Max.Y; y++ {
		for x := second.Min.X; x < second.Max.X; x++ {
			if pic.At(x, y) == rgba(fg[0], fg[1], fg[2]) {
				n++
			}
		}
	}
	if n == 0 {
		t.Error("the text did not wrap onto the second row")
	}
}

func TestGIFOptionsAreChecked(t *testing.T) {
	path := gifRecording(t, 10, 2, 0, "a")
	out := filepath.Join(filepath.Dir(path), "o.gif")
	for want, o := range map[string]GIFOptions{
		"frames a second":   {FPS: 99},
		"the speed":         {Speed: 5000},
		"the font size":     {FontSize: 2},
		"foreground colour": {Foreground: "red"},
		"background colour": {Background: "#12"},
		"font":              {FontPath: filepath.Join(filepath.Dir(path), "missing.ttf")},
	} {
		if _, err := ExportToGIF(path, out, o); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	empty := filepath.Join(filepath.Dir(path), "empty.jsonl")
	if err := os.WriteFile(empty, []byte(`{"type":"meta","width":10,"height":2}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportToGIF(empty, out, GIFOptions{}); err == nil || !strings.Contains(err.Error(), "no frames") {
		t.Errorf("an empty recording: %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a GIF was written although nothing could be made")
	}
}

func TestGIFOptionColoursAndCustomFont(t *testing.T) {
	font := "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
	if _, err := os.Stat(font); err != nil {
		t.Skip("no DejaVu Sans Mono here to try")
	}
	path := gifRecording(t, 10, 2, 0, "\x1b[?25lab")
	g := exportGIF(t, path, GIFOptions{FontPath: font, Background: "#102030", Foreground: "#ffffff"})
	r := cellRect(t, 9, 1)
	if got := pictureAt(g, 0).At(r.Min.X+1, r.Min.Y+1); got != rgba(0x10, 0x20, 0x30) {
		t.Errorf("the background is %v", got)
	}
}

func TestGIFPaletteKeepsFewColoursExactlyAndLimitsManyToWhatAGIFHolds(t *testing.T) {
	few := map[uint32]int{0x112233: 5, 0xffffff: 9, 0x000000: 1}
	pal := gifPaletteFor(few)
	if len(pal) != 3 || pal[0] != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("few colours: %v", pal)
	}
	many := map[uint32]int{}
	for i := 0; i < 5000; i++ {
		many[uint32(i*2654435761)&0xffffff] = 1 + i%7
	}
	many[0x0d1117] = 100000
	p1, p2 := gifPaletteFor(many), gifPaletteFor(many)
	if len(p1) > 255 || len(p1) < 200 {
		t.Errorf("%d colours", len(p1))
	}
	for i := range p1 {
		if p1[i] != p2[i] {
			t.Fatal("the palette is not the same each time")
		}
	}
	idx := newPaletteIndex(p1)
	got := p1[idx.of(0x0d1117)].(color.RGBA)
	if d := int(got.R) - 0x0d + int(got.G) - 0x11 + int(got.B) - 0x17; d > 8 || d < -8 {
		t.Errorf("the colour used most is shown as %v", got)
	}
}

func TestGIFBoxTable(t *testing.T) {
	for r, want := range map[rune][4]uint8{
		0x2500: {0, 1, 0, 1}, 0x2502: {1, 0, 1, 0}, 0x250c: {0, 1, 1, 0}, 0x253c: {1, 1, 1, 1},
		0x2550: {0, 3, 0, 3}, 0x256c: {3, 3, 3, 3}, 0x2503: {2, 0, 2, 0}, 0x2574: {0, 0, 0, 1}, 0x257e: {0, 1, 0, 2},
		0x256d: {0, 1, 1, 0}, // an arc is a corner
	} {
		if got := boxSegments[r-0x2500]; got != want {
			t.Errorf("%U is %v, want %v", r, got, want)
		}
	}
	if isSpecialGlyph('a') || isSpecialGlyph(0x2571) || !isSpecialGlyph(0x2588) || !isSpecialGlyph(0x28ff) {
		t.Error("isSpecialGlyph is wrong")
	}
}
