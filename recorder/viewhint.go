package main

import (
	"regexp"
	"runtime"
	"strings"
)

// viewHint is what a finished recording says about opening it: the command that plays it back and edits it, with the recording's
// path as it can be pasted into a shell.
func viewHint(path string) string {
	return "Play it back and edit with:\ntrecs-edit view " + quoteArg(path, runtime.GOOS) + "\n"
}

// plainArg is a word a shell takes as it is: no space, quote, glob, variable or other character that has a meaning.
var plainArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// quoteArg is a path as one word of a command line: as it is when it can be, else in single quotes (double ones on Windows, which
// does not know the others).
func quoteArg(s, goos string) string {
	if plainArg.MatchString(s) {
		return s
	}
	if goos == "windows" {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
