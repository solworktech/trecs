package lib

import (
	"regexp"
	"strings"
	"testing"
)

// Readline types a character in the middle of a line by putting the terminal in insert mode for it: ESC [ 4 h, a space, ESC [ 4 l,
// back, the character. Ctrl-R does the same at every key. A reader that overwrites instead of inserting loses a character each time
// ("feth" for fetch, "du-h" for du -h).

func plainRow(db *DisplayBuffer, row int) string {
	var b strings.Builder
	for c := range db.grid[row] {
		b.WriteRune(db.grid[row][c].ch)
	}
	return strings.TrimRight(b.String(), " ")
}

func TestInsertModeInTheMiddleOfALineInTheLineEditor(t *testing.T) {
	e := newLineEditor()
	e.write("feth\x1b[1D") // the cursor is on the h
	e.write("\x1b[4h \x1b[4l\x08c")
	if got := e.lines()[0]; got != "fetch" {
		t.Errorf("%q", got)
	}
}

func TestInsertModeInTheMiddleOfALineOnTheScreen(t *testing.T) {
	db := NewDisplayBuffer()
	db.Feed("du-h\x1b[2D\x1b[4h \x1b[4l\x08 ")
	if got := plainRow(db, 0); got != "du -h" {
		t.Errorf("%q", got)
	}
}

func TestInsertDeleteAndEraseCharactersOnTheScreen(t *testing.T) {
	db := NewDisplayBuffer()
	db.Feed("abcdef\x1b[4D\x1b[2@")
	if got := plainRow(db, 0); got != "ab  cdef" {
		t.Errorf("insert: %q", got)
	}
	db.Feed("\x1b[1P")
	if got := plainRow(db, 0); got != "ab cdef" {
		t.Errorf("delete: %q", got)
	}
	db.Feed("\x1b[3X")
	if got := plainRow(db, 0); got != "ab   ef" {
		t.Errorf("erase: %q", got)
	}
}

func TestReverseISearchRedrawKeepsTheWholeCommand(t *testing.T) {
	// the frames bash 5.2 wrote for Ctrl-R, "fet", Enter (a recording of FreeBSD over ssh): the match is drawn, the cursor goes back
	// over it with BS or CR and cursor-forward, the line is edited in insert mode, and then the prompt is drawn over the search prompt
	url := "https://github.com/solworktech/zaje/raw/refs/heads/master/install_zaje.sh"
	bs := func(n int) string { return strings.Repeat("\x08", n) }
	cuf := func(n int) string { return strings.Repeat("\x1b[C", n) }
	frames := []string{
		"\x1b[?2004h\x1b]133;A\a[root@freebsd ~]# \x1b]133;B\a",
		"\r(reverse-i-search)`': \x1b[K",
		"\x08\x08\x08f': fetch https://github.com/solworktech/zaje/raw/re\x1b[7mf\x1b[27ms/heads/master/install_zaje.sh" + bs(31),
		"\r" + cuf(20) + "\x1b[4h \x1b[4l\x08e': \x1b[7mfe\x1b[27mtch https://github.com/solworktech/zaje/raw/ref\r" + cuf(24),
		"\x08\x08\x08\x1b[4h \x1b[4l\x08t': \x1b[7mfet\x1b[27m\x08\x08\x08",
		"\r\x1b[7P",
		"\x1b]133;A\a[root@freebsd ~]# \x1b]133;B\afet\x08\x08\x08\r\n\x1b[?2004l\r",
	}
	e := newLineEditor()
	db := NewDisplayBuffer()
	for _, f := range frames {
		e.write(f)
		db.Feed(f)
	}
	want := "[root@freebsd ~]# fetch " + url
	if got := e.lines()[0]; got != want {
		t.Errorf("line editor: %q", got)
	}
	if got := plainRow(db, 0); got != want {
		t.Errorf("screen: %q", got)
	}
}

func TestAnEscapeSequenceEndingInAtSignDoesNotEatTheTextAfterIt(t *testing.T) {
	// the final byte of a CSI sequence is anything from @ to ~: "ESC [ 2 @" ends at the @, and what follows is text
	if got := stripEscapeSequences("ab\x1b[2@cd"); got != "abcd" {
		t.Errorf("stripEscapeSequences: %q", got)
	}
	if got := FilterForDisplay("ab\x1b[2@cd\x1b[1mx"); got != "abcd\x1b[1mx" {
		t.Errorf("FilterForDisplay: %q", got)
	}
}

var styleTagRe = regexp.MustCompile(`\[[^\[\]]*:[^\[\]]*:[^\[\]]*\]`)

func firstRowOfVT(s *VTScreen) string {
	row := strings.SplitN(styleTagRe.ReplaceAllString(s.Render(), ""), "\n", 2)[0]
	return strings.TrimRight(strings.ReplaceAll(row, "[[", "["), " ")
}

func TestInsertModeOnTheEmulatedScreen(t *testing.T) {
	// the emulator (vt10x) reads ESC [ 4 h and does not insert; the screen must
	s := NewVTScreenSized(80, 24)
	s.Feed("feth\x1b[1D\x1b[4h \x1b[4l\x08c")
	if got := firstRowOfVT(s); got != "fetch" {
		t.Errorf("%q", got)
	}
	// the mode lasts from one frame to the next, and a title is not text
	s = NewVTScreenSized(80, 24)
	s.Feed("ad\x1b[1D\x1b[4h")
	s.Feed("\x1b]0;a title\x07bc")
	s.Feed("\x1b[4lz")
	if got := firstRowOfVT(s); got != "abcz" {
		t.Errorf("%q", got)
	}
}

func TestReverseISearchOnTheEmulatedScreenShowsTheWholeCommand(t *testing.T) {
	url := "https://github.com/solworktech/zaje/raw/refs/heads/master/install_zaje.sh"
	bs := func(n int) string { return strings.Repeat("\x08", n) }
	cuf := func(n int) string { return strings.Repeat("\x1b[C", n) }
	s := NewVTScreenSized(172, 38)
	for _, f := range []string{
		"\x1b[?2004h\x1b]133;A\a[root@freebsd ~]# \x1b]133;B\a",
		"\r(reverse-i-search)`': \x1b[K",
		"\x08\x08\x08f': fetch https://github.com/solworktech/zaje/raw/re\x1b[7mf\x1b[27ms/heads/master/install_zaje.sh" + bs(31),
		"\r" + cuf(20) + "\x1b[4h \x1b[4l\x08e': \x1b[7mfe\x1b[27mtch https://github.com/solworktech/zaje/raw/ref\r" + cuf(24),
		"\x08\x08\x08\x1b[4h \x1b[4l\x08t': \x1b[7mfet\x1b[27m\x08\x08\x08",
		"\r\x1b[7P",
		"\x1b]133;A\a[root@freebsd ~]# \x1b]133;B\afet\x08\x08\x08\r\n\x1b[?2004l\r",
	} {
		s.Feed(f)
	}
	if got := firstRowOfVT(s); got != "[root@freebsd ~]# fetch "+url {
		t.Errorf("%q", got)
	}
}
