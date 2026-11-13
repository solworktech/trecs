package lib

import (
	"io"
	"os"

	"golang.org/x/term"
)

// SGR parameters for Paint: the 8 ANSI colours every terminal has, and orange, which is not one of them (it is colour 208 of the
// 256-colour palette, which any terminal of this century has; the very oldest show it as some other colour).
const (
	Red     = "31"
	Green   = "32"
	Yellow  = "33"
	Blue    = "34"
	Magenta = "35"
	Cyan    = "36"
	Orange  = "38;5;208"
)

// Paint wraps s in the escape sequences for the colour code (one of the constants above), when w is a terminal that would show it. It
// returns s as it is for a pipe or a file, for TERM=dumb and when NO_COLOR (no-color.org) is set to anything.
func Paint(w io.Writer, code, s string) string {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || !term.IsTerminal(int(f.Fd())) {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
