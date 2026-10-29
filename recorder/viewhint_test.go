package main

import "testing"

func TestViewHint(t *testing.T) {
	got := viewHint("/home/jesse/.config/trecs/recordings/ls/terminal.jsonl")
	want := "Play it back and edit with:\ntrecs-edit view /home/jesse/.config/trecs/recordings/ls/terminal.jsonl\n"
	if got != want {
		t.Errorf("viewHint = %q, want %q", got, want)
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
