package lib

import (
	"strings"
	"testing"
)

// A fish 3.1 recording (172 columns, marks sourced inside docker). Fish paints
// a multi-line grey history suggestion that wraps at the terminal width, then
// walks back up with ESC[A and clears with a bare ESC[J (erase below, not
// clear screen). The old reconstruction lost the prompt and fell back to the
// keystroke reader, which showed "(B" charset designations in the input.
func TestFishWrappedSuggestionInputReadsCleanly(t *testing.T) {
	cmds, err := ParseRecording("testdata/fish-wrapped-suggestion.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{4: "lsb_release", 5: "ps fax", 6: "lsb_release -a", 7: "lsb_release -r"}
	for i, w := range want {
		if i >= len(cmds) {
			t.Fatalf("only %d commands", len(cmds))
		}
		if got := cmds[i].InputText; got != w {
			t.Errorf("command %d input = %q, want %q", i, got, w)
		}
	}
}

func TestBareEraseBelowKeepsThePrompt(t *testing.T) {
	e := newLineEditorWidth(80)
	for _, r := range "$ ls -al" {
		e.put(r)
	}
	e.csi("", 'J')
	if got := strings.Join(e.logical(), ""); got != "$ ls -al" {
		t.Errorf("after ESC[J got %q", got)
	}
}

func TestKeystrokeReaderSkipsCharsetSelectionAndStringTerminators(t *testing.T) {
	// the line no longer starts with the prompt, so the keystrokes are followed: ESC ( B is not the text "(B"
	frames := []TerminalFrame{{Data: "\x1b[1mls\x1b(B\x1b[m -l\x1b]0;title\x1b\\"}}
	got := reconstructInputCols(frames, TerminalFrame{Data: "different> "}, "", 0)
	if got != "ls -l" {
		t.Errorf("got %q", got)
	}
}
