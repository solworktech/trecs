//go:build !windows

package main

import (
	"os"
	"testing"

	"github.com/creack/pty"
)

// ttyOut is a terminal to write to, which is what makes the hints coloured.
func ttyOut(t *testing.T) *os.File {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })
	return tty
}

func TestHintColours(t *testing.T) {
	const path = "/home/jesse/rec/terminal.jsonl"
	tty := ttyOut(t)
	red, orange, cyan := "\x1b[31m", "\x1b[38;5;208m", "\x1b[36m"
	reset := "\x1b[0m"

	want := "Play it back and edit with:\n" + red + "trecs-edit" + reset + " " + orange + "view" + reset + " " + cyan + path + reset + "\n"
	if got := viewHint(tty, path); got != want {
		t.Errorf("viewHint = %q, want %q", got, want)
	}
	want = "You can always upload it later with:\n" + red + "trecs" + reset + " " + orange + "upload" + reset + " -name demo " + cyan + path + reset + "\n"
	if got := uploadHint(tty, "demo", path); got != want {
		t.Errorf("uploadHint = %q, want %q", got, want)
	}

	t.Setenv("NO_COLOR", "1")
	if got := viewHint(tty, path); got != "Play it back and edit with:\ntrecs-edit view "+path+"\n" {
		t.Errorf("NO_COLOR set: %q", got)
	}
}
