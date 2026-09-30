package lib

import (
	"strings"
	"testing"
)

func vtCellAt(t *testing.T, markup string, row, col int) renderedCell {
	t.Helper()
	return cellAtLine(t, markup, row, col)
}

func TestMeasureSize(t *testing.T) {
	cases := []struct {
		name           string
		frames         []TerminalFrame
		wantCols       int
		wantRows       int
		wantAtLeastCol int
		wantAtLeastRow int
	}{
		{
			name:     "empty recording falls back to the minimum",
			frames:   nil,
			wantCols: vtScreenMinCols,
			wantRows: vtScreenMinRows,
		},
		{
			name:     "small content stays at the minimum, never shrinks below it",
			frames:   []TerminalFrame{{Data: "hi"}},
			wantCols: vtScreenMinCols,
			wantRows: vtScreenMinRows,
		},
		{
			// vim's own scroll-region setup ("ESC[1;38r") plus its ruler at
			// column 155 is exactly the real shape from the reported bug:
			// explicit cursor addressing reveals the recording's real size.
			name: "explicit cursor addressing beyond the minimum is measured",
			frames: []TerminalFrame{
				{Data: "\x1b[1;38r\x1b[38;155H2"},
			},
			wantAtLeastCol: 155,
			wantAtLeastRow: 38,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cols, rows := MeasureSize(c.frames)
			if c.wantCols != 0 && cols != c.wantCols {
				t.Errorf("cols = %d, want %d", cols, c.wantCols)
			}
			if c.wantRows != 0 && rows != c.wantRows {
				t.Errorf("rows = %d, want %d", rows, c.wantRows)
			}
			if c.wantAtLeastCol != 0 && cols < c.wantAtLeastCol {
				t.Errorf("cols = %d, want at least %d", cols, c.wantAtLeastCol)
			}
			if c.wantAtLeastRow != 0 && rows < c.wantAtLeastRow {
				t.Errorf("rows = %d, want at least %d", rows, c.wantAtLeastRow)
			}
		})
	}
}

func TestMeasureSizeIsClampedToTheSafetyCap(t *testing.T) {
	cols, rows := MeasureSize([]TerminalFrame{{Data: "\x1b[9999;9999H"}})
	if cols != vtScreenMaxCols || rows != vtScreenMaxRows {
		t.Errorf("cols=%d rows=%d, want the safety cap %dx%d", cols, rows, vtScreenMaxCols, vtScreenMaxRows)
	}
}

// TestNewVTScreenSizedClampsInput proves NewVTScreenSized itself enforces
// the same bounds MeasureSize's result is supposed to already satisfy -
// belt and braces against a caller (or a future MeasureSize bug) passing
// something out of range.
func TestNewVTScreenSizedClampsInput(t *testing.T) {
	s := NewVTScreenSized(1, 1)
	s.Feed("\x1b[1;1H")
	render := s.Render()
	if lines := strings.Split(render, "\n"); len(lines) != vtScreenMinRows {
		t.Errorf("too-small size not clamped: got %d rows, want %d", len(lines), vtScreenMinRows)
	}
}

// TestVTScreenAttributeBitsMatchUpstream defensively proves the hardcoded
// vtAttrBold/vtAttrReverse/vtAttrUnderline constants (see their doc comment
// in vt_screen.go) actually correspond to real vt10x behaviour, by feeding
// the real SGR sequences for each and checking the rendered output reflects
// them - rather than trusting the hardcoded bit values were transcribed
// correctly, or that they'll never change upstream.
func TestVTScreenAttributeBitsMatchUpstream(t *testing.T) {
	s := NewVTScreen()
	s.Feed("\x1b[1mB\x1b[0m\x1b[4mU\x1b[0m\x1b[7mR\x1b[0mN")
	render := s.Render()

	bold := vtCellAt(t, render, 0, 0)
	if bold.ch != 'B' || !strings.Contains(bold.tag, "b") {
		t.Errorf("bold (SGR 1) not reflected: %+v", bold)
	}
	underline := vtCellAt(t, render, 0, 1)
	if underline.ch != 'U' || !strings.Contains(underline.tag, "u") {
		t.Errorf("underline (SGR 4) not reflected: %+v", underline)
	}
	reverse := vtCellAt(t, render, 0, 2)
	if reverse.ch != 'R' || !strings.Contains(reverse.tag, "r") {
		t.Errorf("reverse (SGR 7) not reflected: %+v", reverse)
	}
	plain := vtCellAt(t, render, 0, 3)
	_, _, attrs := splitTag(plain.tag)
	if plain.ch != 'N' || attrs != "-" {
		t.Errorf("SGR 0 did not clear attributes: %+v", plain)
	}
}

func TestVTScreenBasicFeedAndColor(t *testing.T) {
	s := NewVTScreen()
	s.Feed("\x1b[31mred\x1b[0m plain")
	render := s.Render()

	r := vtCellAt(t, render, 0, 0)
	fg, _, _ := splitTag(r.tag)
	if r.ch != 'r' || fg != ansi16Hex[1] {
		t.Errorf("red 'r' cell: %+v, want fg %s", r, ansi16Hex[1])
	}
	p := vtCellAt(t, render, 0, 4)
	if p.ch != 'p' {
		t.Fatalf("expected 'p' (start of \"plain\") at col 4, got %+v", p)
	}
	pfg, _, _ := splitTag(p.tag)
	if pfg != "-" {
		t.Errorf("plain text should have default fg, got %s", pfg)
	}
}

func TestVTScreenTruecolor(t *testing.T) {
	s := NewVTScreen()
	s.Feed("\x1b[38;2;10;20;30mX")
	c := vtCellAt(t, s.Render(), 0, 0)
	fg, _, _ := splitTag(c.tag)
	if fg != rgbHex(10, 20, 30) {
		t.Errorf("truecolor fg = %s, want %s", fg, rgbHex(10, 20, 30))
	}
}

func TestVTScreen256Color(t *testing.T) {
	s := NewVTScreen()
	s.Feed("\x1b[38;5;200mX")
	c := vtCellAt(t, s.Render(), 0, 0)
	fg, _, _ := splitTag(c.tag)
	if fg != xterm256Hex(200) {
		t.Errorf("256-color fg = %s, want %s", fg, xterm256Hex(200))
	}
}

func TestVTScreenCursorVisibility(t *testing.T) {
	s := NewVTScreen()
	s.Feed("ab")
	if !strings.Contains(vtCellAt(t, s.Render(), 0, 2).tag, "r") {
		t.Fatal("cursor not shown at (0,2) after printing two characters")
	}

	s.Feed("\x1b[?25l")
	for _, c := range renderedLines(t, s.Render())[0] {
		if strings.Contains(c.tag, "r") {
			t.Errorf("cursor rendered while hidden: %+v", c)
		}
	}

	s.Feed("\x1b[?25h")
	if !strings.Contains(vtCellAt(t, s.Render(), 0, 2).tag, "r") {
		t.Error("cursor not rendered after being shown again")
	}
}

// TestVTScreenReverseCursorTogglesNotDoubles is the case DisplayBuffer's
// simpler append-only reverseTag couldn't handle correctly (it never needed
// to, since the old hand-rolled parser had no SGR 7 support at all): a cell
// that is already reverse-video in its own right, with the cursor also
// resting on it, should read as normal - a real terminal's cursor XORs with
// what's beneath it, so reverse-on-reverse cancels out.
func TestVTScreenReverseCursorTogglesNotDoubles(t *testing.T) {
	s := NewVTScreen()
	s.Feed("\x1b[7mR")  // one reverse-video cell
	s.Feed("\x1b[1;1H") // park the cursor on top of it
	c := vtCellAt(t, s.Render(), 0, 0)
	if strings.Contains(c.tag, "r") {
		t.Errorf("reverse-on-reverse should cancel out, got %+v", c)
	}
}

func TestVTScreenScrollRegion(t *testing.T) {
	// DECSTBM (scroll regions) is exactly the category of thing our old
	// hand-rolled parser explicitly didn't implement; vt10x does.
	s := NewVTScreen()
	s.Feed("\x1b[1;3r") // scroll region: rows 1-3
	s.Feed("one\r\ntwo\r\nthree")
	s.Feed("\r\nfour") // scrolls within the region
	render := renderedLines(t, s.Render())
	var text []string
	for _, row := range render[:4] {
		var b strings.Builder
		for _, c := range row {
			b.WriteRune(c.ch)
		}
		text = append(text, strings.TrimRight(b.String(), " "))
	}
	if text[0] != "two" || text[1] != "three" || text[2] != "four" {
		t.Errorf("scroll region did not scroll as expected: %v", text)
	}
}

func TestVTScreenIsBlank(t *testing.T) {
	s := NewVTScreen()
	if !s.IsBlank() {
		t.Fatal("a fresh screen should be blank")
	}
	s.Feed("\x1b[5;5H") // move the cursor only; write nothing
	if !s.IsBlank() {
		t.Error("moving the cursor made an empty screen report non-blank")
	}
	s.Feed("x")
	if s.IsBlank() {
		t.Error("screen with visible content reported blank")
	}
}

// TestVTScreenVimDownArrowRegression is the same regression proof as
// DisplayBuffer's (lib/frame_parser_test.go), replayed through VTScreen:
// the exact bytes captured from a real recording of pressing the down arrow
// while viewing a file in vim. vim redraws nothing in the buffer area for
// this - only its ruler digit and the cursor position change - so the row
// the cursor moves onto must visibly differ before and after.
func TestVTScreenVimDownArrowRegression(t *testing.T) {
	s := NewVTScreen()
	s.Feed("\x1b[H\x1b[2J\x1b[?25l\x1b[38;1H\"/etc/init.d/apache2\"")
	s.Feed("\x1b[1;1H\x1b[93m  1 \x1b[m\x1b[96m#!/bin/sh\x1b[m\r\n\x1b[93m  2 \x1b[m\x1b[96m### BEGIN\x1b[m")
	s.Feed("\x1b[?25l\x1b[?25h")

	s.Feed("\x1b[?25l\x1b[m\x1b[38;155H2\x1b[2;7H\x1b[?25h")
	afterFirst := s.Render()
	s.Feed("\x1b[?25l\x1b[38;155H3\x1b[3;7H\x1b[?25h")
	afterSecond := s.Render()

	if afterFirst == afterSecond {
		t.Fatal("two different down-arrow presses rendered identically")
	}
	for _, row := range []int{0, 1} {
		l1, l2 := renderedLines(t, afterFirst)[row], renderedLines(t, afterSecond)[row]
		var s1, s2 strings.Builder
		for _, c := range l1 {
			s1.WriteRune(c.ch)
		}
		for _, c := range l2 {
			s2.WriteRune(c.ch)
		}
		if strings.TrimRight(s1.String(), " ") != strings.TrimRight(s2.String(), " ") {
			t.Errorf("row %d text changed across a down-arrow press: %q -> %q", row, s1.String(), s2.String())
		}
	}
	if !strings.Contains(vtCellAt(t, afterFirst, 1, 6).tag, "r") {
		t.Error("cursor not shown at row 2 after the first down arrow")
	}
	if strings.Contains(vtCellAt(t, afterSecond, 1, 6).tag, "r") {
		t.Error("stale cursor still shown at row 2 after the second down arrow")
	}
	if !strings.Contains(vtCellAt(t, afterSecond, 2, 6).tag, "r") {
		t.Error("cursor not shown at row 3 after the second down arrow")
	}
}
