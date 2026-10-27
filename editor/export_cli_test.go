package main

import (
	"fmt"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportMarkdownWithAndWithoutTheWordMd(t *testing.T) {
	for name, args := range map[string]func(f string) []string{
		"export md <file>":           func(f string) []string { return []string{"md", f} },
		"export md -file <file>":     func(f string) []string { return []string{"md", "-file", f} },
		"export <file> (as before)":  func(f string) []string { return []string{f} },
		"export -file <file> -title": func(f string) []string { return []string{"-file", f, "-title", "T"} },
	} {
		f := filepath.Join(t.TempDir(), "a.jsonl")
		copyFile(t, cueFixture, f)
		if err := runExport(args(f)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		md, err := os.ReadFile(f + ".md")
		if err != nil || !strings.Contains(string(md), "echo hello world") {
			t.Errorf("%s: %v\n%s", name, err, md)
		}
	}
}

func TestExportMarkdownTitle(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.jsonl")
	copyFile(t, cueFixture, f)
	if err := runExport([]string{"md", f, "-title", "My session"}); err != nil {
		t.Fatal(err)
	}
	if md, _ := os.ReadFile(f + ".md"); !strings.HasPrefix(string(md), "# My session") {
		t.Errorf("the heading: %.60s", md)
	}
}

func TestExportGIF(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.jsonl")
	copyFile(t, cueFixture, f)
	if err := runExport([]string{"gif", f, "-hold", "0", "-fps", "5"}); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(f + ".gif") // next to the recording, as the Markdown is
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	g, err := gif.DecodeAll(fh)
	if err != nil || len(g.Image) < 2 {
		t.Fatalf("%v, %d pictures", err, len(g.Image))
	}
	// -o names it, flags may come before the file, and none of the Markdown's are accepted
	out := filepath.Join(dir, "x.gif")
	if err := runExport([]string{"gif", "-o", out, "-speed", "4", "-no-cursor", "-file", f}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}
	if err := runExport([]string{"gif", f, "-title", "x"}); err == nil {
		t.Error("-title is for Markdown")
	}
}

func TestExportSaysWhatIsWrong(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.jsonl")
	copyFile(t, cueFixture, f)
	for want, args := range map[string][]string{
		"needs a format":   {},
		"file is required": {"gif"},
		"cannot access":    {"gif", filepath.Join(t.TempDir(), "missing.jsonl")},
		"frames a second":  {"gif", f, "-fps", "0x"},
	} {
		err := runExport(args)
		if want == "frames a second" { // -fps 0x is not a number: the flag package says so
			want = "failed to parse flags"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", want, err)
		}
	}
	if err := runExport([]string{"gif", f, "-fps", "99"}); err == nil || !strings.Contains(err.Error(), "frames a second") {
		t.Errorf("-fps 99: %v", err)
	}
}

func TestIdleTrimCutsThePausesInPlaceAndKeepsABackup(t *testing.T) {
	f := pausedRecording(t, t.TempDir())
	before, _ := os.ReadFile(f)
	if err := runIdleTrim([]string{"-idle-trim", "1s", f}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(f + ".backup"); err != nil || string(b) != string(before) {
		t.Errorf("the backup is not the recording as it was: %v", err)
	}
	if after, _ := os.ReadFile(f); string(after) == string(before) {
		t.Error("the recording was not changed")
	}
}

func TestIdleTrimWithOKeepsTheRecordingAsItIs(t *testing.T) {
	dir := t.TempDir()
	f, out := pausedRecording(t, dir), filepath.Join(dir, "short.jsonl")
	before, _ := os.ReadFile(f)
	if err := runIdleTrim([]string{f, "-idle-trim", "1s", "-o", out}); err != nil { // the file may come first
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(f); string(after) != string(before) {
		t.Error("the recording was changed")
	}
	if got, _ := os.ReadFile(out); string(got) == string(before) || len(got) == 0 {
		t.Error("the trimmed recording was not written")
	}
	if _, err := os.Stat(out + ".backup"); err == nil {
		t.Error("a backup was left beside the copy")
	}
}

func TestIdleTrimNeedsAFileAndASaneLimit(t *testing.T) {
	f := pausedRecording(t, t.TempDir())
	for name, args := range map[string][]string{
		"no file":      {"-idle-trim", "2s"},
		"two files":    {"-idle-trim", "2s", f, f},
		"too short":    {"-idle-trim", "10ms", f},
		"missing file": {"-idle-trim", "2s", f + ".nope"},
	} {
		if err := runIdleTrim(args); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// pausedRecording writes a recording of three commands, 8.5 s of quiet between them, and returns its path.
func pausedRecording(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":"meta","width":80,"height":24}` + "\n")
	for k := 0; k < 3; k++ {
		s := 1000 + k*10000
		for _, f := range []struct {
			at   int
			data string
		}{{s, `\u001b]133;A\u0007$ \u001b]133;B\u0007`}, {s + 500, "echo hi"}, {s + 900, `\r\n\u001b]133;C\u0007`}, {s + 1000, `hi\r\n`}, {s + 1500, `\u001b]133;D;0\u0007`}} {
			fmt.Fprintf(&b, `{"timestamp":%d,"data":"%s"}`+"\n", f.at, strings.ReplaceAll(f.data, `"`, `\"`))
		}
	}
	path := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
