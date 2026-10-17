package lib

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Sixel graphics: the decoder, the encoder and the little image operations (resize, crop) between them.
//
// A sixel image arrives in the terminal's output as a Device Control String,
//
//	ESC P p1 ; p2 ; p3 q  data  ESC \
//
// where the data draws the picture six pixel rows (a "band") at a time:
//
//	"Pan;Pad;Ph;Pv   raster attributes: the aspect ratio and, if given, the size of the picture
//	#n               use colour register n
//	#n;2;r;g;b       define register n as RGB (each 0-100, percent)
//	#n;1;h;l;s       define register n as HLS (hue 0-360, lightness and saturation 0-100)
//	? ... ~          one column of six pixels: the character, less 63, is the 6 bits (the lowest is the top pixel)
//	!n c             repeat the column c, n times
//	$                back to the left edge, to paint the same band in another colour
//	-                the next band
//
// What the pixels are painted with is the colour register in force; a pixel nothing painted is left alone (it
// shows what is under the picture - the terminal's background). Everything here reads that as transparent.

const (
	// sixelMaxDim and sixelMaxPixels bound a decoded picture. A recording is somebody else's file: a sixel that
	// claims to be 60000 pixels wide must not be allowed to make a player allocate gigabytes.
	sixelMaxDim    = 4096
	sixelMaxPixels = 8 << 20
)

// SixelImage is a decoded sixel picture: Pix is W*H pixels of RGBA, row after row; alpha is 0 for a pixel the
// picture did not paint and 255 for one it did.
type SixelImage struct {
	W, H int
	Pix  []byte
}

// ErrNotSixel is returned for a Device Control String that is not a sixel graphic.
var ErrNotSixel = errors.New("sixel: not a sixel graphic")

// sixelBody splits the contents of a Device Control String (what is between "ESC P" and the terminator) into
// the graphic's data, ok being false when it is not a sixel graphic at all - its parameters (digits and ";")
// end in "q".
func sixelBody(dcs string) (data string, ok bool) {
	i := 0
	for i < len(dcs) && (dcs[i] == ';' || (dcs[i] >= '0' && dcs[i] <= '9')) {
		i++
	}
	if i >= len(dcs) || dcs[i] != 'q' {
		return "", false
	}
	return dcs[i+1:], true
}

// IsSixel reports whether the contents of a Device Control String are a sixel graphic.
func IsSixel(dcs string) bool {
	_, ok := sixelBody(dcs)
	return ok
}

// sixelParser walks the data of a graphic and reports what it draws.
type sixelParser struct {
	onRaster func(w, h int)
	onDefine func(n int, r, g, b byte)
	onSelect func(n int)
	// onRun: count columns of the six pixels in bits, at column x of band 'band'.
	onRun func(x, band int, bits byte, count int)
}

// walk reads the graphic. It stops, with an error, at a size no player should accept.
func (p *sixelParser) walk(data string) error {
	x, band := 0, 0
	maxX := 0
	i := 0
	num := func() (int, bool) { // a number at i
		start := i
		v := 0
		for i < len(data) && data[i] >= '0' && data[i] <= '9' {
			if v < 1<<20 {
				v = v*10 + int(data[i]-'0')
			}
			i++
		}
		return v, i > start
	}
	for i < len(data) {
		c := data[i]
		switch {
		case c == '"': // raster attributes: Pan;Pad;Ph;Pv
			i++
			var a [4]int
			n := 0
			for n < 4 {
				v, _ := num()
				a[n] = v
				n++
				if i < len(data) && data[i] == ';' {
					i++
					continue
				}
				break
			}
			if n == 4 && p.onRaster != nil {
				p.onRaster(a[2], a[3])
			}
		case c == '#':
			i++
			n, _ := num()
			if i < len(data) && data[i] == ';' {
				var a [4]int
				k := 0
				for k < 4 && i < len(data) && data[i] == ';' {
					i++
					a[k], _ = num()
					k++
				}
				if k == 4 && p.onDefine != nil {
					r, g, b := sixelColor(a[0], a[1], a[2], a[3])
					p.onDefine(n, r, g, b)
				}
			}
			if p.onSelect != nil {
				p.onSelect(n)
			}
		case c == '!':
			i++
			count, _ := num()
			if i < len(data) && data[i] >= '?' && data[i] <= '~' {
				if count < 1 {
					count = 1
				}
				if x+count > sixelMaxDim {
					return fmt.Errorf("sixel: wider than %d pixels", sixelMaxDim)
				}
				if p.onRun != nil {
					p.onRun(x, band, data[i]-'?', count)
				}
				x += count
				if x > maxX {
					maxX = x
				}
				i++
			}
		case c >= '?' && c <= '~':
			if x+1 > sixelMaxDim {
				return fmt.Errorf("sixel: wider than %d pixels", sixelMaxDim)
			}
			if p.onRun != nil {
				p.onRun(x, band, c-'?', 1)
			}
			x++
			if x > maxX {
				maxX = x
			}
			i++
		case c == '$':
			x = 0
			i++
		case c == '-':
			x = 0
			band++
			if band*6 >= sixelMaxDim {
				return fmt.Errorf("sixel: taller than %d pixels", sixelMaxDim)
			}
			i++
		default: // whitespace, newlines, anything not meant for us
			i++
		}
	}
	return nil
}

// sixelColor turns a colour definition into RGB. Pu 2 is RGB in percent, 1 is HLS (hue 0-360; the VT340's hue
// circle starts at blue - 0 is blue, 120 red, 240 green - which is why 240 is added).
func sixelColor(pu, a, b, c int) (r, g, bl byte) {
	pct := func(v int) byte {
		if v > 100 {
			v = 100
		}
		return byte((v*255 + 50) / 100)
	}
	if pu == 2 {
		return pct(a), pct(b), pct(c)
	}
	// HLS
	h := float64((a+240)%360) / 360
	l := float64(min(b, 100)) / 100
	s := float64(min(c, 100)) / 100
	if s == 0 {
		v := byte(l*255 + 0.5)
		return v, v, v
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	hue := func(t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		switch {
		case t < 1.0/6:
			return p + (q-p)*6*t
		case t < 0.5:
			return q
		case t < 2.0/3:
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	f := func(v float64) byte { return byte(v*255 + 0.5) }
	return f(hue(h + 1.0/3)), f(hue(h)), f(hue(h - 1.0/3))
}

// The sixel specification's default colour registers (the VT340's 16).
var sixelDefaultPalette = [16][3]byte{
	{0, 0, 0}, {51, 51, 204}, {204, 33, 33}, {51, 204, 51}, {204, 51, 204}, {51, 204, 204}, {204, 204, 51}, {135, 135, 135},
	{66, 66, 66}, {84, 84, 153}, {153, 66, 66}, {84, 153, 84}, {153, 84, 153}, {84, 153, 153}, {153, 153, 84}, {204, 204, 204},
}

// SixelSize reports how large the picture is, in pixels, without drawing it: what the raster attributes say, if
// they give a size (what is drawn beyond it is cut off, as libsixel does), and otherwise what the data covers.
func SixelSize(dcs string) (w, h int, err error) {
	data, ok := sixelBody(dcs)
	if !ok {
		return 0, 0, ErrNotSixel
	}
	rw, rh := 0, 0
	maxX, maxBand := 0, -1
	p := &sixelParser{
		onRaster: func(w, h int) { rw, rh = w, h },
		onRun: func(x, band int, bits byte, count int) {
			// blank columns take room too
			if x+count > maxX {
				maxX = x + count
			}
			if band > maxBand {
				maxBand = band
			}
		},
	}
	if err := p.walk(data); err != nil {
		return 0, 0, err
	}
	if rw > 0 && rh > 0 {
		w, h = rw, rh
	} else {
		w, h = maxX, (maxBand+1)*6
	}
	if w > sixelMaxDim || h > sixelMaxDim || w*h > sixelMaxPixels {
		return 0, 0, fmt.Errorf("sixel: %dx%d pixels is too large", w, h)
	}
	return w, h, nil
}

// DecodeSixel draws the graphic in a Device Control String (the part between "ESC P" and the terminator).
func DecodeSixel(dcs string) (*SixelImage, error) {
	w, h, err := SixelSize(dcs)
	if err != nil {
		return nil, err
	}
	if w == 0 || h == 0 {
		return &SixelImage{}, nil
	}
	data, _ := sixelBody(dcs)
	img := &SixelImage{W: w, H: h, Pix: make([]byte, w*h*4)}
	var pal [256][3]byte
	for i, c := range sixelDefaultPalette {
		pal[i] = c
	}
	cur := 0
	p := &sixelParser{
		onDefine: func(n int, r, g, b byte) {
			if n >= 0 && n < len(pal) {
				pal[n] = [3]byte{r, g, b}
			}
		},
		onSelect: func(n int) {
			if n >= 0 && n < len(pal) {
				cur = n
			}
		},
		onRun: func(x, band int, bits byte, count int) {
			if bits == 0 {
				return
			}
			c := pal[cur]
			for bit := 0; bit < 6; bit++ {
				if bits&(1<<bit) == 0 {
					continue
				}
				y := band*6 + bit
				if y >= h {
					break
				}
				for k := 0; k < count && x+k < w; k++ {
					o := (y*w + x + k) * 4
					img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = c[0], c[1], c[2], 255
				}
			}
		},
	}
	if err := p.walk(data); err != nil {
		return nil, err
	}
	return img, nil
}

// Resize returns the picture scaled to w x h pixels, each new pixel the average of the old ones it covers.
func (img *SixelImage) Resize(w, h int) *SixelImage {
	if w < 1 || h < 1 || img.W == 0 || img.H == 0 {
		return &SixelImage{}
	}
	if w == img.W && h == img.H {
		return img
	}
	out := &SixelImage{W: w, H: h, Pix: make([]byte, w*h*4)}
	for y := 0; y < h; y++ {
		y0 := y * img.H / h
		y1 := max((y+1)*img.H/h, y0+1)
		for x := 0; x < w; x++ {
			x0 := x * img.W / w
			x1 := max((x+1)*img.W/w, x0+1)
			var r, g, b, painted, n int
			for yy := y0; yy < y1 && yy < img.H; yy++ {
				for xx := x0; xx < x1 && xx < img.W; xx++ {
					o := (yy*img.W + xx) * 4
					n++
					if img.Pix[o+3] != 0 {
						r += int(img.Pix[o])
						g += int(img.Pix[o+1])
						b += int(img.Pix[o+2])
						painted++
					}
				}
			}
			if painted == 0 || painted*2 < n {
				continue // mostly unpainted: it stays so
			}
			o := (y*w + x) * 4
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = byte(r/painted), byte(g/painted), byte(b/painted), 255
		}
	}
	return out
}

// Crop returns the part of the picture in the rectangle (x, y, w, h), clipped to the picture.
func (img *SixelImage) Crop(x, y, w, h int) *SixelImage {
	x0, y0 := max(x, 0), max(y, 0)
	x1, y1 := min(x+w, img.W), min(y+h, img.H)
	if x1 <= x0 || y1 <= y0 {
		return &SixelImage{}
	}
	out := &SixelImage{W: x1 - x0, H: y1 - y0}
	out.Pix = make([]byte, out.W*out.H*4)
	for yy := y0; yy < y1; yy++ {
		copy(out.Pix[(yy-y0)*out.W*4:], img.Pix[(yy*img.W+x0)*4:(yy*img.W+x1)*4])
	}
	return out
}

// EncodeSixel writes the picture as a complete sixel graphic (ESC P ... ESC \). Unpainted pixels stay unpainted
// (the P2=1 mode: what is under the picture shows). At most 255 colours are used: a picture with more is
// reduced, by median cut.
func EncodeSixel(img *SixelImage) string {
	if img.W == 0 || img.H == 0 {
		return ""
	}
	pal, index := quantize(img)
	var b strings.Builder
	b.WriteString("\x1bP0;1;0q")
	fmt.Fprintf(&b, "\"1;1;%d;%d", img.W, img.H)
	for i, c := range pal {
		fmt.Fprintf(&b, "#%d;2;%d;%d;%d", i, (int(c[0])*100+127)/255, (int(c[1])*100+127)/255, (int(c[2])*100+127)/255)
	}
	bands := (img.H + 5) / 6
	row := make([]byte, img.W)
	for band := 0; band < bands; band++ {
		// which colours the band uses
		used := map[int]bool{}
		for y := band * 6; y < min(band*6+6, img.H); y++ {
			for x := 0; x < img.W; x++ {
				if c := index[y*img.W+x]; c >= 0 {
					used[int(c)] = true
				}
			}
		}
		colours := make([]int, 0, len(used))
		for c := range used {
			colours = append(colours, c)
		}
		sort.Ints(colours)
		for k, c := range colours {
			if k > 0 {
				b.WriteByte('$')
			}
			fmt.Fprintf(&b, "#%d", c)
			for x := 0; x < img.W; x++ {
				var bits byte
				for bit := 0; bit < 6; bit++ {
					y := band*6 + bit
					if y < img.H && index[y*img.W+x] == int16(c) {
						bits |= 1 << bit
					}
				}
				row[x] = bits + '?'
			}
			// run-length: !n c for runs of 4 or more, as short as it gets
			for x := 0; x < img.W; {
				j := x
				for j < img.W && row[j] == row[x] {
					j++
				}
				n := j - x
				if n > 3 {
					b.WriteString("!" + strconv.Itoa(n))
					b.WriteByte(row[x])
				} else {
					for ; n > 0; n-- {
						b.WriteByte(row[x])
					}
				}
				x = j
			}
		}
		if band+1 < bands {
			b.WriteByte('-')
		}
	}
	b.WriteString("\x1b\\")
	return b.String()
}

// quantize gives the picture's palette (at most 255 colours) and each pixel's index in it, -1 for unpainted.
func quantize(img *SixelImage) (pal [][3]byte, index []int16) {
	index = make([]int16, img.W*img.H)
	colours := map[[3]byte]int{}
	exact := true
	for i := range index {
		o := i * 4
		if img.Pix[o+3] == 0 {
			index[i] = -1
			continue
		}
		c := [3]byte{img.Pix[o], img.Pix[o+1], img.Pix[o+2]}
		if _, ok := colours[c]; !ok {
			if len(colours) >= 255 {
				exact = false
				break
			}
			colours[c] = len(pal)
			pal = append(pal, c)
		}
		index[i] = int16(colours[c])
	}
	if exact {
		return pal, index
	}

	// Too many colours: median cut over the distinct ones, weighted by how often each is used.
	type entry struct {
		c [3]byte
		n int
	}
	count := map[[3]byte]int{}
	for i := 0; i < img.W*img.H; i++ {
		o := i * 4
		if img.Pix[o+3] != 0 {
			count[[3]byte{img.Pix[o], img.Pix[o+1], img.Pix[o+2]}]++
		}
	}
	all := make([]entry, 0, len(count))
	for c, n := range count {
		all = append(all, entry{c, n})
	}
	boxes := [][]entry{all}
	for len(boxes) < 255 {
		// split the box with the widest spread of any colour channel
		best, bestCh, bestSpread := -1, 0, 0
		for bi, bx := range boxes {
			if len(bx) < 2 {
				continue
			}
			for ch := 0; ch < 3; ch++ {
				lo, hi := 255, 0
				for _, e := range bx {
					lo, hi = min(lo, int(e.c[ch])), max(hi, int(e.c[ch]))
				}
				if hi-lo > bestSpread {
					best, bestCh, bestSpread = bi, ch, hi-lo
				}
			}
		}
		if best < 0 {
			break
		}
		bx := boxes[best]
		sort.Slice(bx, func(i, j int) bool { return bx[i].c[bestCh] < bx[j].c[bestCh] })
		total := 0
		for _, e := range bx {
			total += e.n
		}
		acc, cut := 0, 1
		for k, e := range bx {
			acc += e.n
			if acc*2 >= total {
				cut = max(k+1, 1)
				break
			}
		}
		if cut >= len(bx) {
			cut = len(bx) - 1
		}
		boxes[best] = bx[:cut]
		boxes = append(boxes, bx[cut:])
	}
	pal = pal[:0]
	lookup := map[[3]byte]int16{}
	for bi, bx := range boxes {
		var r, g, b, n int
		for _, e := range bx {
			r += int(e.c[0]) * e.n
			g += int(e.c[1]) * e.n
			b += int(e.c[2]) * e.n
			n += e.n
		}
		pal = append(pal, [3]byte{byte(r / n), byte(g / n), byte(b / n)})
		for _, e := range bx {
			lookup[e.c] = int16(bi)
		}
	}
	for i := range index {
		o := i * 4
		if img.Pix[o+3] == 0 {
			index[i] = -1
			continue
		}
		index[i] = lookup[[3]byte{img.Pix[o], img.Pix[o+1], img.Pix[o+2]}]
	}
	return pal, index
}
