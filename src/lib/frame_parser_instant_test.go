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
