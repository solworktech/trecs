package lib

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// renderedCells splits Render()'s output back into (tag, rune) pairs per
// line, so tests can assert on specific cell content without hand-parsing
// tview markup themselves.
type renderedCell struct {
	tag string
	ch  rune
}

func renderedLines(t *testing.T, markup string) [][]renderedCell {
	t.Helper()
	var lines [][]renderedCell
	for _, line := range strings.Split(markup, "\n") {
		var cells []renderedCell
		tag := "-:-:-"
		i := 0
		for i < len(line) {
			if line[i] == '[' {
				if i+1 < len(line) && line[i+1] == '[' {
					cells = append(cells, renderedCell{tag: tag, ch: '['})
					i += 2
					continue
				}
				end := strings.IndexByte(line[i:], ']')
				if end < 0 {
					t.Fatalf("unterminated tag in %q", line)
				}
				tag = line[i+1 : i+end]
				i += end + 1
				continue
			}
			r := []rune(line[i:])[0]
			cells = append(cells, renderedCell{tag: tag, ch: r})
			i += len(string(r))
		}
		lines = append(lines, cells)
	}
	return lines
}

func cellAtLine(t *testing.T, markup string, row, col int) renderedCell {
	t.Helper()
	lines := renderedLines(t, markup)
	if row >= len(lines) || col >= len(lines[row]) {
		t.Fatalf("(%d,%d) out of range in rendered output %q", row, col, markup)
	}
	return lines[row][col]
}

func TestDisplayBufferCursorVisibleByDefault(t *testing.T) {
	db := NewDisplayBuffer()
	db.Feed("x")
	// cursor started at (0,0); after printing 'x' it's now at (0,1), a blank
	// past the printed content - the cursor cell, not the 'x', should carry
	// the reverse tag.
	c := cellAtLine(t, db.Render(), 0, 1)
	if c.ch != ' ' || !strings.Contains(c.tag, "r") {
		t.Errorf("cursor cell = %+v, want a reversed blank", c)
	}
	x := cellAtLine(t, db.Render(), 0, 0)
	if x.ch != 'x' || strings.Contains(x.tag, "r") {
		t.Errorf("printed cell = %+v, want plain 'x'", x)
	}
}

func TestDisplayBufferDECTCEMHidesAndShowsCursor(t *testing.T) {
	db := NewDisplayBuffer()
	db.Feed("\x1b[1;1H") // establish row 0, as any real frame stream would before toggling DECTCEM
	if !strings.Contains(cellAtLine(t, db.Render(), 0, 0).tag, "r") {
		t.Fatal("expected the cursor visible at (0,0) initially is wrong in this test setup")
	}

	db.Feed("\x1b[?25l") // hide
	for _, c := range renderedLines(t, db.Render())[0] {
		if strings.Contains(c.tag, "r") {
			t.Errorf("cursor rendered while hidden: %+v", c)
		}
	}

	db.Feed("\x1b[?25h") // show again
	if !strings.Contains(cellAtLine(t, db.Render(), 0, 0).tag, "r") {
		t.Error("cursor not rendered after being shown again")
	}
}

func TestDisplayBufferCursorMovesWithoutContentChange(t *testing.T) {
	// This is the exact shape of the bug: a full-screen program repositions
	// the cursor with a bare CUP sequence and writes no characters at all.
	// Real terminal content is therefore identical at both positions except
	// for where the visible cursor itself is drawn.
	db := NewDisplayBuffer()
	db.Feed("\x1b[1;1Hline one\r\n\x1b[93mline two\x1b[m\r\nline three")

	db.Feed("\x1b[1;1H") // cursor to row 1 col 1, nothing drawn
	before := db.Render()
	if !strings.Contains(cellAtLine(t, before, 0, 0).tag, "r") {
		t.Fatal("cursor not shown at (0,0)")
	}

	db.Feed("\x1b[2;1H") // cursor to row 2 col 1 - vim's down-arrow shape
	after := db.Render()

	if before == after {
		t.Fatal("moving the cursor produced no visible change at all - this is the reported bug")
	}
	if strings.Contains(cellAtLine(t, after, 0, 0).tag, "r") {
		t.Error("old cursor position is still shown as reversed")
	}
	if !strings.Contains(cellAtLine(t, after, 1, 0).tag, "r") {
		t.Error("new cursor position is not shown as reversed")
	}
	// the actual text content must be completely unaffected by the cursor
	// moving over it - only its tag changes, never the character
	if cellAtLine(t, after, 1, 0).ch != 'l' {
		t.Errorf("text under the cursor was altered: %+v", cellAtLine(t, after, 1, 0))
	}
}

func TestDisplayBufferCursorOverColoredAndBoldText(t *testing.T) {
	db := NewDisplayBuffer()
	db.Feed("\x1b[1;1H\x1b[1m\x1b[93mBOLD\x1b[m")
	db.Feed("\x1b[1;1H") // park the cursor on the bold, coloured 'B'

	c := cellAtLine(t, db.Render(), 0, 0)
	if c.ch != 'B' {
		t.Fatalf("expected 'B' under the cursor, got %+v", c)
	}
	if !strings.Contains(c.tag, "r") {
		t.Errorf("cursor not reversed over styled text: %+v", c)
	}
	// the underlying style must survive, not just be replaced by plain reverse
	fg, _, attrs := splitTag(c.tag)
	if fg == "-" {
		t.Errorf("foreground colour lost under the cursor: %+v", c)
	}
	if !strings.Contains(attrs, "b") {
		t.Errorf("bold lost under the cursor: %+v", c)
	}
}

func TestDisplayBufferCursorPastRowContent(t *testing.T) {
	db := NewDisplayBuffer()
	db.Feed("\x1b[1;1Hhi") // a two-character row
	db.Feed("\x1b[1;10H")  // cursor well past the printed content, same row
	line := renderedLines(t, db.Render())[0]
	if len(line) < 10 {
		t.Fatalf("row trimmed away the cursor cell entirely: %v", line)
	}
	if !strings.Contains(line[9].tag, "r") {
		t.Errorf("cursor at col 10 not shown: %+v", line[9])
	}
	if line[9].ch != ' ' {
		t.Errorf("expected a blank cursor cell, got %+v", line[9])
	}
}

func TestDisplayBufferMultipleModesInOneSequence(t *testing.T) {
	// DECSET/DECRST can bundle several mode numbers into one sequence
	// (e.g. "?1;25h"); mode 25 must still be picked out correctly.
	db := NewDisplayBuffer()
	db.Feed("\x1b[1;1H") // establish row 0
	db.Feed("\x1b[?1;25l")
	for _, c := range renderedLines(t, db.Render())[0] {
		if strings.Contains(c.tag, "r") {
			t.Errorf("cursor rendered after a bundled hide: %+v", c)
		}
	}
	db.Feed("\x1b[?25;1h")
	if !strings.Contains(cellAtLine(t, db.Render(), 0, 0).tag, "r") {
		t.Error("cursor not shown after a bundled show")
	}
}

// TestDisplayBufferVimDownArrowRegression replays the exact bytes captured
// from a real recording of pressing the down arrow while viewing a file in
// vim (frames from an actual terminal.jsonl). vim redraws nothing in the
// buffer area for this - it only rewrites its bottom-right ruler digit and
// repositions the cursor - so before the cursorVisible/reverse-video fix,
// Render() before and after down-arrow were identical except for that one
// ruler character, which is exactly the reported bug ("the very last line
// does change, but the rest remain static").
func TestDisplayBufferVimDownArrowRegression(t *testing.T) {
	db := NewDisplayBuffer()

	// Initial paint: a full screen, two visible content lines and a ruler at
	// row 38 (trimmed down from the real 38-row capture to keep this
	// readable - the shape that matters is identical).
	db.Feed("\x1b[H\x1b[2J\x1b[?25l\x1b[38;1H\"/etc/init.d/apache2\"")
	db.Feed("\x1b[1;1H\x1b[93m  1 \x1b[m\x1b[96m#!/bin/sh\x1b[m\r\n\x1b[93m  2 \x1b[m\x1b[96m### BEGIN\x1b[m")
	db.Feed("\x1b[?25l\x1b[?25h")

	// Down arrow #1, byte-for-byte from the recording: hide cursor, reset
	// SGR, write "2" at the ruler (row 38, col 155), move the real cursor to
	// (row 2, col 7), show cursor. No other bytes at all.
	db.Feed("\x1b[?25l\x1b[m\x1b[38;155H2\x1b[2;7H\x1b[?25h")
	afterFirst := db.Render()

	// Down arrow #2: same shape, ruler now "3", cursor to (row 3, col 7).
	db.Feed("\x1b[?25l\x1b[38;155H3\x1b[3;7H\x1b[?25h")
	afterSecond := db.Render()

	if afterFirst == afterSecond {
		t.Fatal("two different down-arrow presses rendered identically")
	}

	// The buffer content lines (1 and 2, 0-indexed 0 and 1) must not have
	// been touched by either press - only the ruler row and the cursor's
	// own position may differ.
	for _, row := range []int{0, 1} {
		l1 := renderedLines(t, afterFirst)[row]
		l2 := renderedLines(t, afterSecond)[row]
		var s1, s2 strings.Builder
		for _, c := range l1 {
			s1.WriteRune(c.ch)
		}
		for _, c := range l2 {
			s2.WriteRune(c.ch)
		}
		if s1.String() != s2.String() {
			t.Errorf("row %d text changed across a down-arrow press: %q -> %q", row, s1.String(), s2.String())
		}
	}

	// And the actual point of the fix: the cursor itself visibly moved
	// between row 2 and row 3 (0-indexed 1 and 2), at column 7 (0-indexed 6).
	if !strings.Contains(cellAtLine(t, afterFirst, 1, 6).tag, "r") {
		t.Error("cursor not shown at row 2 after the first down arrow")
	}
	if strings.Contains(cellAtLine(t, afterSecond, 1, 6).tag, "r") {
		t.Error("stale cursor still shown at row 2 after the second down arrow")
	}
	if !strings.Contains(cellAtLine(t, afterSecond, 2, 6).tag, "r") {
		t.Error("cursor not shown at row 3 after the second down arrow")
	}
}

func TestDisplayBufferIsBlankIgnoresCursorOverlay(t *testing.T) {
	// The cursor overlay is a Render()-time decoration only; it must never
	// make an otherwise-empty screen look non-blank (IsBlank drives the
	// annotation auto-preview linger, which has nothing to do with cursor
	// position).
	db := NewDisplayBuffer()
	if !db.IsBlank() {
		t.Fatal("a fresh buffer should be blank")
	}
	db.Feed("\x1b[5;5H") // move the cursor around; write nothing
	if !db.IsBlank() {
		t.Error("moving the cursor made an empty buffer report non-blank")
	}
}

func TestReverseTagNeverProducesALeadingReset(t *testing.T) {
	// tview treats a leading '-' in the attributes field as "reset and stop"
	// - if reverseTag ever produced that, the reverse flag would silently be
	// dropped for every default-styled cell, which is the common case.
	cases := []string{"-:-:-", "red:-:-", "-:-:b", "#ff0000:#00ff00:b"}
	for _, tag := range cases {
		got := reverseTag(tag)
		_, _, attrs := splitTag(got)
		if strings.HasPrefix(attrs, "-") {
			t.Errorf("reverseTag(%q) = %q, attrs still starts with '-'", tag, got)
		}
		if !strings.Contains(attrs, "r") {
			t.Errorf("reverseTag(%q) = %q, missing 'r'", tag, got)
		}
	}
}

func TestLoadFramesParsesMetaLine(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	content := `{"type":"meta","width":120,"height":40}
{"timestamp":0,"data":"hello"}
{"timestamp":10,"data":"world"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	frames, annotations, meta, err := LoadFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Width != 120 || meta.Height != 40 {
		t.Errorf("meta = %+v, want {120 40}", meta)
	}
	if len(frames) != 2 || frames[0].Data != "hello" || frames[1].Data != "world" {
		t.Errorf("frames = %+v, meta line must not be counted as one", frames)
	}
	if len(annotations) != 0 {
		t.Errorf("annotations = %+v, want none", annotations)
	}
}

func TestLoadFramesWithoutMetaLineIsBackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	content := `{"timestamp":0,"data":"hello"}
{"timestamp":10,"data":"world"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	frames, _, meta, err := LoadFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Width != 0 || meta.Height != 0 {
		t.Errorf("meta = %+v, want the zero value for a recording with no meta line", meta)
	}
	if len(frames) != 2 {
		t.Errorf("frames = %+v, want 2", frames)
	}
}

func TestNewRecordingMetaLineRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(NewRecordingMetaLine(155, 38)); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(TerminalFrame{Timestamp: 0, Data: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	_, _, meta, err := LoadFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Width != 155 || meta.Height != 38 {
		t.Errorf("meta = %+v, want {155 38}", meta)
	}
}
