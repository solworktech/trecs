package lib

import (
	"sort"
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
// A placeholder is  imgBase + start + row*cols + col:  each picture on the screen owns a run of imgPool code points, as many as it has
// cells (start is where its run begins), and row and col are the cell within the picture's box. A screen can hold as many pictures as the
// pool has room for - one picture may take a quarter of it (imgMaxCells cells, a box of 175 x 93, say); a bigger one is shown at the size
// that fits, the cursor moving as far as it can. Runs are given out afresh as pictures come and go, and the oldest picture is
// replaced when there is no room.
const (
	imgBase = 0x100000 // plane 16, private use B: not the Nerd Font icons' (plane 15)
	imgPool = 0xFFFE   // U+100000 to U+10FFFD: the last two code points of the plane are not characters

	// the most cells a picture's box has
	imgMaxCells = 16384

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
	start      int    // the first of its code points
	seq        int    // when it was put on the screen: the lower, the older
}

func (im *vtImage) placeholder(row, col int) rune { return imgBase + rune(im.start+row*im.cols+col) }

func isPlaceholder(r rune) bool { return r >= imgBase && r < imgBase+imgPool }

// imageOf says which picture a placeholder is of, and which cell of its box; nil if that picture is gone.
func (s *VTScreen) imageOf(r rune) (im *vtImage, row, col int) {
	v := int(r - imgBase)
	for _, im := range s.images {
		if v >= im.start && v < im.start+im.cols*im.rows {
			v -= im.start
			return im, v / im.cols, v % im.cols
		}
	}
	return nil, 0, 0
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

// placePicture puts a graphic, sixel or inline image, where the cursor is: its box of placeholder cells and, if moves, the
// cursor below it. A picture that does not move the cursor (doNotMoveCursor=1) is clipped at the bottom of the screen
// rather than scrolling it.
func (s *VTScreen) placePicture(src string, moves bool) {
	cw, ch := s.cellSize()
	s.term.Lock()
	termCols, termRows := s.term.Size()
	s.term.Unlock()
	w, h, err := PictureSize(src, cw, ch, termCols, termRows)
	if err != nil || w == 0 || h == 0 {
		return
	}
	cols, rows := (w+cw-1)/cw, (h+ch-1)/ch

	s.term.Lock()
	cur := s.term.Cursor()
	// which pictures are still on the screen
	inUse := map[*vtImage]bool{}
	for y := 0; y < termRows; y++ {
		for x := 0; x < termCols; x++ {
			if r := s.term.Cell(x, y).Char; isPlaceholder(r) {
				if im, _, _ := s.imageOf(r); im != nil {
					inUse[im] = true
				}
			}
		}
	}
	s.term.Unlock()
	kept := s.images[:0]
	for _, im := range s.images {
		if inUse[im] {
			kept = append(kept, im)
		}
	}
	s.images = kept

	cols = min(cols, max(termCols-cur.X, 1))
	rows = max(min(rows, imgMaxCells/cols), 1)
	if !moves {
		rows = max(min(rows, termRows-cur.Y), 1)
	}

	// the same picture again shares the run of its twin; a new one takes room (or, when there is none, the oldest's)
	var im *vtImage
	for _, o := range s.images {
		if o.dcs == src && o.cols == cols && o.rows == rows && o.w == w && o.h == h {
			im = o
			break
		}
	}
	if im == nil {
		for {
			if start := s.freeRun(cols * rows); start >= 0 {
				s.nextSeq++
				im = &vtImage{dcs: src, w: w, h: h, cols: cols, rows: rows, start: start, seq: s.nextSeq}
				s.images = append(s.images, im)
				break
			}
			oldest := 0
			for i, o := range s.images {
				if o.seq < s.images[oldest].seq {
					oldest = i
				}
			}
			s.images = append(s.images[:oldest], s.images[oldest+1:]...)
		}
	}

	var b strings.Builder
	if moves {
		for r := 0; r < rows; r++ {
			if r > 0 {
				b.WriteString("\n")
				b.WriteString("\x1b[" + strconv.Itoa(cur.X+1) + "G")
			}
			for c := 0; c < cols; c++ {
				b.WriteRune(im.placeholder(r, c))
			}
		}
		// the cursor ends below the picture, in the column it began at
		b.WriteString("\n\x1b[" + strconv.Itoa(cur.X+1) + "G")
	} else {
		for r := 0; r < rows; r++ {
			b.WriteString("\x1b[" + strconv.Itoa(cur.Y+r+1) + ";" + strconv.Itoa(cur.X+1) + "H")
			for c := 0; c < cols; c++ {
				b.WriteRune(im.placeholder(r, c))
			}
		}
		b.WriteString("\x1b[" + strconv.Itoa(cur.Y+1) + ";" + strconv.Itoa(cur.X+1) + "H")
	}
	_, _ = s.term.Write([]byte(b.String()))
}

// freeRun finds n code points of the pool that no picture on the screen has; -1 if there are none together.
func (s *VTScreen) freeRun(n int) int {
	runs := make([]*vtImage, len(s.images))
	copy(runs, s.images)
	sort.Slice(runs, func(i, j int) bool { return runs[i].start < runs[j].start })
	at := 0
	for _, o := range runs {
		if o.start-at >= n {
			return at
		}
		at = o.start + o.cols*o.rows
	}
	if imgPool-at >= n {
		return at
	}
	return -1
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

	img *vtImage
}

// ImagePlacements says which parts of which pictures are on the screen: for each picture the rectangles of cells that
// still show it (a picture intact is one).
func (s *VTScreen) ImagePlacements() []ImagePlacement {
	s.term.Lock()
	defer s.term.Unlock()
	cols, rows := s.term.Size()
	// a run: consecutive cells of one row that are consecutive cells of one picture's box
	type run struct {
		img                        *vtImage
		row0, col0, ox, oy, x0, x1 int
	}
	var out []ImagePlacement
	var open []int // the rectangles in out whose last row is the row above
	for y := 0; y < rows; y++ {
		var runs []run
		for x := 0; x < cols; x++ {
			r := s.term.Cell(x, y).Char
			if !isPlaceholder(r) {
				continue
			}
			img, prow, pcol := s.imageOf(r)
			if img == nil {
				continue
			}
			ox, oy := x-pcol, y-prow // where the box begins: cells of the same picture elsewhere have another
			if n := len(runs); n > 0 {
				l := &runs[n-1]
				if l.img == img && l.ox == ox && l.oy == oy && l.x1 == x {
					l.x1 = x + 1
					continue
				}
			}
			runs = append(runs, run{img: img, row0: prow, col0: pcol, ox: ox, oy: oy, x0: x, x1: x + 1})
		}
		var next []int
		for _, rn := range runs {
			found := -1
			for _, i := range open {
				p := &out[i]
				if p.img == rn.img && p.Col == rn.x0 && p.Cols == rn.x1-rn.x0 && p.Col0 == rn.col0 && p.Row0+p.Rows == rn.row0 {
					found = i
					break
				}
			}
			if found >= 0 {
				out[found].Rows++
				next = append(next, found)
				continue
			}
			im := rn.img
			out = append(out, ImagePlacement{
				DCS: im.dcs, PxW: im.w, PxH: im.h, BoxCols: im.cols, BoxRows: im.rows,
				Col0: rn.col0, Row0: rn.row0, Col: rn.x0, Row: y, Cols: rn.x1 - rn.x0, Rows: 1, img: rn.img,
			})
			next = append(next, len(out)-1)
		}
		open = next
	}
	return out
}
