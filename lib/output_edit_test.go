package lib

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEditedRawKeepsTheColoursOfWhatWasNotTouched(t *testing.T) {
	raw := "\x1b[31mred\x1b[0m plain\r\n\x1b[1;34mdir\x1b[0m/\r\n"
	plain := CleanOutput(raw) // "red plain\ndir/"
	for _, tc := range []struct{ name, edited, want string }{
		{"nothing changed", plain, raw},
		{"a character replaced inside a colour takes its place, and its colour", "rod plain\ndir/", "\x1b[31mrod\x1b[0m plain\r\n\x1b[1;34mdir\x1b[0m/\r\n"},
		{"text added to the end of a coloured word is inside the colour", "reds plain\ndir/", "\x1b[31mreds\x1b[0m plain\r\n\x1b[1;34mdir\x1b[0m/\r\n"},
		{"text added in the plain part", "red plainer\ndir/", "\x1b[31mred\x1b[0m plainer\r\n\x1b[1;34mdir\x1b[0m/\r\n"},
		{"a line added at the end", plain + "\nmore", "\x1b[31mred\x1b[0m plain\r\n\x1b[1;34mdir\x1b[0m/\r\nmore\r\n"},
		{"a line added at the start", "first\n" + plain, "first\r\n" + raw},
		{"a line deleted", "red plain", "\x1b[31mred\x1b[0m plain\x1b[1;34m\x1b[0m\r\n"},
	} {
		got, ok := editedRaw(raw, plain, tc.edited)
		if !ok || got != tc.want {
			t.Errorf("%s:\n got  %q (%v)\n want %q", tc.name, got, ok, tc.want)
		}
	}

	// the colour of a word that goes is not left on what follows it
	got, ok := editedRaw("\x1b[31mred\x1b[0m plain\r\n", "red plain", " plain")
	if !ok || got != "\x1b[31m\x1b[0m plain\r\n" || CleanOutput(got) != " plain" {
		t.Errorf("deleting a coloured word: %q (%v)", got, ok)
	}
	// characters that are not ASCII
	if got, ok := editedRaw("\x1b[32m\u00e9\x1b[0mx\r\n", "\u00e9x", "\u00e9y"); !ok || got != "\x1b[32m\u00e9\x1b[0my\r\n" {
		t.Errorf("non-ASCII: %q (%v)", got, ok)
	}
}

// What cannot be lined up with the text it was cleaned to is not guessed at: the caller writes the plain text.
func TestEditedRawGivesUpRatherThanGuess(t *testing.T) {
	tabbed := "a\tb\r\n" // cleaned, the tab is spaces
	if _, ok := editedRaw(tabbed, CleanOutput(tabbed), "a       c"); ok {
		t.Error("tabs are expanded when the output is cleaned: where an edit belongs cannot be told")
	}
	if _, ok := editedRaw("abc", "abd", "abx"); ok {
		t.Error("text that is not what the raw output cleans to")
	}
}

// A long output is not compared character by character: one changed character in thousands of lines is quick,
// and every other line keeps its colour.
func TestEditedRawOfALongOutput(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "\x1b[32mline %d\x1b[0m\r\n", i)
	}
	raw := sb.String()
	plain := CleanOutput(raw)
	edited := strings.Replace(plain, "line 2500", "line 25x0", 1)
	start := time.Now()
	got, ok := editedRaw(raw, plain, edited)
	if !ok || strings.Count(got, "\x1b[32m") != 5000 || !strings.Contains(got, "\x1b[32mline 25x0\x1b[0m") {
		t.Fatalf("ok=%v, colours kept: %d of 5000", ok, strings.Count(got, "\x1b[32m"))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

// The recording of the user's report, saved after the output of its first command was edited: the colours of ls
// are still there. (They are the output's only escape sequences, apart from the shell's.)
func TestSavingAnEditedOutputKeepsItsColours(t *testing.T) {
	path := copyToTemp(t, editFixture)
	count := func(p string) int {
		frames, _, _, err := LoadFrames(p)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, f := range frames {
			n += strings.Count(f.Data, "\x1b[01;34m")
		}
		return n
	}
	was := count(path)
	if was == 0 {
		t.Fatal("the fixture has no coloured output")
	}
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	cmds[0].OutputText = strings.Replace(cmds[0].OutputText, "total 11232", "total 11233", 1)
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	if now := count(path); now != was {
		t.Errorf("blue (directories) in the recording: %d, there were %d", now, was)
	}
	after, err := ParseRecording(path)
	if err != nil || !strings.HasPrefix(after[0].OutputText, "total 11233") {
		t.Fatalf("%v: %q", err, after[0].OutputText[:20])
	}
	if after[0].OutputText != cmds[0].OutputText {
		t.Errorf("the output reads back differently from what was saved:\n got  %.120q\n want %.120q", after[0].OutputText, cmds[0].OutputText)
	}
}
