package main

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// defaultServer is the server `login` falls back to when nothing else says which. A build can set it:
//
//	go build -ldflags "-X main.defaultServer=https://trecs.example.com" ./recorder
//
// It is the last resort: -server, the server of the last login and $TRECS_SERVER all come before it.
var defaultServer string

// serverDefaultHelp says, for -server's help, where the default comes from.
func serverDefaultHelp() string {
	help := "the server you last logged in to, else $TRECS_SERVER"
	if defaultServer != "" {
		help += ", else " + defaultServer
	}
	return help
}

func stdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// yellow wraps s in the escape sequences for yellow, when w is a terminal that would show it (and NO_COLOR, no-color.org, is not set).
func yellow(w io.Writer, s string) string {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || !term.IsTerminal(int(f.Fd())) {
		return s
	}
	return "\x1b[33m" + s + "\x1b[0m"
}

const scriptWarning = `WARNING: -commands-file is not interactive. Every line of %s
is typed into a real shell, with your permissions, exactly as if it were a shell script you had written
and run: nothing reviews the commands, and any of them can change or delete files, install software or
send data over the network. Only continue if you have read the file and trust every line of it.`

// confirmScript warns about what -commands-file does and asks the person to confirm it; it reports whether to go on. Only the exact
// answer YES goes on. With no person to ask (the input is not a terminal: CI, a pipe) the warning is still shown, and it goes on.
func confirmScript(file string, in io.Reader, out io.Writer, interactive bool) bool {
	_, _ = fmt.Fprintf(out, "\n%s\n\n", yellow(out, fmt.Sprintf(scriptWarning, file)))
	if !interactive {
		return true
	}
	_, _ = fmt.Fprint(out, yellow(out, "Type 'YES' to continue, any other input to exit: "))
	line, _ := bufio.NewReader(in).ReadString('\n')
	return strings.TrimSpace(line) == "YES"
}

// resolveServer picks the server to log in to: -server if it was given; else the server of the saved login; else $TRECS_SERVER;
// else the one built in (-ldflags "-X main.defaultServer=..."). "" means none is known and the person is asked.
func resolveServer(flagValue, saved, env, built string) string {
	return cmp.Or(flagValue, saved, env, built)
}

// yesKey: Y, y and Enter are a yes to a question that is [Y/n]; anything else is a no.
func yesKey(b byte) bool { return b == 'y' || b == 'Y' || b == '\r' || b == '\n' }

// askYes asks a question that is answered with one key, [Y/n]. With in a terminal the key is taken as it is pressed (no Enter);
// from anything else - a file, a pipe - the first byte is the answer. Nothing to read is a no.
func askYes(question string, in *os.File, out io.Writer) bool {
	_, _ = fmt.Fprint(out, question+" [Y/n] ")
	var b [1]byte
	if term.IsTerminal(int(in.Fd())) {
		if old, err := term.MakeRaw(int(in.Fd())); err == nil {
			defer func() { _ = term.Restore(int(in.Fd()), old) }()
		}
	}
	if n, _ := in.Read(b[:]); n == 0 {
		_, _ = fmt.Fprintln(out)
		return false
	}
	if b[0] == '\r' || b[0] == '\n' {
		_, _ = fmt.Fprint(out, "\r\n")
	} else if b[0] >= ' ' {
		_, _ = fmt.Fprintf(out, "%c\r\n", b[0])
	} else {
		_, _ = fmt.Fprint(out, "\r\n")
	}
	return yesKey(b[0])
}
