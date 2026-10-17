package lib

import (
	"strconv"
	"strings"
)

// Sixel images on the emulated screen.
//
// vt10x knows nothing of pictures, and a picture is not a thing a grid of characters can hold. What a real terminal
// does with one - it paints the pixels over a rectangle of cells, and moves the cursor past it - is done here with
// the grid itself: the rectangle is filled with placeholder characters, private-use code points that say
// WHICH image and WHICH cell of it (the idea behind kitty's unicode placeholders), and the cursor is moved by
// feeding the emulator the lines it would have moved by. The grid then does what it always does and the pictures
// follow: they scroll off with the text, a cleared line clears them, text written over one replaces it.
// Placements() reads the grid back and says what is where.
//
// A placeholder is  imgBase + id*4096 + row*64 + col:  id is one of imgIDs (a screen holds that many different
// pictures at once; more, and the oldest is replaced), row and col the cell within the picture's box (a box is at
// most 63 x 63 cells - a bigger picture is shown at the size that fits, the cursor moving as far as it can).
const (
	imgBase   rune = 0x100000 // plane 16, private use B: not the Nerd Font icons' (plane 15)
	imgIDs         = 15
	imgMaxBox      = 63

	// Until the recording says what the terminal's cells were (RecordingMeta.CellWidth), these are taken: a common size
	// of a terminal's cell.
	DefaultCellWidth, DefaultCellHeight = 8, 16

	// The longest sixel graphic kept, as the recorder does (see recorder/dcsfilter.go).
	MaxSixelBytes = 2 << 20
)

// vtImage is a picture that has been shown on the screen.
type vtImage struct {
	dcs        string // the graphic: what is between ESC P and the terminator
	w, h       int    // in pixels
	cols, rows int    // the cells of its box
}

func placeholder(id, row, col int) rune { return imgBase + rune(id*4096+row*64+col) }

func isPlaceholder(r rune) bool { return r >= imgBase && r < imgBase+rune(imgIDs*4096) }

func splitPlaceholder(r rune) (id, row, col int) {
	v := int(r - imgBase)
	return v / 4096, (v / 64) % 64, v % 64
}

// SetCellSize tells the screen how large a character cell was, in pixels, where the recording was made. It decides how
// many cells a picture covered, and so how far it moved the cursor. 0 or less for either leaves the default.
func (s *VTScreen) SetCellSize(w, h int) {
	if w > 0 && h > 0 {
		s.cellW, s.cellH = w, h
	}
}

// SetImageFill sets what the cells of a picture are drawn as in Render() - by default blanks, for a program that paints
// the picture itself over them (see ImagePlacements); a visible mark for one that cannot.
func (s *VTScreen) SetImageFill(r rune) { s.imageFill = r }

// CellSize is the size of a character cell, in pixels, that the pictures were sized by.
func (s *VTScreen) CellSize() (int, int) { return s.cellSize() }

func (s *VTScreen) cellSize() (int, int) {
	if s.cellW > 0 && s.cellH > 0 {
		return s.cellW, s.cellH
	}
	return DefaultCellWidth, DefaultCellHeight
}

// placeSixel puts a graphic where the cursor is: its box of placeholder cells, and the cursor below it.
func (s *VTScreen) placeSixel(dcs string) {
	w, h, err := SixelSize(dcs)
	if err != nil || w == 0 || h == 0 {
		return
	}
	cw, ch := s.cellSize()
	cols, rows := (w+cw-1)/cw, (h+ch-1)/ch

	s.term.Lock()
	cur := s.term.Cursor()
	termCols, termRows := s.term.Size()
	used := make([]bool, imgIDs)
	for y := 0; y < termRows; y++ {
		for x := 0; x < termCols; x++ {
			if r := s.term.Cell(x, y).Char; isPlaceholder(r) {
				id, _, _ := splitPlaceholder(r)
				used[id] = true
			}
		}
	}
	s.term.Unlock()

	cols = min(cols, imgMaxBox, max(termCols-cur.X, 1))
	rows = min(rows, imgMaxBox)

	// the same picture again shares the id of its twin; a new one takes a free id (or, when there is none, the oldest's)
	id := -1
	for i, im := range s.images {
		if im != nil && used[i] && im.dcs == dcs {
			id = i
			break
		}
	}
	if id < 0 {
		for i := 0; i < imgIDs; i++ {
			if !used[i] {
				id = i
				break
			}
		}
	}
	if id < 0 {
		id = s.nextID % imgIDs
		s.nextID++
	}
	s.images[id] = &vtImage{dcs: dcs, w: w, h: h, cols: cols, rows: rows}

	var b strings.Builder
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteString("\n")
			b.WriteString("\x1b[" + strconv.Itoa(cur.X+1) + "G")
		}
		for c := 0; c < cols; c++ {
			b.WriteRune(placeholder(id, r, c))
		}
	}
	// the cursor ends below the picture, in the column it began at
	b.WriteString("\n\x1b[" + strconv.Itoa(cur.X+1) + "G")
	_, _ = s.term.Write([]byte(b.String()))
}

// ImagePlacement is a rectangle of cells that shows (a part of) a picture.
type ImagePlacement struct {
	// DCS is the graphic (what is between ESC P and the terminator), PxW x PxH pixels.
	DCS      string
	PxW, PxH int
	// BoxCols x BoxRows is the picture's whole box in cells, and Col0/Row0 the cell of that box the rectangle starts
	// at: a picture partly scrolled off, or partly written over, is shown in parts.
	BoxCols, BoxRows int
	Col0, Row0       int
	// Col and Row are where on the screen (cells, from 0) the rectangle is, Cols x Rows large.
	Col, Row   int
	Cols, Rows int

	imgID int
}

// ImagePlacements says which parts of which pictures are on the screen: for each picture the rectangles of cells that
// still show it (a picture intact is one).
func (s *VTScreen) ImagePlacements() []ImagePlacement {
	s.term.Lock()
	defer s.term.Unlock()
	cols, rows := s.term.Size()
	// a run: consecutive cells of one row that are consecutive cells of one picture's box
	type run struct{ id, row0, col0, ox, oy, x0, x1 int }
	var out []ImagePlacement
	var open []int // the rectangles in out whose last row is the row above
	for y := 0; y < rows; y++ {
		var runs []run
		for x := 0; x < cols; x++ {
			r := s.term.Cell(x, y).Char
			if !isPlaceholder(r) {
				continue
			}
			id, prow, pcol := splitPlaceholder(r)
			if s.images[id] == nil {
				continue
			}
			ox, oy := x-pcol, y-prow // where the box begins: cells of the same picture elsewhere have another
			if n := len(runs); n > 0 {
				l := &runs[n-1]
				if l.id == id && l.ox == ox && l.oy == oy && l.x1 == x {
					l.x1 = x + 1
					continue
				}
			}
			runs = append(runs, run{id: id, row0: prow, col0: pcol, ox: ox, oy: oy, x0: x, x1: x + 1})
		}
		var next []int
		for _, rn := range runs {
			found := -1
			for _, i := range open {
				p := &out[i]
				if p.imgID == rn.id && p.Col == rn.x0 && p.Cols == rn.x1-rn.x0 && p.Col0 == rn.col0 && p.Row0+p.Rows == rn.row0 {
					found = i
					break
				}
			}
			if found >= 0 {
				out[found].Rows++
				next = append(next, found)
				continue
			}
			im := s.images[rn.id]
			out = append(out, ImagePlacement{
				DCS: im.dcs, PxW: im.w, PxH: im.h, BoxCols: im.cols, BoxRows: im.rows,
				Col0: rn.col0, Row0: rn.row0, Col: rn.x0, Row: y, Cols: rn.x1 - rn.x0, Rows: 1, imgID: rn.id,
			})
			next = append(next, len(out)-1)
		}
		open = next
	}
	return out
}
