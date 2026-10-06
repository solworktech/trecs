package lib

import (
	"reflect"
	"strings"
	"testing"
)

// Every kind of escape sequence is skipped whole - not only CSI and OSC. fish
// sends ESC = and ESC > around every prompt, which used to leave "=" and ">" in
// the text; CSI also ends on bytes that aren't letters (ESC[200~).
func TestStripEscapeSequencesSkipsEveryForm(t *testing.T) {
	for in, want := range map[string]string{
		"a\x1b=b": "ab", "a\x1b>b": "ab", "a\x1b7b\x1b8c": "abc", "a\x1bMb": "ab", "a\x1b(Bb": "ab",
		"a\x1b[>4;1mb": "ab", "a\x1b[?ub": "ab", "a\x1b[>0qb": "ab", "a\x1b[0cb": "ab", "a\x1b[<1ub": "ab",
		"a\x1b[200~b": "ab", "a\x1b[3~b": "ab", "a\x1b[@b": "ab", "a\x1b[18Cb": "ab", "a\x1b[?1049hb": "ab",
		"a\x1b]7;file://h/p\x07b": "ab", "a\x1b]133;C;cmdline_url=ls\x07b": "ab", "a\x1b]0;t\x1b\\b": "ab",
		"a\x1bP+q\x1b\\b": "ab", "keep\r\nthis": "keep\r\nthis", "a\x1b": "a",
	} {
		if got := stripEscapeSequences(in); got != want {
			t.Errorf("stripEscapeSequences(%q) = %q, want %q", in, got, want)
		}
	}
}

// Recorded with fish 4.1 by `recorder record -commands-file`. fish marks A, C
// (with the command line itself) and D on its own, but never B. The commands
// come from the shell's own report, so the multi-line one is exact; the output
// has none of fish's "fresh line" padding (a marker, a screenful of spaces, a CR).
func TestFishRecordingsAreReadFromTheShellsOwnMarks(t *testing.T) {
	want := []string{"ls /etc/hostname", "echo hello world", "printf '%s\\n' one two", "echo \"multi\nline\"", "true", "echo done"}
	for _, file := range []string{"fish-native.jsonl", "fish-marked.jsonl"} {
		cmds, err := ParseRecording("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if got := inputTexts(cmds); !reflect.DeepEqual(got, want) {
			t.Errorf("%s commands:\n got  %q\n want %q", file, got, want)
			continue
		}
		wantOut := []string{"/etc/hostname", "hello world", "one\ntwo", "multi\nline", "", "done"}
		for i, c := range cmds {
			if c.OutputText != wantOut[i] {
				t.Errorf("%s: output of %q = %q, want %q", file, c.InputText, c.OutputText, wantOut[i])
			}
			if strings.ContainsAny(c.OutputText, "\r⏎=") {
				t.Errorf("%s: output of %q still has junk: %q", file, c.InputText, c.OutputText)
			}
		}
	}
}

// An earlier build of the recorder added its own marks on top of fish 4's, so the
// recordings it made have each one twice (A A B C C D D).
func TestFishRecordingWithEveryMarkTwice(t *testing.T) {
	cmds, err := ParseRecording("testdata/fish-doubled-marks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got := inputTexts(cmds); len(got) < 3 || !reflect.DeepEqual(got[:3], []string{"ls /etc/hostname", "echo hello world", "printf '%s\\n' one two"}) {
		t.Errorf("commands: %q", got)
	}
	for _, c := range cmds[:3] {
		if strings.Contains(c.InputText, "root@") || strings.ContainsAny(c.OutputText, "\r⏎=") {
			t.Errorf("prompt or junk leaked: %q / %q", c.InputText, c.OutputText)
		}
	}
}

// The editor tells an edited command from an untouched one by re-deriving its text
// from its frames (syncCommandFrames). With the command line taken from a mark, an
// untouched command must still look untouched - and an edited one must not be
// brought back to life from the mark when the saved file is read again.
func TestSyncKeepsMarkedCommandsAndTheirEdits(t *testing.T) {
	cmds, err := ParseRecording("testdata/fish-marked.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	untouched := cmds[1]
	frames := append(append([]TerminalFrame{}, untouched.InputFrames...), untouched.OutputFrames...)
	syncCommandFrames(&untouched)
	if got := append(append([]TerminalFrame{}, untouched.InputFrames...), untouched.OutputFrames...); !reflect.DeepEqual(got, frames) {
		t.Error("syncing an untouched command must not rewrite its frames")
	}

	edited := cmds[1]
	edited.InputText = "echo goodbye"
	syncCommandFrames(&edited)
	if got := commandInputText(&edited); got != "echo goodbye" {
		t.Errorf("after the edit the command reads %q: the old command line in the mark came back", got)
	}
}
