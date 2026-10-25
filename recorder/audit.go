package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"trecs/scriptaudit"
)

// A script given to -commands-file is typed into a shell with the person's permissions. Before anything is run it is read: by
// package scriptaudit (always), and by ShellCheck when that is installed. What the audit finds that is serious (a download that is
// run, a recursive remove of the home directory, a write to a startup file, a secret sent away...) stops the recording
// unless -allow-risky is given: safety is not left to everyone remembering to look. It is a seat belt and not a sandbox - see
// the package - and CI that must run such a script says so with the flag, in the open.

// say writes to the person's terminal. There is nothing to do when that fails, so the error is dropped in one place.
func say(out io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }

// auditScript reads the commands file and reports whether it may be run, and how many warnings it raised (the audit's findings and
// ShellCheck's). Findings go to out: what only warns in yellow, what blocks with a red label.
func auditScript(file, shell string, allowRisky bool, out io.Writer) (ok bool, warnings int) {
	raw, err := os.ReadFile(file)
	if err != nil {
		say(out, "Error: %v\n", err)
		return false, 0
	}
	findings := scriptaudit.Audit(string(raw), scriptaudit.DialectFor(shell))
	if len(findings) > 0 {
		say(out, "\nChecking %s before it is run:\n", file)
		for _, f := range findings {
			if f.Severity == scriptaudit.Block {
				say(out, "%s line %d: %s\n      %s\n", red(out, "  BLOCKED"), f.Line, f.Message, f.Text)
			} else {
				say(out, "%s\n", yellow(out, fmt.Sprintf("  warning line %d: %s\n      %s", f.Line, f.Message, f.Text)))
			}
		}
	}
	warnings = len(findings) + runShellCheck(file, shell, out)

	if scriptaudit.Blocking(findings) {
		if !allowRisky {
			say(out, "%s\n", red(out, "\nNot run: the commands above are the kind that cause damage that cannot be undone, or hide what they do."))
			say(out, "If you have read them and they are meant, run again with -allow-risky. Nothing was run.\n")
			return false, warnings
		}
		say(out, "%s\n", yellow(out, "\nGoing on, as -allow-risky was given."))
	}
	return true, warnings
}

// shellCheckDialect is the -s value for ShellCheck, or "" for a shell it cannot read.
func shellCheckDialect(shell string) string {
	switch scriptaudit.DialectFor(shell) {
	case scriptaudit.Bash:
		if shellName(shell) == "zsh" {
			return ""
		}
		return "bash"
	case scriptaudit.POSIX:
		if n := shellName(shell); n == "dash" || n == "ash" {
			return "dash"
		}
		return "sh"
	case scriptaudit.Korn:
		return "ksh"
	}
	return ""
}

// runShellCheck shows what ShellCheck (github.com/koalaman/shellcheck, GPLv3: a program we run, not code we ship) says about the file,
// if it is installed, and returns how many things it said. It never stops anything: its findings are style and mistakes, and the
// audit above decides what is dangerous.
func runShellCheck(file, shell string, out io.Writer) int {
	path, err := exec.LookPath("shellcheck")
	if err != nil {
		return 0
	}
	dialect := shellCheckDialect(shell)
	if dialect == "" {
		say(out, "ShellCheck is installed but does not read %s; it was not used.\n", shellName(shell))
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "-s", dialect, "-f", "gcc", "-S", "warning", "--", file)
	cmd.Stdout = &buf
	_ = cmd.Run() // exit 1 means "found something"
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	say(out, "\nShellCheck says:\n")
	for i, l := range lines {
		if i == 20 {
			say(out, "%s\n", yellow(out, fmt.Sprintf("  ... and %d more (run shellcheck %s)", len(lines)-i, file)))
			break
		}
		say(out, "%s\n", yellow(out, "  "+strings.TrimPrefix(l, file+":")))
	}
	return len(lines)
}

// ---- shells without prompt marks --------------------------------------------------------------------------------------

// A script is typed a line at a time, and the next line must wait until the shell is back at its prompt. A shell that marks its prompts
// (OSC 133: bash, zsh and fish, as the recorder starts them) says when. A shell that does not can only be guessed at, and a guess is wrong
// when the shell asks for something itself: its own `read` is typed the next line of the script as the answer. So a script is not typed
// into such a shell unless -allow-unmarked says that is meant.

// supportsMarks says whether the recorder can make this shell mark its prompts.
func supportsMarks(shell string, noMarks bool) bool {
	if noMarks {
		return false
	}
	switch shellName(shell) {
	case "bash":
		return bashSupportsMarks(shell) // 4.4 or newer: macOS's 3.2 and some old Linux distributions' bash cannot
	case "zsh", "fish":
		return true
	}
	return false
}

// whyUnmarked is a few words on why the recorder cannot make shell mark its prompts, when that is something to say.
func whyUnmarked(shell string) string {
	if shellName(shell) == "bash" {
		return " (it is older than bash 4.4)"
	}
	return ""
}

// markShellsAvailable are the shells on this machine that can be recorded with marks, by name and path.
func markShellsAvailable() map[string]string {
	found := map[string]string{}
	for _, n := range []string{"bash", "zsh", "fish"} {
		if p, err := exec.LookPath(n); err == nil && (n != "bash" || bashSupportsMarks(p)) {
			found[n] = p
		}
	}
	return found
}

// unmarkedRefusal is what is said when a script is not typed into a shell without marks.
func unmarkedRefusal(shell string, noMarks bool, avail map[string]string) string {
	var b strings.Builder
	if noMarks {
		fmt.Fprintf(&b, "-commands-file with -no-marks: the prompt marks are what tell the recorder that %s is ready for the next line.\n", shellName(shell))
	} else {
		fmt.Fprintf(&b, "-commands-file in %s: this shell cannot be made to mark its prompts"+whyUnmarked(shell)+", so the recorder cannot tell when it is ready for the next line,\n"+
			"and a line of the script can be typed as the answer to a question that a command of the script asks (a `read`, a prompt of the shell's own).\n", shellName(shell))
	}
	b.WriteString("Use a shell that supports prompt marks - that is what a scripted recording should be made with:\n")
	if len(avail) == 0 {
		b.WriteString("  none of bash, zsh or fish was found on this machine; install one of them (bash is the smallest).\n")
	}
	for _, n := range []string{"bash", "zsh", "fish"} {
		if p, ok := avail[n]; ok {
			fmt.Fprintf(&b, "  -cmd %s    (found: %s)\n", n, p)
		}
	}
	b.WriteString("The script is read as that shell reads it. If it is a /bin/sh script, bash will run it (bash runs POSIX sh), and nothing else changes.\n")
	b.WriteString("If you must use " + shellName(shell) + ", give -allow-unmarked: lines are then typed when the shell is quiet, which is right unless the script asks for input.")
	return b.String()
}

// markShellChoices are the shells of avail that can be offered, in the order they are numbered: bash, zsh, fish.
func markShellChoices(avail map[string]string) (names, paths []string) {
	for _, n := range []string{"bash", "zsh", "fish"} {
		if p, ok := avail[n]; ok {
			names, paths = append(names, n), append(paths, p)
		}
	}
	return names, paths
}

// unmarkedInteractiveWarning is what a person at the keyboard is told before recording in a shell that has no prompt marks, with the
// numbered choices: each shell that can be recorded with its commands told apart (from 0), then recording in the shell that was asked for.
func unmarkedInteractiveWarning(shell string, avail map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s cannot be made to mark its prompts"+whyUnmarked(shell)+", so this recording will not be able to detect individual commands:\n", shellName(shell))
	b.WriteString("it is kept as one stream of terminal output, and the editor cannot tell where one command ends and the next begins.\n")
	names, paths := markShellChoices(avail)
	if len(names) > 0 {
		b.WriteString("It's recommended that you use one of these shells instead:\n")
		for i, n := range names {
			fmt.Fprintf(&b, "  %d  %s    (%s)\n", i, n, paths[i])
		}
	} else {
		b.WriteString("None of bash, zsh or fish was found on this machine; install one of them to get that (bash is the smallest).\n")
	}
	fmt.Fprintf(&b, "  %d  Record using %s\n", len(names), shellName(shell))
	return b.String()
}

// confirmUnmarked warns a person who is about to record in a shell without prompt marks, and asks for one key: the number of a shell to
// record in instead (its path is returned as use), or the number after those - recording in the shell that was asked for (use is empty).
// Any other key, Enter too, is not to go on. It reports whether to go on.
func confirmUnmarked(shell string, avail map[string]string, in *os.File, out io.Writer) (use string, ok bool) {
	say(out, "\n%s\n", yellow(out, unmarkedInteractiveWarning(shell, avail)))
	_, paths := markShellChoices(avail)
	key := askKey("Input selection number (or any other key to exit)", ": ", in, out)
	switch n := int(key) - '0'; {
	case n >= 0 && n < len(paths):
		return paths[n], true
	case n == len(paths):
		return "", true
	}
	return "", false
}
