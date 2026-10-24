package lib

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hinshun/vt10x"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// GIFOptions say how a recording is turned into an animated GIF (see ExportToGIF). The zero value of each field means its default.
type GIFOptions struct {
	// FontPath is a TrueType or OpenType monospaced font to draw the text with (default: Go Mono, which is built in). It has no
	// bold of its own, so bold text is drawn by smearing the regular one.
	FontPath string
	// FontSize is the size of the text in pixels (default 16). The picture is as large as the terminal was, in those cells.
	FontSize float64
	// FPS is the most frames a second the GIF has (default 10): changes closer together than that are shown together. 1 to 50.
	FPS int
	// Speed plays the recording this many times faster (default 1).
	Speed float64
	// IdleLimit is the longest pause that is shown (default 2s): a pause of a minute at the prompt is a couple of seconds in the
	// GIF. A negative value keeps every pause as it was.
	IdleLimit time.Duration
	// Hold is how long the last frame stays on screen before the GIF starts over (default 3s). Negative: it does not wait.
	Hold time.Duration
	// Loop is how many times the GIF repeats after the first: 0, the default, for ever; a negative number plays it once.
	Loop int
	// Cols and Rows give the terminal's size when the recording does not (default: as it was recorded).
	Cols, Rows int
	// Foreground and Background are the colours text and the screen have when the recording does not give one, as "#rrggbb".
	Foreground, Background string
	// NoCursor leaves the cursor out.
	NoCursor bool
}

const (
	gifDefaultFontSize = 16
	gifDefaultFPS      = 10
	gifDefaultIdle     = 2 * time.Second
	gifDefaultHold     = 3 * time.Second
	gifDefaultFG       = "#c9d1d9"
	gifDefaultBG       = "#0d1117"
	// the number of pictures a GIF of a recording may have: a viewer holds them all in memory
	gifMaxFrames = 20000
)

func (o *GIFOptions) defaults() error {
	if o.FontSize == 0 {
		o.FontSize = gifDefaultFontSize
	}
	if o.FontSize < 6 || o.FontSize > 200 {
		return fmt.Errorf("the font size must be between 6 and 200 pixels, not %g", o.FontSize)
	}
	if o.FPS == 0 {
		o.FPS = gifDefaultFPS
	}
	if o.FPS < 1 || o.FPS > 50 {
		return fmt.Errorf("frames a second must be between 1 and 50, not %d", o.FPS)
	}
	if o.Speed == 0 {
		o.Speed = 1
	}
	if o.Speed < 0.05 || o.Speed > 1000 {
		return fmt.Errorf("the speed must be between 0.05 and 1000, not %g", o.Speed)
	}
	if o.IdleLimit == 0 {
		o.IdleLimit = gifDefaultIdle
	}
	if o.Hold == 0 {
		o.Hold = gifDefaultHold
	}
	if o.Foreground == "" {
		o.Foreground = gifDefaultFG
	}
	if o.Background == "" {
		o.Background = gifDefaultBG
	}
	for _, c := range []struct{ name, v string }{{"foreground", o.Foreground}, {"background", o.Background}} {
		if _, ok := parseHexRGB(c.v); !ok {
			return fmt.Errorf("the %s colour must be written #rrggbb, not %q", c.name, c.v)
		}
	}
	if o.Cols < 0 || o.Rows < 0 || o.Cols > vtScreenMaxCols || o.Rows > vtScreenMaxRows {
		return fmt.Errorf("the size must be at most %dx%d", vtScreenMaxCols, vtScreenMaxRows)
	}
	return nil
}

// parseHexRGB reads "#rrggbb" (or "rrggbb").
func parseHexRGB(s string) ([4]uint8, bool) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return [4]uint8{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return [4]uint8{}, false
	}
	return [4]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, true
}

// gifCell is one cell of the screen as it is drawn: what is in it and its colours, with reverse video and the cursor already done.
type gifCell struct {
	ch        rune
	fg, bg    [4]uint8
	bold      bool
	underline bool
}

// gifShot is the whole screen at one moment.
type gifShot struct {
	cols, rows int
	cells      []gifCell
}

// shot reads the screen. The cursor, when shown, is the cell with its colours turned round, as Render draws it.
func (s *VTScreen) gifShot(fg, bg [4]uint8, cursor bool) *gifShot {
	s.term.Lock()
	defer s.term.Unlock()
	cols, rows := s.term.Size()
	out := &gifShot{cols: cols, rows: rows, cells: make([]gifCell, cols*rows)}
	curX, curY := -1, -1
	if cursor && s.term.CursorVisible() {
		cur := s.term.Cursor()
		curX, curY = cur.X, cur.Y
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			g := s.term.Cell(x, y)
			c := gifCell{ch: g.Char, fg: vtRGB(g.FG, fg), bg: vtRGB(g.BG, bg), bold: g.Mode&vtAttrBold != 0, underline: g.Mode&vtAttrUnderline != 0}
			if c.ch == 0 || isPlaceholder(c.ch) { // a picture's cell, or nothing
				c.ch = ' '
			}
			if (g.Mode&vtAttrReverse != 0) != (x == curX && y == curY) {
				c.fg, c.bg = c.bg, c.fg
			}
			out.cells[y*cols+x] = c
		}
	}
	return out
}

// vtRGB is the colour c stands for, def when it is the terminal's default.
func vtRGB(c vt10x.Color, def [4]uint8) [4]uint8 {
	if isDefaultVTColor(c) {
		return def
	}
	if h := vtColorHex(c); h != "" {
		if rgb, ok := parseHexRGB(h); ok {
			return rgb
		}
	}
	return def
}

// gifReplay plays the frames through the terminal emulator and calls visit with the screen each time it changed, with the time
// (in milliseconds of the GIF, which is the recording's with pauses cut down and its speed changed) it shows from. The
// time of the end of it is returned. A change is seen at most once a frame of the GIF: what happens in between is not drawn.
func gifReplay(frames []TerminalFrame, cols, rows int, meta RecordingMeta, o *GIFOptions, visit func(at float64, shot *gifShot)) (end float64) {
	fg, _ := parseHexRGB(o.Foreground)
	bg, _ := parseHexRGB(o.Background)
	// not NewVTScreenSized, which never goes under 80x24: the picture is as large as the terminal was, and a line wraps where it did
	screen := &VTScreen{term: vt10x.New(vt10x.WithSize(cols, rows))}
	if meta.CellWidth > 0 && meta.CellHeight > 0 {
		screen.SetCellSize(meta.CellWidth, meta.CellHeight)
	}
	tick := 1000 / float64(o.FPS)
	idle := float64(o.IdleLimit / time.Millisecond)
	var adj float64 // the GIF's time at the frame being fed
	lastTick := int64(-1)
	var prev *gifShot
	emit := func() {
		cur := screen.gifShot(fg, bg, !o.NoCursor)
		if prev != nil && sameShot(prev, cur) {
			return
		}
		visit(float64(lastTick)*tick, cur)
		prev = cur
	}
	prevTS := frames[0].Timestamp
	for _, f := range frames {
		gap := float64(f.Timestamp - prevTS)
		prevTS = f.Timestamp
		if gap < 0 {
			gap = 0
		}
		if o.IdleLimit > 0 && gap > idle {
			gap = idle
		}
		adj += gap / o.Speed
		if t := int64(adj / tick); t != lastTick {
			if lastTick >= 0 {
				emit()
			}
			lastTick = t
		}
		screen.Feed(f.Data)
	}
	emit()
	return adj
}

func sameShot(a, b *gifShot) bool {
	if a.cols != b.cols || a.rows != b.rows {
		return false
	}
	for i := range a.cells {
		if a.cells[i] != b.cells[i] {
			return false
		}
	}
	return true
}

// gifFonts are the faces the text is drawn with.
type gifFonts struct {
	regular, bold font.Face
	regFont       *opentype.Font
	boldFont      *opentype.Font
	smearBold     bool // there is no bold face: draw the regular one twice, a pixel apart
	cw, ch, base  int  // a cell's size in pixels, and where the baseline is in it
}

func loadGIFFonts(path string, size float64) (*gifFonts, error) {
	regData, boldData := gomono.TTF, gomonobold.TTF
	if path != "" {
		d, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read the font: %w", err)
		}
		regData, boldData = d, nil
	}
	f := &gifFonts{}
	var err error
	if f.regFont, err = opentype.Parse(regData); err != nil {
		return nil, fmt.Errorf("cannot use %s as a font: %w", path, err)
	}
	opts := &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull}
	if f.regular, err = opentype.NewFace(f.regFont, opts); err != nil {
		return nil, err
	}
	if boldData != nil {
		if f.boldFont, err = opentype.Parse(boldData); err != nil {
			return nil, err
		}
		if f.bold, err = opentype.NewFace(f.boldFont, opts); err != nil {
			return nil, err
		}
	} else {
		f.bold, f.boldFont, f.smearBold = f.regular, f.regFont, true
	}
	adv, ok := f.regular.GlyphAdvance('M')
	if !ok || adv <= 0 {
		return nil, fmt.Errorf("the font has no usable letters")
	}
	m := f.regular.Metrics()
	f.cw, f.ch, f.base = adv.Round(), m.Height.Ceil(), m.Ascent.Ceil()
	if f.cw < 3 || f.ch < 4 {
		return nil, fmt.Errorf("the font is too small to read at %g pixels", size)
	}
	return f, nil
}

type glyphKey struct {
	r    rune
	bold bool
}

// gifRenderer draws screens into one picture, a cell at a time.
type gifRenderer struct {
	fonts *gifFonts
	pad   int
	img   *image.RGBA
	masks map[glyphKey][]uint8 // a glyph's coverage of its cell, in quarters (0-4); nil: nothing to draw
	buf   sfnt.Buffer
	bg    [4]uint8
	cols  int
	rows  int
	prev  *gifShot
}

func newGIFRenderer(fonts *gifFonts, cols, rows int, bg [4]uint8) *gifRenderer {
	r := &gifRenderer{fonts: fonts, pad: fonts.cw, masks: map[glyphKey][]uint8{}, bg: bg, cols: cols, rows: rows}
	r.img = image.NewRGBA(image.Rect(0, 0, cols*fonts.cw+2*r.pad, rows*fonts.ch+2*r.pad))
	r.fillAll(bg)
	return r
}

func (r *gifRenderer) fillAll(c [4]uint8) {
	for i := 0; i < len(r.img.Pix); i += 4 {
		copy(r.img.Pix[i:i+4], c[:])
	}
}

// mask is the coverage of the cell by glyph k, from 0 to 4; nil if it draws nothing (a space, or a letter the font has not).
func (r *gifRenderer) mask(k glyphKey) []uint8 {
	if m, ok := r.masks[k]; ok {
		return m
	}
	var m []uint8
	f := r.fonts
	face, fnt := f.regular, f.regFont
	if k.bold {
		face, fnt = f.bold, f.boldFont
	}
	if idx, err := fnt.GlyphIndex(&r.buf, k.r); err == nil && idx != 0 && k.r != ' ' {
		dr, mk, mp, _, ok := face.Glyph(fixed.P(0, f.base), k.r)
		if ok && mk != nil {
			m = make([]uint8, f.cw*f.ch)
			any := false
			put := func(x, y int, a uint32) {
				if x < 0 || y < 0 || x >= f.cw || y >= f.ch {
					return
				}
				q := uint8((a*4 + 32768) / 65535) // quarters: a few levels of anti-aliasing, so the GIF's palette stays small
				if q > m[y*f.cw+x] {
					m[y*f.cw+x] = q
					any = true
				}
			}
			for y := dr.Min.Y; y < dr.Max.Y; y++ {
				for x := dr.Min.X; x < dr.Max.X; x++ {
					_, _, _, a := mk.At(mp.X+x-dr.Min.X, mp.Y+y-dr.Min.Y).RGBA()
					put(x, y, a)
					if k.bold && f.smearBold {
						put(x+1, y, a)
					}
				}
			}
			if !any {
				m = nil
			}
		}
	}
	r.masks[k] = m
	return m
}

// paintCell draws cell c at column x, row y.
func (r *gifRenderer) paintCell(x, y int, c gifCell) {
	f := r.fonts
	p := cellPainter{img: r.img, x0: r.pad + x*f.cw, y0: r.pad + y*f.ch}
	p.x1, p.y1 = p.x0+f.cw, p.y0+f.ch
	p.fill(p.x0, p.y0, p.x1, p.y1, c.bg)
	if c.ch != ' ' && !p.paintSpecial(c.ch, c.fg, c.bg) {
		if m := r.mask(glyphKey{c.ch, c.bold}); m != nil {
			for j := 0; j < f.ch; j++ {
				o := r.img.PixOffset(p.x0, p.y0+j)
				for i := 0; i < f.cw; i++ {
					switch q := int(m[j*f.cw+i]); q {
					case 0:
					case 4:
						copy(r.img.Pix[o+4*i:o+4*i+4], c.fg[:])
					default:
						b := blendQuarters(c.bg, c.fg, q)
						copy(r.img.Pix[o+4*i:o+4*i+4], b[:])
					}
				}
			}
		}
	}
	if c.underline {
		th := max(1, f.ch/16)
		y0 := min(p.y0+f.base+max(1, f.ch/16), p.y1-th)
		p.fill(p.x0, y0, p.x1, y0+th, c.fg)
	}
}

// draw paints what changed since the last screen and returns the rectangle it touched (empty if nothing).
func (r *gifRenderer) draw(s *gifShot) image.Rectangle {
	f := r.fonts
	if r.prev == nil {
		for y := 0; y < s.rows; y++ {
			for x := 0; x < s.cols; x++ {
				r.paintCell(x, y, s.cells[y*s.cols+x])
			}
		}
		r.prev = s
		return r.img.Bounds()
	}
	x0, y0, x1, y1 := s.cols, s.rows, -1, -1
	for y := 0; y < s.rows; y++ {
		for x := 0; x < s.cols; x++ {
			if s.cells[y*s.cols+x] == r.prev.cells[y*s.cols+x] {
				continue
			}
			r.paintCell(x, y, s.cells[y*s.cols+x])
			x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
		}
	}
	r.prev = s
	if x1 < 0 {
		return image.Rectangle{}
	}
	return image.Rect(r.pad+x0*f.cw, r.pad+y0*f.ch, r.pad+(x1+1)*f.cw, r.pad+(y1+1)*f.ch)
}

func rgbKey(c [4]uint8) uint32 { return uint32(c[0])<<16 | uint32(c[1])<<8 | uint32(c[2]) }

// gifPaletteFor chooses at most 255 colours for the GIF: all of those that are drawn if there are few enough, else the 255 that
// stand for them best (median cut, each colour counting as often as it appears).
func gifPaletteFor(weights map[uint32]int) color.Palette {
	if len(weights) <= 255 {
		keys := make([]uint32, 0, len(weights))
		for k := range weights {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return weights[keys[i]] > weights[keys[j]] || (weights[keys[i]] == weights[keys[j]] && keys[i] < keys[j])
		})
		pal := make(color.Palette, len(keys))
		for i, k := range keys {
			pal[i] = color.RGBA{uint8(k >> 16), uint8(k >> 8), uint8(k), 255}
		}
		return pal
	}
	type wc struct {
		c uint32
		w int
	}
	all := make([]wc, 0, len(weights))
	for k, w := range weights {
		all = append(all, wc{k, w})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].c < all[j].c }) // the same palette every time
	boxes := [][]wc{all}
	channel := func(c uint32, ch int) int { return int(c>>(16-8*uint(ch))) & 255 }
	spread := func(b []wc) (best, size int) {
		for ch := 0; ch < 3; ch++ {
			lo, hi := 255, 0
			for _, e := range b {
				v := channel(e.c, ch)
				lo, hi = min(lo, v), max(hi, v)
			}
			if hi-lo >= size {
				best, size = ch, hi-lo
			}
		}
		return
	}
	for len(boxes) < 255 {
		pick, pickSize := -1, 0
		for i, b := range boxes {
			if len(b) < 2 {
				continue
			}
			if _, size := spread(b); size*len(b) > pickSize { // the box that is both wide and holds many colours
				pick, pickSize = i, size*len(b)
			}
		}
		if pick < 0 {
			break
		}
		b := boxes[pick]
		ch, _ := spread(b)
		sort.Slice(b, func(i, j int) bool { return channel(b[i].c, ch) < channel(b[j].c, ch) })
		total, acc, cut := 0, 0, 1
		for _, e := range b {
			total += e.w
		}
		for i, e := range b {
			acc += e.w
			if acc*2 >= total {
				cut = min(max(i+1, 1), len(b)-1)
				break
			}
		}
		boxes[pick], boxes = b[:cut], append(boxes, b[cut:])
	}
	pal := make(color.Palette, 0, len(boxes))
	for _, b := range boxes {
		var sum [3]int
		tw := 0
		for _, e := range b {
			for ch := 0; ch < 3; ch++ {
				sum[ch] += channel(e.c, ch) * e.w
			}
			tw += e.w
		}
		pal = append(pal, color.RGBA{uint8((sum[0] + tw/2) / tw), uint8((sum[1] + tw/2) / tw), uint8((sum[2] + tw/2) / tw), 255})
	}
	return pal
}

// paletteIndex finds the entry of pal nearest to each colour asked for, remembering the answers.
type paletteIndex struct {
	pal   color.Palette
	cache map[uint32]uint8
}

func newPaletteIndex(pal color.Palette) *paletteIndex {
	pi := &paletteIndex{pal: pal, cache: make(map[uint32]uint8, len(pal)*2)}
	for i, c := range pal {
		r, g, b, _ := c.RGBA()
		pi.cache[uint32(r>>8)<<16|uint32(g>>8)<<8|uint32(b>>8)] = uint8(i)
	}
	return pi
}

func (pi *paletteIndex) of(key uint32) uint8 {
	if i, ok := pi.cache[key]; ok {
		return i
	}
	r, g, b := int(key>>16&255), int(key>>8&255), int(key&255)
	best, bestD := 0, 1<<30
	for i, c := range pi.pal {
		pr, pg, pb, _ := c.RGBA()
		dr, dg, db := r-int(pr>>8), g-int(pg>>8), b-int(pb>>8)
		if d := 2*dr*dr + 4*dg*dg + 3*db*db; d < bestD { // the eye is most sensitive to green, least to blue
			best, bestD = i, d
		}
	}
	pi.cache[key] = uint8(best)
	return uint8(best)
}

// shotColours adds to weights the colours drawing the cells of s that differ from those of prev needs. A letter takes its two
// colours and what is between them (a quarter, a half, three quarters), a shade character some of those, and a space only the background.
func shotColours(weights map[uint32]int, s, prev *gifShot) {
	for i, c := range s.cells {
		if prev != nil && prev.cols == s.cols && prev.rows == s.rows && prev.cells[i] == c {
			continue
		}
		weights[rgbKey(c.bg)]++
		if c.ch == ' ' && !c.underline {
			continue
		}
		weights[rgbKey(c.fg)]++
		if !isSpecialGlyph(c.ch) || (c.ch >= 0x2591 && c.ch <= 0x2593) {
			for q := 1; q <= 3; q++ {
				weights[rgbKey(blendQuarters(c.bg, c.fg, q))]++
			}
		}
	}
}

// ExportToGIF writes the recording as an animated GIF at outPath: the screen of the terminal as the editor shows it, from the
// first frame to the last. Sound is not in a GIF, and neither are pictures (sixel, inline images) nor timed annotations.
// It returns a one-line description of what was written.
func ExportToGIF(filePath, outPath string, opts GIFOptions) (string, error) {
	o := opts
	if err := o.defaults(); err != nil {
		return "", err
	}
	frames, _, meta, err := LoadFrames(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to parse recording: %w", err)
	}
	if len(frames) == 0 {
		return "", fmt.Errorf("no frames found in recording")
	}
	cols, rows := meta.Width, meta.Height
	if cols <= 0 || rows <= 0 {
		cols, rows = MeasureSize(frames)
	}
	if o.Cols > 0 {
		cols = o.Cols
	}
	if o.Rows > 0 {
		rows = o.Rows
	}
	cols, rows = clampInt(cols, 1, vtScreenMaxCols), clampInt(rows, 1, vtScreenMaxRows)
	fonts, err := loadGIFFonts(o.FontPath, o.FontSize)
	if err != nil {
		return "", err
	}

	// first pass: which colours are drawn, and how many pictures there will be
	weights := map[uint32]int{}
	bg, _ := parseHexRGB(o.Background)
	weights[rgbKey(bg)]++
	var prevShot *gifShot
	shots := 0
	gifReplay(frames, cols, rows, meta, &o, func(_ float64, s *gifShot) {
		shotColours(weights, s, prevShot)
		prevShot = s
		shots++
	})
	if shots > gifMaxFrames {
		return "", fmt.Errorf("the GIF would have %d pictures (the most is %d): make it shorter, or use a lower -fps, a higher -speed or a shorter -idle", shots, gifMaxFrames)
	}
	pal := gifPaletteFor(weights)
	index := newPaletteIndex(pal)

	// second pass: the pictures
	rd := newGIFRenderer(fonts, cols, rows, bg)
	out := &gif.GIF{LoopCount: o.Loop, Config: image.Config{ColorModel: pal, Width: rd.img.Bounds().Dx(), Height: rd.img.Bounds().Dy()}}
	if o.Loop < 0 {
		out.LoopCount = -1
	}
	var starts []float64
	add := func(at float64, rect image.Rectangle) {
		pm := image.NewPaletted(rect, pal)
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			so := rd.img.PixOffset(rect.Min.X, y)
			do := pm.PixOffset(rect.Min.X, y)
			for x := 0; x < rect.Dx(); x++ {
				pm.Pix[do+x] = index.of(uint32(rd.img.Pix[so+4*x])<<16 | uint32(rd.img.Pix[so+4*x+1])<<8 | uint32(rd.img.Pix[so+4*x+2]))
			}
		}
		out.Image = append(out.Image, pm)
		out.Disposal = append(out.Disposal, gif.DisposalNone)
		starts = append(starts, at)
	}
	end := gifReplay(frames, cols, rows, meta, &o, func(at float64, s *gifShot) {
		rect := rd.draw(s)
		if len(out.Image) == 0 {
			rect = rd.img.Bounds() // the first picture is the whole screen, and shows from the start
			at = 0
		}
		if !rect.Empty() {
			add(at, rect)
		}
	})
	if len(out.Image) == 0 {
		return "", fmt.Errorf("nothing to draw")
	}
	// How long each stays: until the next one. Delays are whole hundredths of a second, so the rounding is carried on
	// rather than lost, and none is under 2: viewers show a shorter one as ten.
	hold := float64(o.Hold / time.Millisecond)
	if o.Hold < 0 {
		hold = 0
	}
	cum := 0
	for i := range out.Image {
		next := end + hold
		if i+1 < len(starts) {
			next = starts[i+1]
		}
		target := int(next/10 + 0.5)
		d := max(target-cum, 2)
		out.Delay = append(out.Delay, d)
		cum += d
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, out); err != nil {
		return "", fmt.Errorf("cannot encode the GIF: %w", err)
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", outPath, err)
	}
	return fmt.Sprintf("%s: %dx%d pixels (%dx%d characters), %d pictures, %s, %d colours, %.1f MB", outPath, rd.img.Bounds().Dx(), rd.img.Bounds().Dy(), cols, rows,
		len(out.Image), (time.Duration(cum) * 10 * time.Millisecond).Round(100*time.Millisecond), len(pal), float64(buf.Len())/1e6), nil
}
