package lib

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mark(s string) string { return "\x1b]133;" + s + "\x07" }

func framesOf(parts ...string) []TerminalFrame {
	out := make([]TerminalFrame, len(parts))
	for i, p := range parts {
		out[i] = TerminalFrame{Timestamp: int64(i) * 10, Data: p}
	}
	return out
}

func groupedTexts(t *testing.T, frames []TerminalFrame) []string {
	t.Helper()
	cmds, err := groupIntoCommands(frames, "")
	if err != nil {
		t.Fatal(err)
	}
	return inputTexts(cmds)
}

// marks-script.jsonl was recorded with `recorder record -commands-file` in bash,
// with no terminal. The shell itself said where every prompt, input and output
// began and ended.
func TestRecordingWithPromptMarksIsGroupedByThem(t *testing.T) {
	cmds, err := ParseRecording("testdata/marks-script.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ls /etc/hostname",
		"printf '%s\\n' -author \"Jesse\" -i \\\n    https://example.com/README.md -o out.pdf \\\n    -theme dark -with-footer",
		"echo \"first\nsecond\"",
		"echo \"> not a continuation prompt\"",
		"true",
		"echo done",
	} // the trailing `exit` is dropped by ParseRecording
	if got := inputTexts(cmds); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got  %q\n want %q", got, want)
	}
	if cmds[1].ContinuationPrompt != "> " || !strings.Contains(cmds[1].OutputText, "https://example.com/README.md") {
		t.Errorf("continuation prompt %q, output %q", cmds[1].ContinuationPrompt, cmds[1].OutputText)
	}
	if strings.TrimSpace(cmds[4].OutputText) != "" || strings.TrimSpace(cmds[3].OutputText) != "> not a continuation prompt" {
		t.Errorf("outputs: %q / %q", cmds[4].OutputText, cmds[3].OutputText)
	}
	for i := 1; i < len(cmds); i++ {
		if cmds[i].FirstRawFrameIndex < cmds[i-1].FirstRawFrameIndex || cmds[i-1].LastRawFrameIndex < cmds[i-1].FirstRawFrameIndex {
			t.Errorf("frame ranges out of order at %d: %+v %+v", i, cmds[i-1].FirstRawFrameIndex, cmds[i].FirstRawFrameIndex)
		}
	}
}

// A multi-line command recalled from history: drawn in reverse video, redrawn
// after cursor-up moves. Derived from a real recording by inserting the marks.
func TestMarksReadARecalledMultiLineCommand(t *testing.T) {
	cmds, err := ParseRecording("testdata/md2pdf-marks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"md2pdf -author \"Jesse Portnoy\" -i \\\n    https://github.com/solworktech/md2pdf/raw/refs/heads/master/README.md -o md2pdf.pdf \\\n    -theme dark -title \"Deb packages\" -with-footer",
		"wget https://github.com/jessp01/crash-course-in/raw/refs/heads/main/courses/apt_dpkg_deb/apt_dpkg_deb.md -P ~/docs",
	}
	if got := inputTexts(cmds); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got  %q\n want %q", got, want)
	}
}

func TestMarksDoNotCareWhatThePromptLooksLike(t *testing.T) {
	// no $, # or > anywhere, and no window title: nothing the heuristics could use
	frames := framesOf(
		mark("D;0")+mark("A")+"λ "+mark("B"), "l", "s", "\r\n", mark("C")+"a.txt\r\n",
		mark("D;0")+mark("A")+"λ "+mark("B"), "p", "w", "d", "\r\n", mark("C")+"/tmp\r\n",
		mark("D;0")+mark("A")+"λ "+mark("B"), "exit\r\n",
	)
	if got := groupedTexts(t, frames); !reflect.DeepEqual(got, []string{"ls", "pwd"}) {
		t.Errorf("got %q", got)
	}
}

func TestMarkCutBetweenTwoFramesIsJoined(t *testing.T) {
	frames := framesOf(mark("A")+"$ "+mark("B")+"ls\r\n\x1b]133", ";C\x07out\r\n\x1b]133;D;0\x07\x1b]133;", "A\x07$ \x1b]133;B\x07")
	cmds, err := groupIntoCommands(frames, "")
	if err != nil || len(cmds) != 1 || cmds[0].InputText != "ls" || strings.TrimSpace(cmds[0].OutputText) != "out" {
		t.Errorf("cmds=%+v err=%v", cmds, err)
	}
}

func TestEmptyEnterCtrlCAndRedrawedPromptsLeaveNoExtraCommands(t *testing.T) {
	frames := framesOf(
		mark("D;0")+mark("A")+"$ "+mark("B"), "\r\n", // empty Enter: never executed
		mark("D;0")+mark("A")+"$ "+mark("B"), "x", "^C\r\n", // Ctrl-C: nothing ran
		mark("D;130")+mark("A")+"$ "+mark("B"), "l", "\r"+mark("A")+"$ "+mark("B")+"ls", "\r\n", mark("C")+"ok\r\n", // a redraw: A with no D before it
	)
	got := groupedTexts(t, frames)
	n := 0
	for _, g := range got {
		if g == "ls" {
			n++
		}
	}
	if n != 1 || got[len(got)-1] != "ls" {
		t.Errorf("got %q", got)
	}
}

// Saving must lose or repeat nothing, and the marks must survive it: the saved
// recording is still a marked one.
func TestSavingAMarkedRecordingChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	data, err := os.ReadFile("testdata/marks-script.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	again, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inputTexts(again), inputTexts(cmds)) {
		t.Errorf("commands changed by a save:\n before %q\n after  %q", inputTexts(cmds), inputTexts(again))
	}
	for i := range cmds {
		if cmds[i].OutputText != again[i].OutputText {
			t.Errorf("command %d output changed by a save", i)
		}
	}
	saved, _ := os.ReadFile(path)
	if !strings.Contains(string(saved), "133;A") || !strings.Contains(string(saved), "133;C") {
		t.Error("the saved recording lost its marks")
	}
}
