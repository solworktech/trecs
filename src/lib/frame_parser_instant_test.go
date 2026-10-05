package lib

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// cli-session.jsonl was recorded with the real recorder. In it the shell's Enter
// echo shares a frame with the command's output (uname, df, ...), and for `true`
// - which prints nothing - with the next prompt as well.
const instantSession = "testdata/cli-session.jsonl"

func inputTexts(cmds []Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, c.InputText)
	}
	return out
}

func TestCommandsThatFinishAtOnceAreNotMerged(t *testing.T) {
	cmds, err := ParseRecording(instantSession)
	if err != nil {
		t.Fatal(err)
	}
	got := inputTexts(cmds)
	// the trailing `exit` is dropped by ParseRecording: it only ends the session
	want := []string{
		"ls -l /etc/hostname /etc/os-release", "uname -a", "echo 'a `quoted` word'", "true",
		"ls -l /etc/hostname /etc/os-release", "df -h /", `printf 'one\ntwo\n'`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got  %q\n want %q", got, want)
	}
	// the output of each landed on the right command, including the part that
	// shared a frame with the Enter echo and the part that shared one with the prompt
	if !strings.Contains(cmds[1].OutputText, "Linux vm") {
		t.Errorf("uname's output = %q", cmds[1].OutputText)
	}
	if strings.TrimSpace(cmds[3].OutputText) != "" {
		t.Errorf("`true` prints nothing, got %q", cmds[3].OutputText)
	}
	if !strings.Contains(cmds[5].OutputText, "Filesystem") {
		t.Errorf("df's output = %q", cmds[5].OutputText)
	}
}

// RebuildRecording writes prompt + input + output frames back out. Splitting a
// merged frame must therefore neither lose nor repeat a byte: saving and
// re-reading must give the same commands and the same output, once.
func TestSavingAfterTheSplitKeepsEveryByteOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "terminal.jsonl")
	data, err := os.ReadFile(instantSession)
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
			t.Errorf("command %d output changed by a save:\n before %q\n after  %q", i, cmds[i].OutputText, again[i].OutputText)
		}
	}
	stream := func(p string) string {
		frames, _, _, err := LoadFrames(p)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, f := range frames {
			b.WriteString(f.Data)
		}
		return b.String()
	}
	for _, marker := range []string{"a `quoted` word", "Linux vm", "Filesystem"} {
		if before, after := strings.Count(stream(instantSession), marker), strings.Count(stream(path), marker); before != after || after == 0 {
			t.Errorf("%q occurs %d times in the original and %d after a save", marker, before, after)
		}
	}
}

// reverse-search.jsonl has two Ctrl-R sessions. Each redraws the line with a
// leading CR (which is not Enter) and ends with a redraw of the accepted line
// plus Enter in one write. Before, the commands came out as
// "(reverse-i-search)`':", an empty one, and "PING debian.org s of data.".
func TestReverseISearchYieldsTheCommandThatRan(t *testing.T) {
	cmds, err := ParseRecording("testdata/reverse-search.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ls -al", "uname -a", "lsb_release", "onefetch", "df -h", ". /etc/profile.d/zaje.sh", "df -h", "ping -4 debian.org",
	}
	if got := inputTexts(cmds); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got  %q\n want %q", got, want)
	}
	// the output went to the right command: ping's header is ping's, df's table is df's
	if !strings.Contains(cmds[7].OutputText, "PING debian.org") || strings.Contains(cmds[6].OutputText, "PING") {
		t.Errorf("output misattributed: ping=%q", cmds[7].OutputText)
	}
}

func TestReconstructInputFollowsTheLineNotTheKeystrokes(t *testing.T) {
	prompt := TerminalFrame{Data: "\x1b]0;t\x07\x1b[01;32mu@h\x1b[00m:~$ "}
	fr := func(chunks ...string) []TerminalFrame {
		var out []TerminalFrame
		for i, c := range chunks {
			out = append(out, TerminalFrame{Timestamp: int64(i), Data: c})
		}
		return out
	}
	for name, c := range map[string]struct {
		in   []TerminalFrame
		want string
	}{
		"plain typing with backspaces":     {fr("l", "s", "x", "\b\x1b[K", " ", "-", "a", "\r\n"), "ls -a"},
		"echoed backspace-space-backspace": {fr("abc", "\b \b", "d", "\r\n"), "abd"},
		"history recall rewrites the line": {fr(". /etc/profile.d/zaje.sh ", strings.Repeat("\b", 25)+"df -h\x1b[K", "\r\n"), "df -h"},
		"a CR redraw after i-search":       {fr("\r(reverse-i-search)`': \x1b[K", "\r\x1b]0;t\x07\x1b[01;32mu@h\x1b[00m:~$ ls -al\x1b[K\b\b\b\b\b\b\r\n"), "ls -al"},
		"delete a character mid-line":      {fr("ls  -l", "\b\b\b\b", "\x1b[1P", "\r\n"), "ls -l"},
	} {
		if got := reconstructInput(c.in, prompt, ""); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// multiline.jsonl: a command continued with backslashes (like a long md2pdf
// invocation), one continued by an open quote, and one whose OUTPUT begins with
// "> ". Before, the command was only its first line and the rest of it, with the
// real output, was filed as output.
func TestMultiLineCommandsAreOneCommand(t *testing.T) {
	cmds, err := ParseRecording("testdata/multiline.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"printf \"%s\\n\" -author \"Jesse\" -i \\\n    https://example.com/README.md -o out.pdf \\\n    -theme dark -with-footer",
		"echo \"first\nsecond\"",
		"echo \"> not a continuation prompt\"",
		"echo done",
	}
	if got := inputTexts(cmds); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got  %q\n want %q", got, want)
	}
	if cmds[0].ContinuationPrompt != "> " || cmds[2].ContinuationPrompt != "" {
		t.Errorf("continuation prompts: %q, %q", cmds[0].ContinuationPrompt, cmds[2].ContinuationPrompt)
	}
	if !strings.Contains(cmds[0].OutputText, "-author") || !strings.Contains(cmds[0].OutputText, "https://example.com/README.md") {
		t.Errorf("the output belongs to the command: %q", cmds[0].OutputText)
	}
	if strings.TrimSpace(cmds[2].OutputText) != "> not a continuation prompt" {
		t.Errorf("output that begins with \"> \" must stay output: %q", cmds[2].OutputText)
	}
	if got := CollapseCommand(cmds[0].InputText); got != "printf \"%s\\n\" -author \"Jesse\" -i https://example.com/README.md -o out.pdf -theme dark -with-footer" {
		t.Errorf("CollapseCommand = %q", got)
	}
}

// Saving must not take a multi-line command for an edited one (syncCommandFrames
// rewrites the frames of a command whose text no longer matches them) nor lose or
// repeat a byte of the continuation lines.
func TestSavingAMultiLineCommandChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	data, err := os.ReadFile("testdata/multiline.jsonl")
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
}

// md2pdf-history.jsonl: a three-line command recalled from history with the Up
// arrow. It is drawn in reverse video, redrawn after cursor-up moves, and Enter
// arrives as a bare "bracketed paste off" with no CRLF of its own. Before, those
// frames were discarded as "not input" and the first line of the command's
// OUTPUT was taken for the command.
func TestMultiLineCommandRecalledFromHistory(t *testing.T) {
	cmds, err := ParseRecording("testdata/md2pdf-history.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"md2pdf -author \"Jesse Portnoy\" -i \\\n    https://github.com/solworktech/md2pdf/raw/refs/heads/master/README.md -o md2pdf.pdf \\\n    -theme dark -title \"Deb packages\" -with-footer",
		"wget https://github.com/jessp01/crash-course-in/raw/refs/heads/main/courses/apt_dpkg_deb/apt_dpkg_deb.md -P ~/docs",
	} // the trailing `exit` (Ctrl-D) is dropped by ParseRecording
	if got := inputTexts(cmds); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got  %q\n want %q", got, want)
	}
	if !strings.Contains(cmds[0].OutputText, "Downloaded image to: /tmp/md2pdf/badge.svg") {
		t.Errorf("the output belongs to the command: %q", cmds[0].OutputText)
	}

	// and saving it changes nothing: same commands, same output, each byte once
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	data, _ := os.ReadFile("testdata/md2pdf-history.jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	again, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inputTexts(again), want) {
		t.Errorf("commands changed by a save: %q", inputTexts(again))
	}
	for i := range cmds {
		if cmds[i].OutputText != again[i].OutputText {
			t.Errorf("command %d output changed by a save", i)
		}
	}
}
