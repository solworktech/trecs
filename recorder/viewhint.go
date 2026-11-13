package main

import (
	"io"
	"regexp"
	"runtime"
	"strings"

	libtrecs "github.com/solworktech/trecs/lib"
)

// commandHint is a command line to suggest to the person, shown in colour at a terminal: the program in red, its subcommand in orange
// and the recording's path in cyan. Words that are none of those stay as they are.
type commandHint struct{ prog, sub string }

// line is the command line with the path as one word a shell takes as it is (see quoteArg). opts come between the subcommand and the
// path and are not coloured: `-name`, its value.
func (c commandHint) line(w io.Writer, path string, opts ...string) string {
	parts := []string{libtrecs.Paint(w, libtrecs.Red, c.prog), libtrecs.Paint(w, libtrecs.Orange, c.sub)}
	parts = append(parts, opts...)
	parts = append(parts, libtrecs.Paint(w, libtrecs.Cyan, quoteArg(path, runtime.GOOS)))
	return strings.Join(parts, " ")
}

// viewHint is what a finished recording says about opening it: the command that plays it back and edits it, with the recording's
// path as it can be pasted into a shell. w is where it is going to be printed, which decides whether it has colours.
func viewHint(w io.Writer, path string) string {
	return "Play it back and edit with:\n" + commandHint{"trecs-edit", "view"}.line(w, path) + "\n"
}

// uploadHint is what is said when the person did not upload the recording: the command that does. The title comes in only when there is
// one: `-name` with nothing after it would take the path as its value.
func uploadHint(w io.Writer, name, path string) string {
	var opts []string
	if name != "" {
		opts = []string{"-name", quoteArg(name, runtime.GOOS)}
	}
	return "You can always upload it later with:\n" + commandHint{progName(), "upload"}.line(w, path, opts...) + "\n"
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
