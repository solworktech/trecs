package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func (s *syncBuf) Reset()         { s.mu.Lock(); s.b.Reset(); s.mu.Unlock() }

// quadrants is a graphic of w x h pixels in four colours: red top left, green top right, blue bottom left, white bottom right.
func quadrants(w, h int) string {
	img := &libtrecs.SixelImage{W: w, H: h, Pix: make([]byte, w*h*4)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := [3]byte{255, 0, 0}
			switch {
			case x >= w/2 && y < h/2:
				c = [3]byte{0, 255, 0}
			case x < w/2 && y >= h/2:
				c = [3]byte{0, 0, 255}
			case x >= w/2:
				c = [3]byte{255, 255, 255}
			}
			o := (y*w + x) * 4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = c[0], c[1], c[2], 255
		}
	}
	return strings.TrimSuffix(strings.TrimPrefix(libtrecs.EncodeSixel(img), "\x1bP"), "\x1b\\")
}

func decodeDrawn(t *testing.T, written string) (img *libtrecs.SixelImage, row, col int) {
	t.Helper()
	m := regexp.MustCompile("\x1b\\[(\\d+);(\\d+)H\x1bP([^\x1b]*)\x1b\\\\").FindStringSubmatch(written)
	if m == nil {
		t.Fatalf("no picture in %q", written)
	}
	fmt.Sscan(m[1], &row)
	fmt.Sscan(m[2], &col)
	img, err := libtrecs.DecodeSixel(m[3])
	if err != nil {
		t.Fatal(err)
	}
	return img, row, col
}

func pixel(img *libtrecs.SixelImage, x, y int) [3]byte {
	o := (y*img.W + x) * 4
	return [3]byte{img.Pix[o], img.Pix[o+1], img.Pix[o+2]}
}

func TestWhatTheTerminalSaysOfItself(t *testing.T) {
	g := parseProbeReply("\x1b[6;18;9t\x1b[?62;4;22c")
	if !g.sixel || g.cellW != 9 || g.cellH != 18 {
		t.Errorf("%+v", g)
	}
	if g := parseProbeReply("\x1b[?62;22c"); g.sixel || g.cellW != 0 {
		t.Errorf("no sixel in the list: %+v", g)
	}
	if g := parseProbeReply("\x1b[?4c"); !g.sixel {
		t.Errorf("only 4: %+v", g)
	}
	if g := parseProbeReply("\x1b[?64;14;42c"); g.sixel { // 14 and 42 are not 4
		t.Errorf("%+v", g)
	}
	if g := parseProbeReply(""); g.sixel {
		t.Errorf("%+v", g)
	}
	if !endsDeviceAttributes("\x1b[6;18;9t\x1b[?62;4c") || endsDeviceAttributes("\x1b[6;18;9t") || endsDeviceAttributes("\x1b[?62;4") {
		t.Error("the end of the answer")
	}
}

func TestTheEnvironmentHasTheLastWord(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	probed := termGraphics{sixel: true, cellW: 9, cellH: 18}
	if g := termGraphicsFromEnv(probed, env("TRECS_SIXEL", "0")); g.sixel || g.cellW != 9 {
		t.Errorf("off: %+v", g)
	}
	if g := termGraphicsFromEnv(termGraphics{}, env("TRECS_SIXEL", "1", "TRECS_CELL_SIZE", "10x20")); !g.sixel || g.cellW != 10 || g.cellH != 20 {
		t.Errorf("on: %+v", g)
	}
	if g := termGraphicsFromEnv(probed, env("TRECS_CELL_SIZE", "nonsense")); g != probed {
		t.Errorf("bad size: %+v", g)
	}
	if g := termGraphicsFromEnv(probed, env()); g != probed {
		t.Errorf("nothing said: %+v", g)
	}
}

func TestAPictureIsCroppedAndScaledToTheCellsOfThisTerminal(t *testing.T) {
	p := newSixelPainter(termGraphics{sixel: true, cellW: 10, cellH: 20})
	// recorded in 8x16 cells: 16 x 32 px is 2 x 2 cells; here a cell is 10 x 20
	r := imageRect{dcs: quadrants(16, 32), pxW: 16, pxH: 32, cols: 2, rows: 2, recCellW: 8, recCellH: 16, cellW: 10, cellH: 20}
	img, err := libtrecs.DecodeSixel(strings.TrimSuffix(strings.TrimPrefix(p.sixelFor(r), "\x1bP"), "\x1b\\"))
	if err != nil {
		t.Fatal(err)
	}
	if img.W != 20 || img.H != 40 {
		t.Fatalf("whole: %dx%d, want 20x40", img.W, img.H)
	}
	if pixel(img, 2, 2) != [3]byte{255, 0, 0} || pixel(img, 18, 2) != [3]byte{0, 255, 0} || pixel(img, 2, 38) != [3]byte{0, 0, 255} {
		t.Errorf("colours: %v %v %v", pixel(img, 2, 2), pixel(img, 18, 2), pixel(img, 2, 38))
	}
	// only the bottom row of cells in view (the top one is scrolled off): the lower half, the two blues/whites
	r.srcRow, r.rows = 1, 1
	img, _ = libtrecs.DecodeSixel(strings.TrimSuffix(strings.TrimPrefix(p.sixelFor(r), "\x1bP"), "\x1b\\"))
	if img.W != 20 || img.H != 20 {
		t.Fatalf("lower row: %dx%d, want 20x20", img.W, img.H)
	}
	if pixel(img, 2, 2) != [3]byte{0, 0, 255} || pixel(img, 18, 2) != [3]byte{255, 255, 255} {
		t.Errorf("lower row colours: %v %v", pixel(img, 2, 2), pixel(img, 18, 2))
	}
	// only the right column
	r.srcRow, r.rows, r.srcCol, r.cols = 0, 2, 1, 1
	img, _ = libtrecs.DecodeSixel(strings.TrimSuffix(strings.TrimPrefix(p.sixelFor(r), "\x1bP"), "\x1b\\"))
	if img.W != 10 || img.H != 40 {
		t.Fatalf("right column: %dx%d, want 10x40", img.W, img.H)
	}
	if pixel(img, 2, 2) != [3]byte{0, 255, 0} || pixel(img, 2, 38) != [3]byte{255, 255, 255} {
		t.Errorf("right column colours")
	}
	// a rectangle the picture does not reach gives nothing
	r.srcCol = 9
	if s := p.sixelFor(r); s != "" {
		t.Errorf("outside the picture: %q", s)
	}
}

func TestPicturesAreWrittenOnlyWhenTheyChange(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)
	var out syncBuf
	p := newSixelPainter(termGraphics{sixel: true, cellW: 8, cellH: 16})
	p.out = &out
	r := imageRect{dcs: quadrants(16, 32), pxW: 16, pxH: 32, cols: 2, rows: 2, col: 5, row: 3, recCellW: 8, recCellH: 16, cellW: 8, cellH: 16}

	p.paint(sim, []imageRect{r})
	img, row, col := decodeDrawn(t, out.String())
	if row != 4 || col != 6 || img.W != 16 || img.H != 32 {
		t.Errorf("drawn at %d;%d, %dx%d", row, col, img.W, img.H)
	}
	if !strings.HasPrefix(out.String(), "\x1b7") || !strings.HasSuffix(out.String(), "\x1b8") {
		t.Error("the cursor is not saved and restored around the pictures")
	}
	out.Reset()
	p.paint(sim, []imageRect{r})
	if out.String() != "" {
		t.Error("the same picture, written again")
	}
	r2 := r
	r2.row = 4
	p.paint(sim, []imageRect{r2})
	if _, row, _ := decodeDrawn(t, out.String()); row != 5 {
		t.Errorf("moved: row %d", row)
	}
	out.Reset()
	sim.SetSize(110, 30) // the terminal was resized: it was drawn anew, and the pictures are gone with it
	p.paint(sim, []imageRect{r2})
	if out.String() == "" {
		t.Error("not drawn again after a resize")
	}
	out.Reset()
	p.paint(sim, nil)
	if out.String() != "" {
		t.Errorf("nothing to draw, yet %q", out.String())
	}
	p.paint(sim, nil)
	if len(p.last) != 0 || out.String() != "" {
		t.Error("nothing")
	}
}

// ---- in the editor -------------------------------------------------------------------------------------------------

var fixtureHello = regexp.MustCompile(`"data":"hello world\\r\\n"`)

// sixelFixture is the editor's six-command recording, 100 columns wide with 8 x 16 pixel cells, and a 16 x 32 pixel (2 x 2 cells)
// picture after the output of the second command.
func sixelFixture(t *testing.T) string {
	t.Helper()
	data, err := readFileBytes(cueFixture)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	s = strings.Replace(s, `{"type":"meta","width":172,"height":38}`, `{"type":"meta","width":100,"height":38,"cell_width":8,"cell_height":16}`, 1)
	pic := "\x1bP" + quadrants(16, 32) + "\x1b\\"
	enc := strings.NewReplacer("\x1b", `\u001b`, `\`, `\\`, `"`, `\"`).Replace(pic)
	if !fixtureHello.MatchString(s) {
		t.Fatal("the fixture has changed")
	}
	s = fixtureHello.ReplaceAllString(s, `"data":"hello world\r\n`+strings.ReplaceAll(enc, `\`, `\`)+`"`)
	path := filepath.Join(t.TempDir(), "sixel.jsonl")
	if err := writeFileBytes(path, []byte(s)); err != nil {
		t.Fatal(err)
	}
	return path
}

// newSixelHarness runs the editor over sixelFixture on a 120x45 terminal, with the sixel painter given.
func newSixelHarness(t *testing.T, gfx termGraphics, out *syncBuf) *harness {
	t.Helper()
	path := sixelFixture(t)
	cmds, err := libtrecs.ParseRecording(path)
	if err != nil || len(cmds) != 6 {
		t.Fatalf("%d commands, %v", len(cmds), err)
	}
	h := &harness{t: t, sim: tcell.NewSimulationScreen("UTF-8"), done: make(chan error, 1)}
	h.em = NewEditorMode(libtrecs.NewTerminalPlayer(), cmds, path)
	h.em.sixel = newSixelPainter(gfx)
	h.em.sixel.out = out
	h.em.app.SetScreen(h.sim)
	h.sim.SetSize(120, 45)
	go func() { h.done <- h.em.Run() }()
	t.Cleanup(func() {
		h.em.app.Stop()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("the editor did not stop")
		}
	})
	h.waitFor("the editor to be drawn", func() bool { return strings.Contains(h.screen(), "^L Browse") }, 5*time.Second)
	return h
}

// rowOf is the row (from 0) of the screen that has the text alone on it.
func (h *harness) rowOf(text string) int {
	for i, l := range strings.Split(h.screen(), "\n") {
		if strings.TrimSpace(l) == text {
			return i
		}
	}
	return -1
}

func TestAPictureInTheRecordingIsDrawnWhereItsCellsAre(t *testing.T) {
	var out syncBuf
	h := newSixelHarness(t, termGraphics{sixel: true, cellW: 8, cellH: 16}, &out)
	h.waitFor("the picture", func() bool { return strings.Contains(out.String(), "\x1bP") }, 8*time.Second)
	h.pausePlayback()
	row := h.rowOf("hello world")
	if row < 0 {
		t.Fatalf("no hello world:\n%s", h.screen())
	}
	img, drawnRow, drawnCol := decodeDrawn(t, out.String())
	if drawnRow != row+2 || drawnCol != 1 { // the line after the text, 1-based
		t.Errorf("drawn at %d;%d, the text is on row %d (0-based)", drawnRow, drawnCol, row)
	}
	if img.W != 16 || img.H != 32 || pixel(img, 2, 2) != [3]byte{255, 0, 0} {
		t.Errorf("the picture: %dx%d", img.W, img.H)
	}
	if strings.Contains(h.screen(), "▒") {
		t.Error("shaded cells where a picture is drawn")
	}
}

func TestInATerminalWithoutSixelThePictureIsAShadedBlock(t *testing.T) {
	var out syncBuf
	h := newSixelHarness(t, termGraphics{}, &out)
	h.waitFor("the shaded cells", func() bool { return strings.Contains(h.screen(), "▒▒") }, 8*time.Second)
	h.pausePlayback()
	scr := strings.Split(h.screen(), "\n")
	row := h.rowOf("▒▒")
	if row < 0 || !strings.Contains(scr[row+1], "▒▒") || strings.Contains(scr[row+2], "▒") {
		t.Errorf("the block is not 2 cells by 2:\n%s", h.screen())
	}
	if out.String() != "" {
		t.Errorf("written to a terminal that cannot show it: %q", out.String())
	}
}

func TestThePicturesAreOffWhileAnotherPageOrACardIsOver(t *testing.T) {
	var out syncBuf
	h := newSixelHarness(t, termGraphics{sixel: true, cellW: 8, cellH: 16}, &out)
	h.waitFor("the picture", func() bool { return strings.Contains(out.String(), "\x1bP") }, 8*time.Second)
	h.pausePlayback()
	var shown bool
	h.onUI(func() { shown = len(h.em.pictureRects(h.sim)) > 0 })
	if !shown {
		t.Fatal("no picture in view")
	}
	h.key(tcell.KeyCtrlL) // the command list
	h.waitFor("the command list", func() bool { return h.front() == "commandlist" }, 3*time.Second)
	h.onUI(func() { shown = len(h.em.pictureRects(h.sim)) > 0 })
	if shown {
		t.Error("a picture is drawn over the command list")
	}
	h.onUI(func() { h.em.paintPictures(h.sim) })
	if len(h.em.sixel.last) != 0 {
		t.Error("the painter still holds the pictures")
	}
}

func TestThePicturesAreNotDrawnWhenTheViewIsNarrowerThanTheRecordedScreen(t *testing.T) {
	var out syncBuf
	path := sixelFixture(t)
	cmds, _ := libtrecs.ParseRecording(path)
	h := &harness{t: t, sim: tcell.NewSimulationScreen("UTF-8"), done: make(chan error, 1)}
	h.em = NewEditorMode(libtrecs.NewTerminalPlayer(), cmds, path)
	h.em.sixel = newSixelPainter(termGraphics{sixel: true, cellW: 8, cellH: 16})
	h.em.sixel.out = &out
	h.em.app.SetScreen(h.sim)
	h.sim.SetSize(80, 45) // the recording is 100 wide: the lines wrap
	go func() { h.done <- h.em.Run() }()
	t.Cleanup(func() { h.em.app.Stop(); <-h.done })
	h.waitFor("the shaded cells", func() bool { return strings.Contains(h.screen(), "▒") }, 8*time.Second)
	if out.String() != "" {
		t.Errorf("drawn over wrapped lines: %q", out.String())
	}
}

// A picture that is partly out of the view is drawn in the part that is in it.
func TestAPartlyScrolledPictureIsCropped(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)
	em := NewEditorMode(libtrecs.NewTerminalPlayer(), nil, "x.jsonl")
	em.pages = tview.NewPages()
	em.pages.AddPage("playback", em.createPlaybackView(), true, true)
	em.sixel = newSixelPainter(termGraphics{sixel: true, cellW: 8, cellH: 16})
	em.displayCols, em.displayRows = 80, 30
	vt := libtrecs.NewVTScreenSized(80, 30)
	vt.SetCellSize(8, 16)
	vt.Feed("line 0\r\nline 1\r\n" + "\x1bP" + quadrants(16, 64) + "\x1b\\" + "after\r\n") // the picture is rows 2-5, 2 cells wide
	em.display = vt
	em.playbackView.SetRect(10, 3, 80, 4)
	em.playbackView.SetText(em.renderDisplay())

	check := func(scrollTo int, wantRow, wantSrcRow, wantRows int) {
		t.Helper()
		em.playbackView.ScrollTo(scrollTo, 0)
		em.playbackView.Draw(sim)
		rects := em.pictureRects(sim)
		if len(rects) != 1 {
			t.Fatalf("scrolled to %d: %d rectangles", scrollTo, len(rects))
		}
		if r := rects[0]; r.row != wantRow || r.srcRow != wantSrcRow || r.rows != wantRows || r.col != 10 || r.cols != 2 || r.srcCol != 0 {
			t.Errorf("scrolled to %d: %+v, want row %d, from row %d of the picture, %d rows", scrollTo, r, wantRow, wantSrcRow, wantRows)
		}
	}
	check(0, 3+2, 0, 2) // lines 0-3 in view: the picture's first two rows, two rows down the view
	check(1, 3+1, 0, 3) // lines 1-4: its first three
	check(4, 3+0, 2, 2) // lines 4-7: its last two, at the top of the view
}
