package lib

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The recording of a user who edited the first command of a fish session (fish 4: marks A, B, C with the command
// line, and D) - ls -al became ls -al --colors, and the file count in its output changed to match. Saved, the
// recording lost the C and D marks of that command and its own header, so read back its output was taken for
// part of what was typed.
const editFixture = "testdata/fish-edit-input-and-output.jsonl"

func copyToTemp(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// markCounts counts, in a saved recording, the marks that say where commands start and end.
func markCounts(t *testing.T, path string) (c, d int) {
	t.Helper()
	frames, _, _, err := LoadFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		c += strings.Count(f.Data, "\x1b]133;C")
		d += strings.Count(f.Data, "\x1b]133;D")
	}
	return c, d
}

func TestEditingInputAndOutputOfAMarkedCommandSurvivesASaveAndAReload(t *testing.T) {
	path := copyToTemp(t, editFixture)
	before, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(readFile(t, path), "\n")

	edited := append([]Command(nil), before...)
	edited[0].InputText = "ls -al --colors"
	edited[0].OutputText = strings.Replace(before[0].OutputText, "total 11232", "total 11233", 1)
	if edited[0].OutputText == before[0].OutputText {
		t.Fatal("the fixture's first command no longer says total 11232")
	}
	if err := RebuildRecording(path, path+".bak", edited); err != nil {
		t.Fatal(err)
	}

	// the header - the size it was recorded at - is still the first line
	if got, _, _ := strings.Cut(readFile(t, path), "\n"); got != header || !strings.Contains(got, `"type":"meta"`) {
		t.Errorf("first line of the saved recording: %q, want the header %q", got, header)
	}
	_, _, meta, err := LoadFrames(path)
	if err != nil || meta.Width != 172 || meta.Height != 38 {
		t.Errorf("recorded size after the save: %+v (%v), want 172x38", meta, err)
	}
	// every command still starts and ends where the shell said (the trailing exit, which is not a command here, apart)
	if c, d := markCounts(t, path); c != len(before) || d != len(before) {
		t.Errorf("marks after the save: %d starts and %d ends, for %d commands", c, d, len(before))
	}

	// and read back, the first command is what was edited - not a mess of its input and output
	after, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("%d commands after the save, there were %d", len(after), len(before))
	}
	if after[0].InputText != "ls -al --colors" {
		t.Errorf("first command's input: %q", after[0].InputText)
	}
	if after[0].OutputText != edited[0].OutputText {
		t.Errorf("first command's output:\n got  %q\n want %q", after[0].OutputText, edited[0].OutputText)
	}
	for i := 1; i < len(before); i++ {
		if after[i].InputText != before[i].InputText || after[i].OutputText != before[i].OutputText {
			t.Errorf("command %d changed though it was not edited: %q / %q", i+1, after[i].InputText, after[i].OutputText)
		}
	}
}

func TestEditingOnlyTheOutputOfAMarkedCommandKeepsItsMarks(t *testing.T) {
	path := copyToTemp(t, editFixture)
	before, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := append([]Command(nil), before...)
	edited[1].OutputText = "Linux somewhere else"
	if err := RebuildRecording(path, path+".bak", edited); err != nil {
		t.Fatal(err)
	}
	if c, d := markCounts(t, path); c != len(before) || d != len(before) {
		t.Errorf("marks: %d starts and %d ends, for %d commands", c, d, len(before))
	}
	after, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := inputTexts(after); !reflect.DeepEqual(got, inputTexts(before)) {
		t.Errorf("commands:\n got  %q\n want %q", got, inputTexts(before))
	}
	if after[1].OutputText != "Linux somewhere else" {
		t.Errorf("edited output: %q", after[1].OutputText)
	}
	if after[0].OutputText != before[0].OutputText {
		t.Errorf("the command before it changed: %q", after[0].OutputText)
	}
}

// Deleting a command's output altogether leaves the command, with its marks, and no output.
func TestEmptyingTheOutputOfAMarkedCommandKeepsTheCommand(t *testing.T) {
	path := copyToTemp(t, editFixture)
	before, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := append([]Command(nil), before...)
	edited[0].OutputText = ""
	if err := RebuildRecording(path, path+".bak", edited); err != nil {
		t.Fatal(err)
	}
	after, err := ParseRecording(path)
	if err != nil || len(after) != len(before) || after[0].InputText != before[0].InputText || after[0].OutputText != "" {
		t.Fatalf("%v: %d commands, first %q -> %q", err, len(after), after[0].InputText, after[0].OutputText)
	}
}

// A recording is saved with the header it had; one that had none gets none.
func TestRebuildKeepsTheHeaderOnlyIfThereWasOne(t *testing.T) {
	path := copyToTemp(t, editFixture)
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(readFile(t, path), "\n"); first != `{"type":"meta","width":172,"height":38}` {
		t.Errorf("first line: %q", first)
	}

	_, headless, _ := strings.Cut(readFile(t, copyToTemp(t, editFixture)), "\n")
	bare := filepath.Join(t.TempDir(), "bare.jsonl")
	if err := os.WriteFile(bare, []byte(headless), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds, err = ParseRecording(bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildRecording(bare, bare+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, bare), `"type":"meta"`) {
		t.Error("a recording that had no header must not be given one")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
