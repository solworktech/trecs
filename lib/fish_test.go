package lib

import (
	"os"
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

// The user's own fish recording: fish 4 marks A, C (with the command line) and D,
// not B. Read from those marks, the commands are exact and the output clean.
func TestRealFishRecordingIsReadFromItsMarks(t *testing.T) {
	cmds, err := ParseRecording("testdata/fish-kitty-keyboard.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ls -al", "uname -a", "lsb_release", "curl -4 ifconfig.me", "onefetch"} // the trailing exit is dropped
	if got := inputTexts(cmds); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got  %q\n want %q", got, want)
	}
	if !strings.HasPrefix(cmds[1].OutputText, "Linux port") || strings.ContainsAny(cmds[1].OutputText, "\r⏎=") {
		t.Errorf("uname's output: %q", cmds[1].OutputText)
	}
}

// In the user's recording `onefetch` was typed while `curl` was still running: the
// terminal echoed it, one character per frame, and curl printed its IP (with no
// newline) right after it. fish then prints a marker for the missing newline, pads
// the line with spaces to the terminal's width and moves on. The IP is the whole of
// curl's output: the echo is the start of the next command, and the marker and the
// padding are fish's, for the prompt that follows.
func TestCurlOutputSurvivesTypedAheadKeystrokesAndFishsFreshLineMarker(t *testing.T) {
	path := "testdata/fish-kitty-keyboard.jsonl"
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	curl := cmds[3]
	if curl.InputText != "curl -4 ifconfig.me" || curl.OutputText != "188.30.133.136" {
		t.Errorf("curl: input %q, output %q", curl.InputText, curl.OutputText)
	}
	if len(curl.TypedAhead) != len("onefetch") {
		t.Errorf("the 8 echoed keystrokes of `onefetch` should be recognised, got %v", curl.TypedAhead)
	}
	for _, c := range cmds {
		if strings.ContainsAny(c.OutputText, "⏎\r") {
			t.Errorf("%q: the shell's marker leaked into the output: %q", c.InputText, c.OutputText)
		}
	}
	if !strings.Contains(cmds[4].PromptFrame.Data, "⏎") {
		t.Error("what the shell wrote after curl ended belongs to the next prompt")
	}
	if want := cmds[1].OutputText; !strings.HasPrefix(want, "Linux port") {
		t.Errorf("uname: %q", want)
	}

	// Nothing is lost by this: every byte is still in the recording, saved or not.
	stream := func(cs []Command) string {
		var b strings.Builder
		for _, c := range cs {
			b.WriteString(c.PromptFrame.Data)
			for _, f := range c.InputFrames {
				b.WriteString(f.Data)
			}
			b.WriteString(reconstructOutput(c.OutputFrames))
		}
		return b.String()
	}
	before := stream(cmds)
	tmp := t.TempDir() + "/terminal.jsonl"
	data, _ := os.ReadFile(path)
	_ = os.WriteFile(tmp, data, 0o644)
	if err := RebuildRecording(tmp, tmp+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	again, err := ParseRecording(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inputTexts(again), inputTexts(cmds)) || again[3].OutputText != "188.30.133.136" {
		t.Errorf("a save changed the commands or curl's output: %q / %q", inputTexts(again), again[3].OutputText)
	}
	for _, needle := range []string{"188.30.133.136", "\x1b]133;C;cmdline_url=onefetch", "⏎"} {
		if strings.Count(before, needle) != strings.Count(stream(again), needle) {
			t.Errorf("%q occurs %d times before a save and %d after", needle, strings.Count(before, needle), strings.Count(stream(again), needle))
		}
	}
}
