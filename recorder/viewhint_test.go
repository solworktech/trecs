package main

import (
	"bytes"
	"testing"
)

func TestViewHint(t *testing.T) {
	// a buffer is not a terminal: no colours
	got := viewHint(&bytes.Buffer{}, "/home/jesse/.config/trecs/recordings/ls/terminal.jsonl")
	want := "Play it back and edit with:\ntrecs-edit view /home/jesse/.config/trecs/recordings/ls/terminal.jsonl\n"
	if got != want {
		t.Errorf("viewHint = %q, want %q", got, want)
	}
}

func TestUploadHint(t *testing.T) {
	const path = "/home/jesse/.config/trecs/recordings/ls/terminal.jsonl"
	for _, c := range []struct{ name, want string }{
		{"", "You can always upload it later with:\ntrecs upload " + path + "\n"}, // `-name` with no name would swallow the path
		{"demo", "You can always upload it later with:\ntrecs upload -name demo " + path + "\n"},
		{"my demo", "You can always upload it later with:\ntrecs upload -name 'my demo' " + path + "\n"},
	} {
		if got := uploadHint(&bytes.Buffer{}, c.name, path); got != c.want {
			t.Errorf("uploadHint(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestQuoteArg(t *testing.T) {
	for _, c := range []struct{ in, goos, want string }{
		{"/a/b-c_d.e/terminal.jsonl", "linux", "/a/b-c_d.e/terminal.jsonl"},
		{"/Users/Jesse Packman/rec/terminal.jsonl", "darwin", "'/Users/Jesse Packman/rec/terminal.jsonl'"},
		{"/tmp/it's here/t.jsonl", "linux", `'/tmp/it'\''s here/t.jsonl'`},
		{"/tmp/$HOME/*.jsonl", "linux", "'/tmp/$HOME/*.jsonl'"},
		{"C:\\Users\\Jesse P\\t.jsonl", "windows", `"C:\Users\Jesse P\t.jsonl"`},
		{"", "linux", "''"},
	} {
		if got := quoteArg(c.in, c.goos); got != c.want {
			t.Errorf("quoteArg(%q, %s) = %s, want %s", c.in, c.goos, got, c.want)
		}
	}
}
