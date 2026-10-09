package lib

import (
	"strings"
	"unicode/utf8"

	"github.com/hinshun/vt10x"
)

// vtScreenMinCols/vtScreenMinRows/vtScreenMaxCols/vtScreenMaxRows bound the
// size MeasureSize will return, and are the fallback when it has nothing to
// measure (an empty recording).
//
// Unlike DisplayBuffer's grid, which grows to fit whatever content arrives,
// a real terminal emulator needs a size decided up front - it genuinely
// wraps lines at that width, the same as a real terminal would. Recordings
// don't currently carry their true terminal size: TerminalFrame.Width/Height
// exist in the format, but the recorder never actually populates them (it
// captures the real size via term.GetSize for the PTY itself, but never
// writes it into the .jsonl file).
//
// An early version of this used one large fixed size (220x50) for every
// recording regardless of what it actually used, on the theory that
// full-screen programs address every cell explicitly and so wouldn't care
// how much unused space surrounded their content. That was wrong in
// practice: the playback view scrolls to the end of the rendered text after
// every frame, and "the end" of an oversized, mostly-blank grid is nowhere
// near the real content - during the early, low-content part of a
// recording this scrolled straight past the only thing on screen, and even
// once a full-screen program filled its own real rows, a fixed size taller
// than that left a dead band of blank padding below it, visibly shrinking
// how much of the viewport its content appeared to occupy. MeasureSize
// exists to avoid guessing: it replays the actual recording once, through a
// throwaway emulator at a generous safety-cap size, and returns how far the
// cursor really travelled - almost always matching the recording's true
// terminal size, since a program can only address a cell by moving the
// cursor to it.
const (
	vtScreenMinCols, vtScreenMinRows = 80, 24
	vtScreenMaxCols, vtScreenMaxRows = 500, 150
)

// vt10x's Glyph.Mode bitmask constants are unexported (state.go), so they
// can't be referenced directly from outside the package. These mirror them
// exactly, pinned to the exact version this module depends on:
// https://github.com/hinshun/vt10x/blob/master/state.go
//
//	const (
//		attrReverse = 1 << iota
//		attrUnderline
//		attrBold
//		attrGfx
//		attrItalic
//		attrBlink
//		attrWrap
//	)
//
// TestVTScreenAttributeBitsMatchUpstream feeds real SGR sequences through an
// actual VTScreen and asserts on the rendered output, so if a future version
// of vt10x ever renumbers these, that test fails immediately rather than
// silently mis-rendering bold/reverse text.
const (
	vtAttrReverse int16 = 1 << iota
	vtAttrUnderline
	vtAttrBold
)

// VTScreen is a tview-renderable terminal display backed by a real VT100/
// xterm emulator (github.com/hinshun/vt10x): raw frame bytes are fed to a
// genuine parser and screen-buffer implementation - the same category of
// thing a real terminal runs - rather than reimplementing that parsing by
// hand. It implements the same small interface DisplayBuffer does (Feed,
// Render, IsBlank), so the two are interchangeable at call sites.
type VTScreen struct {
	term  vt10x.Terminal
	carry string // the start of an escape sequence cut by the end of the last frame (see sanitizeForVT)
}

// NewVTScreen creates an empty screen at the smallest allowed size.
// Prefer NewVTScreenSized with a size from MeasureSize whenever the frames
// to be played are known ahead of time - see the size-constants comment
// above for why an oversized guess is actively harmful, not just wasteful.
func NewVTScreen() *VTScreen {
	return NewVTScreenSized(vtScreenMinCols, vtScreenMinRows)
}

// NewVTScreenSized creates an empty screen at exactly the given size,
// clamped to [vtScreenMinCols,vtScreenMaxCols] x [vtScreenMinRows,vtScreenMaxRows].
func NewVTScreenSized(cols, rows int) *VTScreen {
	cols = clampInt(cols, vtScreenMinCols, vtScreenMaxCols)
	rows = clampInt(rows, vtScreenMinRows, vtScreenMaxRows)
	return &VTScreen{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// MeasureSize estimates the terminal size a sequence of frames actually
// used, by replaying them through a throwaway emulator at a generous
// safety-cap size ([vtScreenMaxCols]x[vtScreenMaxRows]) and tracking the
// furthest row and column the cursor ever reaches. A program can only write
// to a cell by first moving the cursor there - whether by explicit cursor
// positioning (the case that matters for full-screen programs like vim or
// htop) or by printing/wrapping into it - so the furthest the cursor ever
// travels is, for any real recording, the recording's real terminal size.
// Result is clamped to [vtScreenMinCols,vtScreenMaxCols] x
// [vtScreenMinRows,vtScreenMaxRows]; an empty frame list returns the
// minimum.
//
// Cursor position is sampled after every rune, not once per frame: a
// single frame can move the cursor somewhere, print something, and move it
// away again before the frame ends - vim's ruler, for instance, is written
// mid-frame by jumping right (often past the real content column, e.g. to
// pad-align "Top"/"Bot"/a percentage), then the cursor is immediately
// returned to the edit position, all within one frame. Sampling only after
// each frame's Write() call completes sees just that final, already-moved-
// back position and misses the wider one the ruler itself needed,
// under-measuring the grid; real playback then clips that same ruler text,
// and because vim's scroll region here spans the entire screen (no row is
// excluded from it), a clipped line wrapping at the right edge scrolls the
// whole screen by one - which is exactly what under-measuring this looked
// like when it happened for real. Sampling here is per rune rather than per
// byte specifically so a multi-byte UTF-8 character is never fed to Write()
// split across two calls, which would silently drop it (vt10x's Write()
// treats a trailing incomplete rune as "wait for more", but never gets
// "more" from a separate call - there is no cross-call buffering for
// partial runes, only for escape-sequence parser state, which does persist
// correctly across calls this way).
func MeasureSize(frames []TerminalFrame) (cols, rows int) {
	term := vt10x.New(vt10x.WithSize(vtScreenMaxCols, vtScreenMaxRows))
	maxCol, maxRow := 0, 0
	sample := func() {
		term.Lock()
		cur := term.Cursor()
		term.Unlock()
		if cur.X > maxCol {
			maxCol = cur.X
		}
		if cur.Y > maxRow {
			maxRow = cur.Y
		}
	}
	var buf [utf8.UTFMax]byte
	for _, f := range frames {
		for _, r := range f.Data {
			n := utf8.EncodeRune(buf[:], r)
			_, _ = term.Write(buf[:n])
			sample()
		}
	}
	return clampInt(maxCol+1, vtScreenMinCols, vtScreenMaxCols), clampInt(maxRow+1, vtScreenMinRows, vtScreenMaxRows)
}

// Feed processes one more raw frame of terminal data.
func (s *VTScreen) Feed(data string) {
	clean, carry := sanitizeForVT(s.carry, data)
	s.carry = carry
	_, _ = s.term.Write([]byte(clean))
}

// IsBlank reports whether nothing visible is on the screen: every cell is an
// unstyled space (or empty). Matches DisplayBuffer.IsBlank's semantics - a
// coloured space is visible, since a background-coloured bar is made of
// them - and, like it, looks only at stored cell state, never at the cursor
// overlay Render() draws.
func (s *VTScreen) IsBlank() bool {
	s.term.Lock()
	defer s.term.Unlock()
	cols, rows := s.term.Size()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			g := s.term.Cell(x, y)
			if g.Char != 0 && g.Char != ' ' {
				return false
			}
			if g.Mode != 0 || !isDefaultVTColor(g.FG) || !isDefaultVTColor(g.BG) {
				return false
			}
		}
	}
	return true
}

// isDefaultVTColor reports whether c is one of vt10x's sentinel "use the
// terminal's actual default" colours (DefaultFG/DefaultBG/DefaultCursor),
// rather than an actual palette or truecolour value - see color.go.
func isDefaultVTColor(c vt10x.Color) bool {
	return c >= 1<<24
}

// vtColorHex converts a vt10x.Color to this file's "#rrggbb" hex convention
// (or "" for one of the default-colour sentinels, meaning "let the terminal
// decide" - the same meaning "-" carries in currentTag/splitTag). Color
// packs three distinct meanings into one uint32 - see
// https://github.com/hinshun/vt10x/blob/master/color.go and the SGR 38/48
// handling in state.go's setAttr:
//   - < 16: the standard ANSI palette
//   - 16-255: the extended xterm palette (both handled uniformly by
//     xterm256Hex, which already covers the full 0-255 range the same way
//     vt10x itself does)
//   - a sentinel with bit 1<<24 set: terminal default
//   - anything else: a packed 24-bit truecolour triple, r<<16|g<<8|b
func vtColorHex(c vt10x.Color) string {
	if isDefaultVTColor(c) {
		return ""
	}
	if c <= 255 {
		return xterm256Hex(int(c))
	}
	return rgbHex(int((c>>16)&0xFF), int((c>>8)&0xFF), int(c&0xFF))
}

// vtTag builds this file's "fg:bg:attrs" tag string for glyph g.
func vtTag(g vt10x.Glyph) string {
	fg := vtColorHex(g.FG)
	if fg == "" {
		fg = "-"
	}
	bg := vtColorHex(g.BG)
	if bg == "" {
		bg = "-"
	}
	attrs := ""
	if g.Mode&vtAttrBold != 0 {
		attrs += "b"
	}
	if g.Mode&vtAttrReverse != 0 {
		attrs += "r"
	}
	if g.Mode&vtAttrUnderline != 0 {
		attrs += "u"
	}
	if attrs == "" {
		attrs = "-"
	}
	return fg + ":" + bg + ":" + attrs
}

// toggleReverse flips the reverse-video flag on a tag: adds "r" if absent,
// removes it if present. Used to paint the cursor: a real terminal's block
// cursor XORs with whatever is already there, so a cell that is already
// reverse-video via its own SGR 7 (some status lines and selections use
// this directly) and also happens to be under the cursor should read as
// normal video, not doubly-reversed - the same as it would on a real
// terminal. See reverseTag's own comment for why "r" can never simply be
// prepended with "-": the same restriction applies here.
func toggleReverse(tag string) string {
	fg, bg, attrs := splitTag(tag)
	if attrs == "-" {
		return fg + ":" + bg + ":r"
	}
	if i := strings.IndexByte(attrs, 'r'); i >= 0 {
		attrs = attrs[:i] + attrs[i+1:]
		if attrs == "" {
			attrs = "-"
		}
		return fg + ":" + bg + ":" + attrs
	}
	return fg + ":" + bg + ":" + attrs + "r"
}

// Render produces the screen's current content as a tview dynamic-colour
// markup string, ready to pass to TextView.SetText. Each row is trimmed of
// trailing blank cells before being joined with the next, matching
// DisplayBuffer.Render's convention; unlike DisplayBuffer's grid, every row
// here already physically has exactly as many cells as the screen is wide (a real emulator's
// grid is fixed-size, not grown on demand), so there is no separate
// "extend the row" step - trimming just stops early when the cursor is
// further right than the real content.
//
// When the cursor is visible, its cell is painted in reverse video - see
// VTScreen's own doc comment and the DisplayBuffer.cursorVisible field
// comment (lib/frame_parser.go) for why this matters well beyond cosmetics.
func (s *VTScreen) Render() string {
	s.term.Lock()
	defer s.term.Unlock()

	cols, rows := s.term.Size()
	cursorRow, cursorCol := -1, -1
	if s.term.CursorVisible() {
		cur := s.term.Cursor()
		cursorRow, cursorCol = cur.Y, cur.X
	}

	// Find the last row actually worth emitting: the grid is a fixed size
	// (see the size-constants comment above), but at any given moment -
	// especially early in a recording, before a full-screen program has
	// painted its whole first frame - only a prefix of it may hold real
	// content. Emitting every row regardless would make Render's output
	// always exactly [rows] lines long, and playbackView.ScrollToEnd()
	// would then scroll toward the bottom of that fixed size rather than
	// toward the real content - hiding whatever was actually written,
	// which is the opposite of what "scroll to the end" is for. Trimming
	// trailing wholly-blank rows here keeps the emitted text's own end in
	// step with the content's real end, frame to frame, the same property
	// DisplayBuffer's grid always had by construction (it simply never
	// had unused rows to trim in the first place).
	lastContentRow := -1
	for y := rows - 1; y >= 0; y-- {
		if y == cursorRow {
			lastContentRow = y
			break
		}
		blank := true
		for x := 0; x < cols; x++ {
			g := s.term.Cell(x, y)
			if (g.Char != 0 && g.Char != ' ') || g.Mode != 0 || !isDefaultVTColor(g.FG) || !isDefaultVTColor(g.BG) {
				blank = false
				break
			}
		}
		if !blank {
			lastContentRow = y
			break
		}
	}

	var out strings.Builder
	lastTag := ""
	for y := 0; y <= lastContentRow; y++ {
		if y > 0 {
			out.WriteByte('\n')
		}

		end := cols
		for end > 0 {
			if y == cursorRow && end-1 == cursorCol {
				break // never trim the cursor's own cell away
			}
			g := s.term.Cell(end-1, y)
			if (g.Char != 0 && g.Char != ' ') || g.Mode != 0 || !isDefaultVTColor(g.FG) || !isDefaultVTColor(g.BG) {
				break
			}
			end--
		}

		for x := 0; x < end; x++ {
			g := s.term.Cell(x, y)
			tag := vtTag(g)
			if y == cursorRow && x == cursorCol {
				tag = toggleReverse(tag)
			}
			if tag != lastTag {
				fg, bg, attrs := splitTag(tag)
				out.WriteString("[" + fg + ":" + bg + ":" + attrs + "]")
				lastTag = tag
			}
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			if ch == '[' {
				out.WriteString("[[")
			} else {
				out.WriteRune(ch)
			}
		}
	}
	return out.String()
}
