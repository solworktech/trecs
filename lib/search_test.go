package lib

import (
	"fmt"
	"strings"
	"testing"
)

func texts(ls []SearchLine) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Text)
	}
	return out
}

func TestLinesAreReadAsTheExportReadsThem(t *testing.T) {
	ls := linesOfFrames([]searchFrame{
		{data: "\x1b[32mone\x1b[0m\r\n", ts: 10, fi: 1, upto: 1},
		{data: "a\tb\r\n", ts: 20, fi: 2, upto: 2},
		{data: "loading 10%\rloading 100%\r\n", ts: 30, fi: 3, upto: 3},
		{data: "no newline at the end", ts: 40, fi: 4, upto: 4},
	}, 0)
	want := []string{"one", "a       b", "loading 100%", "no newline at the end"}
	if got := texts(ls); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if ls[0].TS != 10 || ls[0].FI != 1 || ls[1].Upto != 2 || ls[3].FI != 4 {
		t.Errorf("a line is placed at the frame that completed it: %+v", ls)
	}
}

func TestALineThatTakesFramesToWriteIsPlacedAtTheLastOne(t *testing.T) {
	ls := linesOfFrames([]searchFrame{{data: "par", ts: 1, fi: 1, upto: 1}, {data: "tial\r\n", ts: 5, fi: 2, upto: 2}}, 0)
	if len(ls) != 1 || ls[0].Text != "partial" || ls[0].TS != 5 || ls[0].Upto != 2 {
		t.Errorf("%+v", ls)
	}
}

func TestMultibyteTextSurvives(t *testing.T) {
	ls := linesOfFrames([]searchFrame{{data: "héllo → wörld\r\n", ts: 1, fi: 0, upto: 1}}, 0)
	if len(ls) != 1 || ls[0].Text != "héllo → wörld" {
		t.Errorf("%+v", ls)
	}
}

func cmd(input string, prompt int64, outs ...TerminalFrame) Command {
	c := Command{InputText: input, HasPrompt: true, PromptFrame: TerminalFrame{Timestamp: prompt, Data: "$ "}, StartTime: prompt}
	c.InputFrames = []TerminalFrame{{Timestamp: prompt + 1, Data: input + "\r\n"}}
	c.OutputFrames = outs
	return c
}

func TestIndexLeavesOutCommandsWithNoTextAndInteractiveOnes(t *testing.T) {
	a := cmd("ls", 0, TerminalFrame{Timestamp: 5, Data: "x\r\n"})
	empty := cmd("", 10)
	vim := cmd("vim f", 20, TerminalFrame{Timestamp: 25, Data: "\x1b[?1049hhello"})
	vim.OutputTextRaw = "\x1b[?1049hhello"
	idx := BuildSearchIndex([]Command{a, empty, vim}, nil)
	if len(idx) != 2 || idx[0].Idx != 0 || idx[1].Idx != 2 {
		t.Fatalf("%+v", idx)
	}
	if len(idx[1].Lines) != 0 {
		t.Errorf("a full-screen program has no lines: %+v", idx[1].Lines)
	}
}

func TestIndexFindsTheRecordingsFrame(t *testing.T) {
	frames := []TerminalFrame{
		{Timestamp: 0, Data: "$ "}, {Timestamp: 1, Data: "echo hi\r\n"},
		{Timestamp: 5, Data: "hi\r\n"}, {Timestamp: 5, Data: "there\r\n"},
	}
	c := cmd("echo hi", 0, frames[2], frames[3])
	c.InputFrames = []TerminalFrame{frames[1]}
	c.FirstRawFrameIndex, c.LastRawFrameIndex = 0, 3
	e := BuildSearchIndex([]Command{c}, frames)[0]
	if e.Input.FI != 1 || e.Lines[0].FI != 2 || e.Lines[1].FI != 3 {
		t.Errorf("frames at the same time are told apart by what they hold: %+v %+v", e.Input, e.Lines)
	}
	if e.Input.Upto != 2 || e.Lines[0].Upto != 3 || e.Lines[1].Upto != 4 {
		t.Errorf("upto: %+v %+v", e.Input, e.Lines)
	}
}

func TestTypedAheadOutputIsNotSearched(t *testing.T) {
	c := cmd("sleep 1", 0, TerminalFrame{Timestamp: 5, Data: "real\r\n"}, TerminalFrame{Timestamp: 6, Data: "ls typed ahead\r\n"})
	c.TypedAhead = []int{1}
	e := BuildSearchIndex([]Command{c}, nil)[0]
	if got := texts(e.Lines); len(got) != 1 || got[0] != "real" {
		t.Errorf("%q", got)
	}
}

func TestCompilePattern(t *testing.T) {
	cases := []struct {
		term         string
		regex, mcase bool
		match, miss  string
	}{
		{"hello", true, false, "say HELLO", ""},      // no capital: case does not count
		{"Hello", true, false, "say Hello", "hello"}, // a capital: it does
		{"hello", true, true, "hello", "HELLO"},      // match case
		{"a.c", false, false, "a.c", "abc"},          // plain text: the dot is a dot
		{"a.c", true, false, "abc", ""},
		{`(?i)HELLO`, true, true, "hello", ""},         // an inline flag wins
		{`foo\hbar`, true, false, "foo bar", "foobar"}, // PCRE's \h
		{`foo\Z`, true, false, "a foo", "foo b"},
		{`(?'n'ab)+`, true, false, "abab", ""},
		{`[]]x`, true, false, "]x", ""},
		{`\bword\b`, true, false, "a word here", "swords"},
	}
	for _, c := range cases {
		re, err := CompileSearchPattern(c.term, c.regex, c.mcase)
		if err != nil {
			t.Errorf("%q: %v", c.term, err)
			continue
		}
		if !re.MatchString(c.match) {
			t.Errorf("%q should match %q", c.term, c.match)
		}
		if c.miss != "" && re.MatchString(c.miss) {
			t.Errorf("%q should not match %q", c.term, c.miss)
		}
	}
}

func TestPatternsThatCannotBeHonouredAreRefusedWithAMessage(t *testing.T) {
	for _, term := range []string{`foo(?=bar)`, `(a)\1`, `a++b`, `(?<!x)yz1`, `[abc`, `(unclosed`} {
		_, err := CompileSearchPattern(term, true, false)
		if err == nil || !strings.HasPrefix(err.Error(), "Not a valid pattern: ") {
			t.Errorf("%q: %v", term, err)
		}
	}
	if _, err := CompileSearchPattern("ab", true, false); err == nil || !strings.Contains(err.Error(), "at least 3") {
		t.Errorf("too short: %v", err)
	}
	if _, err := CompileSearchPattern("   ", true, false); err == nil {
		t.Error("blanks are not a term")
	}
	if _, err := CompileSearchPattern("(?=x)", false, false); err != nil {
		t.Errorf("as text, it is only characters: %v", err)
	}
}

func pieces(ps []SearchPiece) string {
	var sb strings.Builder
	for _, p := range ps {
		if p.Hit {
			sb.WriteString("[" + p.T + "]")
		} else {
			sb.WriteString(p.T)
		}
	}
	return sb.String()
}

func TestHighlight(t *testing.T) {
	re, _ := CompileSearchPattern("err", true, false)
	if got := pieces(HighlightLine("an Error, an error", re)); got != "an [Err]or, an [err]or" {
		t.Errorf("%q", got)
	}
	if HighlightLine("fine", re) != nil {
		t.Error("no hit, no pieces")
	}
	empty, _ := CompileSearchPattern("x*y*z*", true, false)
	if HighlightLine("abc", empty) != nil {
		t.Error("an empty match is not a hit")
	}
	long := strings.Repeat("err ", 100)
	if n := len(HighlightLine(long, re)); n > 2*maxSearchPieces+1 {
		t.Errorf("%d pieces", n)
	}
}

func TestRunSearchGivesContextAndMergesNeighbours(t *testing.T) {
	var outs []TerminalFrame
	for i, l := range []string{"a1", "needle one", "b1", "c1", "d1", "e1", "needle two", "f1"} {
		outs = append(outs, TerminalFrame{Timestamp: int64(10 + i), Data: l + "\r\n"})
	}
	idx := BuildSearchIndex([]Command{cmd("cat file", 0, outs...)}, nil)
	re, _ := CompileSearchPattern("needle", true, false)
	got := RunSearch(idx, re, 0)
	if got.MatchedLines != 2 || len(got.Results) != 1 || got.Truncated {
		t.Fatalf("%+v", got)
	}
	var shown []string
	for _, b := range got.Results[0].Blocks {
		var rows []string
		for _, r := range b.Rows {
			if r.Match {
				rows = append(rows, "*"+r.Text)
			} else {
				rows = append(rows, r.Text)
			}
		}
		shown = append(shown, strings.Join(rows, ","))
	}
	if want := []string{"a1,*needle one,b1", "e1,*needle two,f1"}; strings.Join(shown, ";") != strings.Join(want, ";") {
		t.Errorf("got %q, want %q", shown, want)
	}
}

func TestACommandsOwnTextIsSearchedButNeverContext(t *testing.T) {
	c := cmd("grep needle file", 0, TerminalFrame{Timestamp: 5, Data: "x needle\r\n"}, TerminalFrame{Timestamp: 6, Data: "y\r\n"})
	idx := BuildSearchIndex([]Command{c}, nil)
	re, _ := CompileSearchPattern("needle", true, false)
	got := RunSearch(idx, re, 0)
	rows := got.Results[0].Blocks[0].Rows
	if !rows[0].Input || !rows[0].Match || got.MatchedLines != 2 {
		t.Errorf("%+v", got)
	}
	only, _ := CompileSearchPattern("y\\b", true, false)
	g2 := RunSearch(idx, only, 0)
	for _, b := range g2.Results[0].Blocks {
		for _, r := range b.Rows {
			if r.Input {
				t.Errorf("the command's text is the heading, not context: %+v", r)
			}
		}
	}
}

func TestRunSearchStopsAtTheLimit(t *testing.T) {
	var outs []TerminalFrame
	for i := 0; i < 50; i++ {
		outs = append(outs, TerminalFrame{Timestamp: int64(10 + i), Data: "hit line\r\n"})
	}
	idx := BuildSearchIndex([]Command{cmd("one", 0, outs...), cmd("two", 100, outs...)}, nil)
	re, _ := CompileSearchPattern("hit", true, false)
	got := RunSearch(idx, re, 30)
	if got.MatchedLines != 30 || !got.Truncated {
		t.Errorf("%d lines, truncated %v", got.MatchedLines, got.Truncated)
	}
}

func frames(data ...string) []searchFrame {
	var out []searchFrame
	for i, d := range data {
		out = append(out, searchFrame{data: d, ts: int64(i * 10), fi: i, upto: i})
	}
	return out
}

func TestAPastedAddressDrawnAfterSkippedColumnsLeavesTheCommand(t *testing.T) {
	// what bash writes for "fetch <paste>": the address is written after a run of cursor-right over the text already there
	got := texts(linesOfFrames(frames(
		"$ ", "f", "e", "tch ", "\x1b[7mhttp://x/y.tgz\x1b[27m", "\r\x1b[8Chttp://x/y.tgz", "\r\n"), 0))
	if len(got) != 1 || got[0] != "$ fetch http://x/y.tgz" {
		t.Errorf("%q", got)
	}
}

func TestALineTheShellDrewAgainIsTheLineItEndedAs(t *testing.T) {
	got := texts(linesOfFrames(frames(
		"$ df -h | ./zaje -l df", "\r\x1b[K$ ./zaje /etc/rc.d/sshd", "\x1b[3D\x1b[P\x1b[P", "\r\n", "out\r\n"), 0))
	want := "$ ./zaje /etc/rc.d/sd"
	if len(got) != 2 || got[0] != want || got[1] != "out" {
		t.Errorf("%q", got)
	}
}

func TestInsertModeInTheMiddleOfALine(t *testing.T) {
	// bash on FreeBSD types in front of text with: insert mode, a space, insert mode off, back, the character
	got := texts(linesOfFrames(frames("./zaje /etc", "\r\x1b[2C", "\x1b[4h \x1b[4l\x08-", "\x1b[4h \x1b[4l\x08l", "\r\n"), 0))
	if len(got) != 1 || got[0] != "./-lzaje /etc" {
		t.Errorf("%q", got)
	}
}

func TestInsertAndEraseInALine(t *testing.T) {
	got := texts(linesOfFrames(frames("abcdef", "\x1b[4D\x1b[2@XY", "\x1b[2C\x1b[1X", "\r\n"), 0))
	if len(got) != 1 || got[0] != "abXYcd f" {
		t.Errorf("%q", got)
	}
}

func TestAProgramThatPaintsTheScreenLeavesNoLines(t *testing.T) {
	got := texts(linesOfFrames(frames(
		"before\r\n",
		"\x1b[H\x1b[2J\x1b[1;1HPID USER\x1b[2;1H  1 root\x1b[3;1H  2 jesse",
		"\x1b[24;1HF10Quit",
		"\x1b[H\x1b[2J\x1b[24;1H",
		"\r\n$ next\r\n"), 24))
	for _, l := range got {
		if strings.Contains(l, "PID") || strings.Contains(l, "root") || strings.Contains(l, "F10") {
			t.Errorf("a painting was taken for output: %q", got)
		}
	}
	if len(got) == 0 || got[0] != "before" || got[len(got)-1] != "$ next" {
		t.Errorf("%q", got)
	}
}

func TestTheAlternateScreenIsNotPartOfTheLog(t *testing.T) {
	got := texts(linesOfFrames(frames("a\r\n", "\x1b[?1049hmenu\r\nitems\r\n\x1b[?1049l", "b\r\n"), 24))
	if strings.Join(got, "|") != "a|b" {
		t.Errorf("%q", got)
	}
}

func TestATextFilmKeepsItsNarrationWhileThePictureAboveIsRedrawn(t *testing.T) {
	// A film made of text: the top of the screen is a title and a picture, drawn by address between the words and put back with
	// ESC 7 / ESC 8; below a rule is the scroll region, where the narration is typed and scrolls. What scrolls out of the region
	// is the log; the picture is a painting and is not.
	d := []string{"\x1b[2J\x1b[H\x1b[?25l", "\x1b[22;1H------", "\x1b[23;31r\x1b[31;1H$ ", "cat film", "\r\n"}
	var want []string
	want = append(want, "$ cat film")
	for i := 1; i <= 45; i++ { // more than the screen has rows
		d = append(d, fmt.Sprintf("\x1b7\x1b[3;40HWEST COAST %d\x1b8", i), fmt.Sprintf("narration line %d", i), "\x1b7\x1b[1;1Htitle\x1b[2;1H\x1b[2K\x1b8", "\r\n")
		want = append(want, fmt.Sprintf("narration line %d", i))
	}
	got := texts(linesOfFrames(frames(d...), 31))
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestTheScrollRegionIsWhereLinesScrollFrom(t *testing.T) {
	// the rows above the region stay, whatever scrolls in it; a region that is the whole screen is no region
	s := newScreenLog(5)
	s.run("\x1b[2;4r\x1b[2;1Ha\r\nb\r\nc\r\nd\r\ne", func(int) int { return 0 })
	text, _ := s.lines()
	if got := strings.Join(text, "|"); got != "a|b|c|d|e" {
		t.Errorf("%q", got)
	}
	if !s.region() {
		t.Errorf("rows 2 to 4 of 5 are a region")
	}
	s.run("\x1b[r", func(int) int { return 0 })
	if s.region() {
		t.Errorf("ESC [ r resets the region to the whole screen")
	}
}

func TestAScreenSetUpInThePromptsFrameIsKnownToTheLines(t *testing.T) {
	// A text film: its first frame clears the screen, draws a rule, sets the scroll region and puts the cursor on its last row, and
	// ends with the prompt; what the command prints is typed there and scrolls in the region, while the top is drawn by address.
	// The recording makes that first frame the prompt's. The command's lines are the narration, and the picture is not one.
	var out []TerminalFrame
	for i := 1; i <= 14; i++ {
		out = append(out,
			TerminalFrame{Timestamp: int64(100 + 10*i), Data: fmt.Sprintf("\x1b7\x1b[3;40Hpicture %d\x1b8", i)},
			TerminalFrame{Timestamp: int64(100 + 10*i), Data: fmt.Sprintf("narration %d about the coast", i)},
			TerminalFrame{Timestamp: int64(105 + 10*i), Data: "\r\n"})
	}
	c := cmd("cat film", 50, out...)
	c.HasPrompt = true
	c.PromptFrame = TerminalFrame{Timestamp: 0, Data: "\x1b[2J\x1b[H\x1b[22;1H------\x1b[23;31r\x1b[31;1H$ "}
	idx := BuildSearchIndexFor([]Command{c}, nil, 31)
	var want []string
	for i := 1; i <= 14; i++ {
		want = append(want, fmt.Sprintf("narration %d about the coast", i))
	}
	if len(idx) != 1 || strings.Join(texts(idx[0].Lines), "|") != strings.Join(want, "|") {
		t.Fatalf("%q", texts(idx[0].Lines))
	}
	re, _ := CompileSearchPattern("coast", true, false)
	if got := RunSearch(idx, re, 0).MatchedLines; got != 14 {
		t.Errorf("%d lines match", got)
	}
}
