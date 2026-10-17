package lib

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// sixelOf is a graphic of w x h pixels, all red.
func sixelOf(w, h int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "0;1;0q\"1;1;%d;%d#1;2;100;0;0", w, h)
	for y := 0; y < h; y += 6 {
		fmt.Fprintf(&b, "#1!%d~-", w)
	}
	return b.String()
}

func dcs(w, h int) string { return "\x1bP" + sixelOf(w, h) + "\x1b\\" }

func newImgScreen(cols, rows int) *VTScreen {
	s := NewVTScreenSized(cols, rows)
	s.SetCellSize(8, 16)
	return s
}

var tviewTag = regexp.MustCompile(`\[[^\[\]]*\]`)

// lines are the rows as text: no colour tags, "[[" as "["
func lines(s *VTScreen) []string {
	out := strings.Split(s.Render(), "\n")
	for i, l := range out {
		out[i] = strings.ReplaceAll(tviewTag.ReplaceAllString(strings.ReplaceAll(l, "[[", "\x00"), ""), "\x00", "[")
	}
	return out
}

func TestSixelTakesItsCellsAndMovesTheCursorPastThem(t *testing.T) {
	s := newImgScreen(80, 24)
	s.Feed("ab" + dcs(16, 32) + "cd")
	ps := s.ImagePlacements()
	if len(ps) != 1 {
		t.Fatalf("%d placements", len(ps))
	}
	p := ps[0]
	if p.Col != 2 || p.Row != 0 || p.Cols != 2 || p.Rows != 2 || p.BoxCols != 2 || p.BoxRows != 2 || p.Col0 != 0 || p.Row0 != 0 {
		t.Errorf("placement %+v", p)
	}
	if p.PxW != 16 || p.PxH != 32 || !IsSixel(p.DCS) {
		t.Errorf("the picture: %dx%d", p.PxW, p.PxH)
	}
	l := lines(s)
	if len(l) < 3 || strings.Index(l[2], "cd") != 2 {
		t.Errorf("the text after the picture is below it, in its column: %q", l)
	}
	if strings.Contains(s.Render(), string(imgBase)) {
		t.Error("a placeholder leaked into the text")
	}
	if s.IsBlank() {
		t.Error("a picture is not nothing")
	}
}

func TestSixelRoundsUpToWholeCellsAndUsesTheRecordedCellSize(t *testing.T) {
	s := newImgScreen(80, 24)
	s.Feed(dcs(17, 17))
	if p := s.ImagePlacements()[0]; p.Cols != 3 || p.Rows != 2 {
		t.Errorf("17x17 px at 8x16: %dx%d cells", p.Cols, p.Rows)
	}
	s = NewVTScreenSized(80, 24)
	s.SetCellSize(10, 20)
	s.Feed(dcs(100, 100))
	if p := s.ImagePlacements()[0]; p.Cols != 10 || p.Rows != 5 {
		t.Errorf("100x100 px at 10x20: %dx%d cells", p.Cols, p.Rows)
	}
	s = NewVTScreenSized(80, 24) // nothing said: the default
	s.Feed(dcs(80, 160))
	if p := s.ImagePlacements()[0]; p.Cols != 80/DefaultCellWidth || p.Rows != 160/DefaultCellHeight {
		t.Errorf("default: %dx%d", p.Cols, p.Rows)
	}
	s.SetCellSize(0, 5) // not a size: ignored
	if w, h := s.cellSize(); w != DefaultCellWidth || h != DefaultCellHeight {
		t.Errorf("%d %d", w, h)
	}
}

func TestSixelScrollsWithTheText(t *testing.T) {
	s := newImgScreen(80, 24)
	s.Feed(dcs(16, 48)) // 2 x 3 cells
	s.Feed(strings.Repeat("x\r\n", 21))
	// the cursor was on row 3; 21 lines later it is at the bottom and has scrolled by one: the picture's first row went off the top
	ps := s.ImagePlacements()
	if len(ps) != 1 || ps[0].Row != 0 || ps[0].Row0 != 1 || ps[0].Rows != 2 {
		t.Fatalf("after scrolling one row off: %+v", ps)
	}
	s.Feed(strings.Repeat("y\r\n", 5))
	if ps := s.ImagePlacements(); len(ps) != 0 {
		t.Errorf("scrolled off altogether: %+v", ps)
	}
}

func TestSixelAtTheBottomScrollsTheScreen(t *testing.T) {
	s := newImgScreen(80, 24)
	s.Feed(strings.Repeat("l\r\n", 22) + dcs(8, 64)) // 4 rows from row 22 of 24: two too many, and the line after it one more
	ps := s.ImagePlacements()
	if len(ps) != 1 || ps[0].Rows != 4 || ps[0].Row != 19 {
		t.Fatalf("%+v", ps)
	}
}

func TestSixelWrittenOverIsSplit(t *testing.T) {
	s := newImgScreen(80, 24)
	s.Feed(dcs(32, 48))  // 4 x 3 cells at the top left
	s.Feed("\x1b[2;2HZ") // a character in the middle of the second row
	ps := s.ImagePlacements()
	// the picture is now: the first row whole, the second row in two pieces (1 cell, 2 cells), the third row whole;
	// pieces that line up (the first and third columns... ) are separate rectangles
	cells := 0
	for _, p := range ps {
		cells += p.Cols * p.Rows
		if p.Col <= 1 && p.Row <= 1 && p.Col+p.Cols > 1 && p.Row+p.Rows > 1 {
			t.Errorf("the overwritten cell is still part of a picture: %+v", p)
		}
	}
	if cells != 4*3-1 {
		t.Errorf("%d cells of the 12 left, want 11: %+v", cells, ps)
	}
	// each piece knows where in the picture it begins
	for _, p := range ps {
		if p.Col-p.Col0 != 0 || p.Row-p.Row0 != 0 {
			t.Errorf("the picture's origin moved: %+v", p)
		}
	}
	// a cleared line takes its row of the picture with it
	s.Feed("\x1b[1;1H\x1b[2K")
	cells = 0
	for _, p := range s.ImagePlacements() {
		cells += p.Cols * p.Rows
	}
	if cells != 4*2-1 {
		t.Errorf("after erasing the first row: %d cells", cells)
	}
}

func TestSixelCutAcrossFramesAndOtherDeviceControlStrings(t *testing.T) {
	var s *VTScreen
	whole := "hi" + dcs(16, 16) + "!"
	for _, cut := range []int{1, 3, 5, 9, 20, len(whole) - 3, len(whole) - 1} {
		s = newImgScreen(80, 24)
		s.Feed(whole[:cut])
		s.Feed(whole[cut:])
		ps := s.ImagePlacements()
		if len(ps) != 1 || ps[0].Cols != 2 || ps[0].Rows != 1 {
			t.Errorf("cut at %d: %+v", cut, ps)
		}
		if !strings.Contains(s.Render(), "!") {
			t.Errorf("cut at %d: the text after is lost: %q", cut, s.Render())
		}
	}
	// a device control string that is no graphic is not drawn, and not printed
	s = newImgScreen(80, 24)
	s.Feed("a\x1bP$qm\x1b\\b")
	if got := strings.TrimRight(lines(s)[0], " "); got != "ab" {
		t.Errorf("%q", got)
	}
	if len(s.ImagePlacements()) != 0 {
		t.Error("a placement from nothing")
	}
}

func TestSixelThatIsNotValidOrTooBigLeavesTheScreenAlone(t *testing.T) {
	s := newImgScreen(80, 24)
	s.Feed("a\x1bP0;1;0q!99999~\x1b\\b")
	if got := strings.TrimRight(lines(s)[0], " "); got != "ab" {
		t.Errorf("%q", got)
	}
}

func TestSixelBoxIsLimitedToTheScreenAndTo63(t *testing.T) {
	s := newImgScreen(80, 24)
	s.Feed("\x1b[1;75H" + dcs(800, 16)) // 100 cells wide, from column 75 of 80
	if p := s.ImagePlacements()[0]; p.Col != 74 || p.Cols != 6 {
		t.Errorf("%+v", p)
	}
	s = newImgScreen(200, 150)
	s.Feed(dcs(2000, 1600))
	if p := s.ImagePlacements()[0]; p.Cols != 63 || p.Rows != 63 {
		t.Errorf("%dx%d", p.Cols, p.Rows)
	}
}

func TestTheSamePictureSharesAnIDAndFifteenDistinctOnesFit(t *testing.T) {
	s := newImgScreen(80, 100)
	for i := 0; i < 3; i++ {
		s.Feed(dcs(8, 16))
	}
	if n := len(s.ImagePlacements()); n != 3 {
		t.Fatalf("three placements, got %d", n)
	}
	ids := map[int]bool{}
	for _, p := range s.ImagePlacements() {
		ids[p.imgID] = true
	}
	if len(ids) != 1 {
		t.Errorf("the same picture on %d ids", len(ids))
	}
	s = newImgScreen(80, 100)
	for i := 0; rune(i) < imgIDs; i++ {
		s.Feed(dcs(8+i, 16))
	}
	if n := len(s.ImagePlacements()); n != int(imgIDs) {
		t.Errorf("%d of %d distinct pictures", n, imgIDs)
	}
	s.Feed(dcs(100, 16)) // one more than there are ids: the oldest's place is taken, the screen does not break
	if n := len(s.ImagePlacements()); n < int(imgIDs) {
		t.Errorf("%d", n)
	}
}

func TestImageFillShowsWhereAPictureIs(t *testing.T) {
	s := newImgScreen(80, 24)
	s.SetImageFill('░')
	s.Feed("x" + dcs(16, 16) + "y")
	l := lines(s)
	if !strings.Contains(l[0], "x░░") || !strings.Contains(l[1], "y") {
		t.Errorf("%q", l)
	}
}

func TestARecordingWithAGraphicLoadsAndKeepsTheCellSize(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	// one frame of about 1 MB: far over a line reader's usual limit
	big := "\x1bP0;1;0q\"1;1;64;96#1;2;100;0;0" + strings.Repeat("#1!64~-", 16) + strings.Repeat("#2;2;1;2;3", 100000) + "\x1b\\"
	frame, _ := json.Marshal(TerminalFrame{Timestamp: 5, Data: "$ show\r\n" + big + "done\r\n"})
	content := `{"type":"meta","width":100,"height":30,"cell_width":9,"cell_height":19}` + "\n" + string(frame) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	frames, _, meta, err := LoadFrames(path)
	if err != nil {
		t.Fatalf("a long line: %v", err)
	}
	if meta.CellWidth != 9 || meta.CellHeight != 19 || len(frames) != 1 {
		t.Fatalf("meta %+v, %d frames", meta, len(frames))
	}
	s := NewVTScreenSized(meta.Width, meta.Height)
	s.SetCellSize(meta.CellWidth, meta.CellHeight)
	s.Feed(frames[0].Data)
	ps := s.ImagePlacements()
	if len(ps) != 1 || ps[0].Cols != 8 || ps[0].Rows != 6 { // 64/9 -> 8, 96/19 -> 6
		t.Errorf("%+v", ps)
	}

	// the player reads the same file
	tp := &TerminalPlayerImpl{}
	if f, err := os.Open(path); err == nil {
		tp.file = f
		if err := tp.loadFrames(); err != nil || len(tp.frames) != 1 {
			t.Errorf("player: %v, %d frames", err, len(tp.frames))
		}
		_ = f.Close()
	}

	// merging keeps the size of a cell
	out := dir + "/merged.jsonl"
	if err := MergeRecordings([]string{path, path}, out, DefaultMergeGapMs, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, _, m, err := LoadFrames(out); err != nil || m.CellWidth != 9 || m.CellHeight != 19 {
		t.Errorf("merged: %+v %v", m, err)
	}
}

func TestTheTextOfACommandHasNoGraphicInIt(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	var b strings.Builder
	for i, d := range []string{"$ ", "show\r\n", dcs(16, 16) + "visible text\r\n", "$ "} {
		f, _ := json.Marshal(TerminalFrame{Timestamp: int64(i * 100), Data: d})
		b.Write(f)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if strings.Contains(c.OutputText, "#1") || strings.Contains(c.OutputText, "0;1;0q") {
			t.Errorf("graphic data in the text: %q", c.OutputText)
		}
	}
}
