package lib

import "image"

// Characters the GIF export draws itself instead of taking from the font: block elements, braille patterns and box-drawing
// lines. A font draws them for its own cell, and any font whose cell is not exactly the size of the one the GIF uses (which is
// nearly every font) leaves hairline gaps between neighbouring cells - and a bar of half blocks, or a box, is made of neighbours.
// Drawn here, they fill their cell exactly and join.

// boxSegments says what a box-drawing character (U+2500..U+257F) is made of: for up, right, down and left, 0 for nothing,
// 1 a light line, 2 a heavy one, 3 a double one. Dashed lines are solid and arcs are square corners. The diagonals are left out.
// (Generated from the characters' Unicode names.)
var boxSegments = [128][4]uint8{ // up, right, down, left: 0 none, 1 light, 2 heavy, 3 double (generated from the Unicode names)
	{0, 1, 0, 1}, {0, 2, 0, 2}, {1, 0, 1, 0}, {2, 0, 2, 0},
	{0, 1, 0, 1}, {0, 2, 0, 2}, {1, 0, 1, 0}, {2, 0, 2, 0},
	{0, 1, 0, 1}, {0, 2, 0, 2}, {1, 0, 1, 0}, {2, 0, 2, 0},
	{0, 1, 1, 0}, {0, 2, 1, 0}, {0, 1, 2, 0}, {0, 2, 2, 0},
	{0, 0, 1, 1}, {0, 0, 1, 2}, {0, 0, 2, 1}, {0, 0, 2, 2},
	{1, 1, 0, 0}, {1, 2, 0, 0}, {2, 1, 0, 0}, {2, 2, 0, 0},
	{1, 0, 0, 1}, {1, 0, 0, 2}, {2, 0, 0, 1}, {2, 0, 0, 2},
	{1, 1, 1, 0}, {1, 2, 1, 0}, {2, 1, 1, 0}, {1, 1, 2, 0},
	{2, 1, 2, 0}, {2, 2, 1, 0}, {1, 2, 2, 0}, {2, 2, 2, 0},
	{1, 0, 1, 1}, {1, 0, 1, 2}, {2, 0, 1, 1}, {1, 0, 2, 1},
	{2, 0, 2, 1}, {2, 0, 1, 2}, {1, 0, 2, 2}, {2, 0, 2, 2},
	{0, 1, 1, 1}, {0, 1, 1, 2}, {0, 2, 1, 1}, {0, 2, 1, 2},
	{0, 1, 2, 1}, {0, 1, 2, 2}, {0, 2, 2, 1}, {0, 2, 2, 2},
	{1, 1, 0, 1}, {1, 1, 0, 2}, {1, 2, 0, 1}, {1, 2, 0, 2},
	{2, 1, 0, 1}, {2, 1, 0, 2}, {2, 2, 0, 1}, {2, 2, 0, 2},
	{1, 1, 1, 1}, {1, 1, 1, 2}, {1, 2, 1, 1}, {1, 2, 1, 2},
	{2, 1, 1, 1}, {1, 1, 2, 1}, {2, 1, 2, 1}, {2, 1, 1, 2},
	{2, 2, 1, 1}, {1, 1, 2, 2}, {1, 2, 2, 1}, {2, 2, 1, 2},
	{1, 2, 2, 2}, {2, 1, 2, 2}, {2, 2, 2, 1}, {2, 2, 2, 2},
	{0, 1, 0, 1}, {0, 2, 0, 2}, {1, 0, 1, 0}, {2, 0, 2, 0},
	{0, 3, 0, 3}, {3, 0, 3, 0}, {0, 3, 1, 0}, {0, 1, 3, 0},
	{0, 3, 3, 0}, {0, 0, 1, 3}, {0, 0, 3, 1}, {0, 0, 3, 3},
	{1, 3, 0, 0}, {3, 1, 0, 0}, {3, 3, 0, 0}, {1, 0, 0, 3},
	{3, 0, 0, 1}, {3, 0, 0, 3}, {1, 3, 1, 0}, {3, 1, 3, 0},
	{3, 3, 3, 0}, {1, 0, 1, 3}, {3, 0, 3, 1}, {3, 0, 3, 3},
	{0, 3, 1, 3}, {0, 1, 3, 1}, {0, 3, 3, 3}, {1, 3, 0, 3},
	{3, 1, 0, 1}, {3, 3, 0, 3}, {1, 3, 1, 3}, {3, 1, 3, 1},
	{3, 3, 3, 3}, {0, 1, 1, 0}, {0, 0, 1, 1}, {1, 0, 0, 1},
	{1, 1, 0, 0}, {0, 0, 0, 0}, {0, 0, 0, 0}, {0, 0, 0, 0},
	{0, 0, 0, 1}, {1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0},
	{0, 0, 0, 2}, {2, 0, 0, 0}, {0, 2, 0, 0}, {0, 0, 2, 0},
	{0, 2, 0, 1}, {1, 0, 2, 0}, {0, 1, 0, 2}, {2, 0, 1, 0},
}

// shadeLevels is how much of the cell's foreground a shade character (U+2591..U+2593) lets through, in quarters.
var shadeLevels = map[rune]int{0x2591: 1, 0x2592: 2, 0x2593: 3}

// quadrants gives the quarters of the cell a quadrant character (U+2596..U+259F) fills: 1 upper left, 2 upper right, 4 lower left, 8 lower right.
var quadrants = map[rune]int{0x2596: 4, 0x2597: 8, 0x2598: 1, 0x2599: 1 | 4 | 8, 0x259A: 1 | 8, 0x259B: 1 | 2 | 4, 0x259C: 1 | 2 | 8,
	0x259D: 2, 0x259E: 2 | 4, 0x259F: 2 | 4 | 8}

// isSpecialGlyph says whether the GIF draws r itself.
func isSpecialGlyph(r rune) bool {
	return (r >= 0x2500 && r < 0x2580 && boxHasLines(r)) || (r >= 0x2580 && r <= 0x259F) || (r >= 0x2800 && r <= 0x28FF)
}

func boxHasLines(r rune) bool {
	s := boxSegments[r-0x2500]
	return s[0]|s[1]|s[2]|s[3] != 0
}

// cellPainter draws into one cell of an image: the rectangle [x0,x1) x [y0,y1).
type cellPainter struct {
	img            *image.RGBA
	x0, y0, x1, y1 int
}

func (p cellPainter) fill(ax0, ay0, ax1, ay1 int, c [4]uint8) {
	ax0, ay0 = max(ax0, p.x0), max(ay0, p.y0)
	ax1, ay1 = min(ax1, p.x1), min(ay1, p.y1)
	for y := ay0; y < ay1; y++ {
		o := p.img.PixOffset(ax0, y)
		for x := ax0; x < ax1; x++ {
			copy(p.img.Pix[o:o+4], c[:])
			o += 4
		}
	}
}

// paintSpecial draws r in the cell in fg, over bg (which is already there). It reports false if r is not one of the characters above.
func (p cellPainter) paintSpecial(r rune, fg, bg [4]uint8) bool {
	w, h := p.x1-p.x0, p.y1-p.y0
	switch {
	case r >= 0x2500 && r < 0x2580 && boxHasLines(r):
		p.paintBox(boxSegments[r-0x2500], fg)
	case r == 0x2580: // upper half
		p.fill(p.x0, p.y0, p.x1, p.y0+h/2, fg)
	case r >= 0x2581 && r <= 0x2588: // lower 1/8 .. 8/8
		n := int(r - 0x2580)
		p.fill(p.x0, p.y1-(h*n+4)/8, p.x1, p.y1, fg)
	case r >= 0x2589 && r <= 0x258F: // left 7/8 .. 1/8
		n := 8 - int(r-0x2588)
		p.fill(p.x0, p.y0, p.x0+(w*n+4)/8, p.y1, fg)
	case r == 0x2590: // right half
		p.fill(p.x1-w/2, p.y0, p.x1, p.y1, fg)
	case r >= 0x2591 && r <= 0x2593:
		p.fill(p.x0, p.y0, p.x1, p.y1, blendQuarters(bg, fg, shadeLevels[r]))
	case r == 0x2594: // upper 1/8
		p.fill(p.x0, p.y0, p.x1, p.y0+(h+4)/8, fg)
	case r == 0x2595: // right 1/8
		p.fill(p.x1-(w+4)/8, p.y0, p.x1, p.y1, fg)
	case r >= 0x2596 && r <= 0x259F:
		q, mx, my := quadrants[r], p.x0+w/2, p.y0+h/2
		if q&1 != 0 {
			p.fill(p.x0, p.y0, mx, my, fg)
		}
		if q&2 != 0 {
			p.fill(mx, p.y0, p.x1, my, fg)
		}
		if q&4 != 0 {
			p.fill(p.x0, my, mx, p.y1, fg)
		}
		if q&8 != 0 {
			p.fill(mx, my, p.x1, p.y1, fg)
		}
	case r >= 0x2800 && r <= 0x28FF:
		p.paintBraille(uint8(r-0x2800), fg)
	default:
		return false
	}
	return true
}

// paintBraille draws the dots of a braille pattern: two columns by four rows, bit 0 the top left dot (dots 1, 2, 3 down the left,
// 4, 5, 6 down the right, then 7 and 8 under them).
func (p cellPainter) paintBraille(bits uint8, fg [4]uint8) {
	w, h := p.x1-p.x0, p.y1-p.y0
	d := max(2, w/5)
	pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	for i, ps := range pos {
		if bits&(1<<i) == 0 {
			continue
		}
		cx := p.x0 + w*(2*ps[0]+1)/4
		cy := p.y0 + h*(2*ps[1]+1)/8
		p.fill(cx-d/2, cy-d/2, cx-d/2+d, cy-d/2+d, fg)
	}
}

// lineSpan is the part of a cell's width (or height) a line covers, as offsets from the middle: [lo, hi).
type lineSpan struct{ lo, hi int }

// boxLines gives the lines a box arm of that kind is made of, t being the thickness of a light line: one light line, one of twice
// the thickness, or two light lines with a light line's width between them.
func boxLines(kind uint8, t int) []lineSpan {
	switch kind {
	case 1:
		return []lineSpan{{-t / 2, -t/2 + t}}
	case 2:
		return []lineSpan{{-t, t}}
	case 3:
		a, b := -t-t/2, t-t/2
		return []lineSpan{{a, a + t}, {b, b + t}}
	}
	return nil
}

// paintBox draws a box-drawing character: each of its four arms (up, right, down, left) runs from the middle of the cell to its
// edge. Where an arm meets arms across it, it starts at the edge of the lines it joins, so corners are filled and a double
// line's inner side stops at the inner line, as in the font.
func (p cellPainter) paintBox(seg [4]uint8, fg [4]uint8) {
	t := max(1, (p.y1-p.y0)/14)
	cx, cy := p.x0+(p.x1-p.x0)/2, p.y0+(p.y1-p.y0)/2
	for dir := 0; dir < 4; dir++ {
		kind := seg[dir]
		if kind == 0 {
			continue
		}
		vertical := dir == 0 || dir == 2
		positive := dir == 1 || dir == 2 // down, right: away from the origin
		// the arms across this one: before it (left or up) and after it (right or down)
		var lowArm, highArm uint8
		if vertical {
			lowArm, highArm = seg[3], seg[1]
		} else {
			lowArm, highArm = seg[0], seg[2]
		}
		var across [][]lineSpan
		for _, k := range []uint8{lowArm, highArm} {
			if k != 0 {
				across = append(across, boxLines(k, t))
			}
		}
		for i, ln := range boxLines(kind, t) {
			joins := len(across) > 0
			if kind == 3 { // only the line on the side of an arm across joins it; the other one goes round the corner
				joins = (i == 0 && lowArm != 0) || (i == 1 && highArm != 0)
			}
			var edge int
			switch {
			case len(across) == 0:
				edge = -t / 2
				if !positive {
					edge = t - t/2
				}
			case positive && joins:
				edge = -1 << 30
				for _, a := range across {
					edge = max(edge, a[len(a)-1].lo)
				}
			case positive:
				edge = 1 << 30
				for _, a := range across {
					edge = min(edge, a[0].lo)
				}
			case joins:
				edge = 1 << 30
				for _, a := range across {
					edge = min(edge, a[0].hi)
				}
			default:
				edge = -1 << 30
				for _, a := range across {
					edge = max(edge, a[len(a)-1].hi)
				}
			}
			switch dir {
			case 0:
				p.fill(cx+ln.lo, p.y0, cx+ln.hi, cy+edge, fg)
			case 2:
				p.fill(cx+ln.lo, cy+edge, cx+ln.hi, p.y1, fg)
			case 1:
				p.fill(cx+edge, cy+ln.lo, p.x1, cy+ln.hi, fg)
			case 3:
				p.fill(p.x0, cy+ln.lo, cx+edge, cy+ln.hi, fg)
			}
		}
	}
}

// blendQuarters is bg + (fg - bg) * q/4.
func blendQuarters(bg, fg [4]uint8, q int) [4]uint8 {
	var o [4]uint8
	for i := 0; i < 3; i++ {
		o[i] = uint8((int(bg[i])*(4-q) + int(fg[i])*q + 2) / 4)
	}
	o[3] = 255
	return o
}
