package main

import (
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
