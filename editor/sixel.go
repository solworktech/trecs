package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	libtrecs "trecs/lib"
)

// Sixel pictures in the playback view.
//
// The view is a tview text view, and tcell has no notion of a picture: it draws cells. A picture in the recording
// is a rectangle of placeholder cells in the emulated screen (see lib/vt_images.go), shown as blanks here; once tview
// has drawn the view (SetAfterDrawFunc), the pictures are written straight to the terminal over those blanks, each
// cropped to the cells that are in view, scaled from the cell size of the recording's terminal to this one's, and
// encoded again as sixel. A terminal keeps pixels under the cells they were drawn in and has no way to be told to
// forget them, so a picture is taken off the screen by drawing the cells again (tcell's Sync); that is done
// when the pictures in view change, not on every frame.
//
// A terminal that does not draw sixels shows the cells as a shaded block instead, so the place and the size of the
// picture are still there. Whether this one does is asked of it before the editor starts (probeTerminal), and can be
// said by the environment: TRECS_SIXEL=0 / 1, and TRECS_CELL_SIZE=WxH (pixels) if the terminal cannot tell.

// termGraphics is what the terminal can do.
type termGraphics struct {
	sixel        bool
	cellW, cellH int // a character cell in pixels, 0 if not known
}

var (
	deviceAttributesReply = regexp.MustCompile("\x1b\\[\\?([0-9;]*)c")
	cellSizeReply         = regexp.MustCompile("\x1b\\[6;([0-9]+);([0-9]+)t")
)

// parseProbeReply reads the answers to probeTerminal's two questions.
func parseProbeReply(s string) (t termGraphics) {
	if m := deviceAttributesReply.FindStringSubmatch(s); m != nil {
		for _, f := range strings.Split(m[1], ";") {
			if f == "4" {
				t.sixel = true
			}
		}
	}
	if m := cellSizeReply.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		w, _ := strconv.Atoi(m[2])
		if w > 0 && h > 0 {
			t.cellW, t.cellH = w, h
		}
	}
	return t
}

// termGraphicsFromEnv lets the environment have the last word on what the terminal told.
func termGraphicsFromEnv(t termGraphics, getenv func(string) string) termGraphics {
	switch strings.ToLower(getenv("TRECS_SIXEL")) {
	case "0", "off", "no", "false":
		t.sixel = false
	case "1", "on", "yes", "true":
		t.sixel = true
	}
	if w, h, ok := strings.Cut(strings.ToLower(getenv("TRECS_CELL_SIZE")), "x"); ok {
		cw, e1 := strconv.Atoi(strings.TrimSpace(w))
		ch, e2 := strconv.Atoi(strings.TrimSpace(h))
		if e1 == nil && e2 == nil && cw > 0 && ch > 0 {
			t.cellW, t.cellH = cw, ch
		}
	}
	return t
}

// detectTermGraphics asks the terminal (unless told not to), and then the environment.
func detectTermGraphics() termGraphics {
	var t termGraphics
	if v := strings.ToLower(os.Getenv("TRECS_SIXEL")); v != "0" && v != "off" && v != "no" && v != "false" {
		t = probeTerminal(400 * time.Millisecond)
	}
	return termGraphicsFromEnv(t, os.Getenv)
}

// imageRect is a rectangle of the screen that a picture is drawn over, and all that decides how it looks.
type imageRect struct {
	dcs                string // the picture as recorded
	pxW, pxH           int
	srcCol, srcRow     int // the cell of the picture's box this rectangle starts at
	col, row           int // where on the screen (cells from 0), cols x rows large
	cols, rows         int
	recCellW, recCellH int // a cell where it was recorded
	cellW, cellH       int // and here
}

// sixelPainter draws the pictures and remembers what it drew.
type sixelPainter struct {
	gfx termGraphics
	out io.Writer // where the pictures are written; the terminal of the screen if nil

	last         []imageRect
	lastW, lastH int
	decoded      map[string]*libtrecs.SixelImage
	encoded      map[imageRect]string
	encodedBytes int
}

func newSixelPainter(g termGraphics) *sixelPainter {
	return &sixelPainter{gfx: g, decoded: map[string]*libtrecs.SixelImage{}, encoded: map[imageRect]string{}}
}

// canDraw: pictures are drawn at all (the terminal draws them and its cell size is known).
func (p *sixelPainter) canDraw() bool { return p != nil && p.gfx.sixel }

// cellSize is a cell of this terminal in pixels, from the terminal itself if it says, else from the probe.
func (p *sixelPainter) cellSize(screen tcell.Screen) (int, int) {
	if tty, ok := screen.Tty(); ok {
		if ws, err := tty.WindowSize(); err == nil {
			if w, h := ws.CellDimensions(); w > 0 && h > 0 {
				return w, h
			}
		}
	}
	return p.gfx.cellW, p.gfx.cellH
}

// sixelFor is the picture in the rectangle, as the sixel to write.
func (p *sixelPainter) sixelFor(r imageRect) string {
	if s, ok := p.encoded[r]; ok {
		return s
	}
	img, ok := p.decoded[r.dcs]
	if !ok {
		var err error
		if img, err = libtrecs.DecodeSixel(r.dcs); err != nil {
			img = &libtrecs.SixelImage{}
		}
		if len(p.decoded) > 16 {
			p.decoded = map[string]*libtrecs.SixelImage{}
		}
		p.decoded[r.dcs] = img
	}
	// the part of the picture in these cells, in the pixels of the recording...
	part := img.Crop(r.srcCol*r.recCellW, r.srcRow*r.recCellH, r.cols*r.recCellW, r.rows*r.recCellH)
	// ...made as large as that part of the box is here
	w := (part.W*r.cellW + r.recCellW/2) / r.recCellW
	h := (part.H*r.cellH + r.recCellH/2) / r.recCellH
	s := ""
	if part.W > 0 && part.H > 0 {
		s = libtrecs.EncodeSixel(part.Resize(max(w, 1), max(h, 1)))
	}
	if p.encodedBytes+len(s) > 32<<20 {
		p.encoded, p.encodedBytes = map[imageRect]string{}, 0
	}
	p.encoded[r] = s
	p.encodedBytes += len(s)
	return s
}

// paint puts the pictures on the screen, called when tview has drawn the view. Nothing is written unless the pictures, or
// the screen they are on, are not what was drawn last.
func (p *sixelPainter) paint(screen tcell.Screen, rects []imageRect) {
	w, h := screen.Size()
	if w == p.lastW && h == p.lastH && sameRects(rects, p.last) {
		return
	}
	out := p.out
	if out == nil {
		tty, ok := screen.Tty()
		if !ok {
			return
		}
		out = tty
	}
	hadPictures := len(p.last) > 0
	p.last, p.lastW, p.lastH = append([]imageRect(nil), rects...), w, h
	if hadPictures {
		screen.Sync() // the cells are drawn again: what was over them is gone
	} else if len(rects) > 0 {
		screen.Show() // the cells first: a cell drawn after its picture would take the picture's place
	}
	if len(rects) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\x1b7") // the cursor is tcell's: put it back where it was
	for _, r := range rects {
		if s := p.sixelFor(r); s != "" {
			fmt.Fprintf(&b, "\x1b[%d;%dH%s", r.row+1, r.col+1, s)
		}
	}
	b.WriteString("\x1b8")
	_, _ = io.WriteString(out, b.String())
}

func sameRects(a, b []imageRect) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- the editor's side

// initSixel decides, once, whether pictures are drawn. Tests set the painter themselves.
func (em *EditorMode) initSixel() {
	if em.sixel == nil {
		em.sixel = newSixelPainter(detectTermGraphics())
	}
}

// pictureFill is what the cells of a picture are drawn as in the text: blanks when the picture will be painted over
// them, a shaded block when it cannot be (no sixel in the terminal, or the view narrower than the screen that was
// recorded, which would wrap the lines and put the cells somewhere else).
func (em *EditorMode) pictureFill() rune {
	if em.sixel.canDraw() {
		if _, _, w, _ := em.playbackView.GetInnerRect(); w == 0 || w >= em.displayCols {
			return 0
		}
	}
	return '▒'
}

// renderDisplay is the screen as text for the view.
func (em *EditorMode) renderDisplay() string {
	if vt, ok := em.display.(*libtrecs.VTScreen); ok {
		vt.SetImageFill(em.pictureFill())
	}
	return em.display.Render()
}

// picturesVisible: the playback page is the one in front, with nothing laid over it.
func (em *EditorMode) picturesVisible() bool {
	if em.pages == nil || em.playbackView == nil {
		return false
	}
	if name, _ := em.pages.GetFrontPage(); name != "playback" {
		return false
	}
	return em.cueOverlay.text == "" && em.timedOverlay.text == ""
}

// pictureRects works out where on the screen the pictures of the emulated screen are drawn.
func (em *EditorMode) pictureRects(screen tcell.Screen) []imageRect {
	vt, ok := em.display.(*libtrecs.VTScreen)
	if !ok || !em.sixel.canDraw() || !em.picturesVisible() {
		return nil
	}
	cw, ch := em.sixel.cellSize(screen)
	rcw, rch := vt.CellSize()
	if cw <= 0 || ch <= 0 {
		return nil
	}
	x, y, w, h := em.playbackView.GetInnerRect()
	if w < em.displayCols {
		return nil
	}
	rowOff, colOff := em.playbackView.GetScrollOffset()
	var rects []imageRect
	for _, p := range vt.ImagePlacements() {
		top, bottom := max(p.Row, rowOff), min(p.Row+p.Rows, rowOff+h)
		left, right := max(p.Col, colOff), min(p.Col+p.Cols, colOff+w)
		if bottom <= top || right <= left {
			continue
		}
		rects = append(rects, imageRect{
			dcs: p.DCS, pxW: p.PxW, pxH: p.PxH,
			srcCol: p.Col0 + (left - p.Col), srcRow: p.Row0 + (top - p.Row),
			col: x + left - colOff, row: y + top - rowOff, cols: right - left, rows: bottom - top,
			recCellW: rcw, recCellH: rch, cellW: cw, cellH: ch,
		})
	}
	return rects
}

// paintPictures is tview's after-draw handler.
func (em *EditorMode) paintPictures(screen tcell.Screen) {
	if !em.sixel.canDraw() {
		return
	}
	em.sixel.paint(screen, em.pictureRects(screen))
}
