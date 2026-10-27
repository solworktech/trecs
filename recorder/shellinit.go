package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	libtrecs "trecs/lib"
)

// `trecs shell-init SHELL` prints the prompt marks as a file that a person can load into a shell the recorder did not start: the
// shell on a server they reached by ssh while recording on their own machine. The recorder starts its own shell with the same
// code (bashIntegration and the others in shellint.go); here it is wrapped so that it does nothing unless it is meant to:
//
//   - the shell must be interactive and its output a terminal: a script, scp, `ssh host command` or cron never sees it;
//   - the code itself skips what it cannot work with (a bash older than 4.4, a prompt that already has marks...).
//
// So loading it by hand marks that one shell, and putting it in a startup file is the person's own choice, with its effects
// listed in the file's header.

// shellInitShells are the shells there is a file for.
var shellInitShells = []string{"bash", "zsh", "fish"}

// shellInitHeader explains the file to whoever opens it. The same text is in the README; if one changes, so must the other.
func shellInitHeader(kind, version string) string {
	var useNow, useAlways string
	switch kind {
	case "fish":
		useNow = "source FILE"
		useAlways = "add that line to ~/.config/fish/config.fish"
	case "zsh":
		useNow = "source FILE"
		useAlways = "add that line to ~/.zshrc"
	default:
		useNow = "source FILE"
		useAlways = "add that line to ~/.bashrc (not ~/.profile: it is read by shells that do not understand this file)"
	}
	return fmt.Sprintf(`# trecs shell integration for %[1]s: prompt marks (OSC 133)    [made by trecs %[2]s]
#
# WHAT THIS IS FOR
#   When trecs records a shell it starts, it makes that shell mark where each prompt, each command and each command's output
#   begins and ends. The recording can then be navigated and searched command by command. A shell that trecs did not start - the
#   one you get on a server after "ssh" - is not marked, and the whole session is recorded as the one command that ran "ssh".
#   Loading this file into that remote shell marks it the same way, and the commands you type there become commands of the
#   recording.
#
# HOW TO USE IT
#   Once, for the shell you are in:     %[3]s     (FILE is where you saved this file)
#   Every time (your own decision):     %[4]s
#   Nothing is installed or changed on the machine by reading this file; it only affects the shell that loads it.
#
# WHAT IT DOES, WHEN LOADED
#   - Before and after each prompt, and when a command starts and ends, the shell writes a short invisible escape sequence
#     (ESC ] 133 ; A, B, C or D;status BEL). These are the marks kitty, iTerm2, WezTerm and VS Code use too. They hold no command
#     text; they go to the terminal only, and nothing is sent anywhere.
#   - To do that it changes the shell's prompt (PS1, PS2%[5]s), and adds functions whose names start with __trecs_.
#   - It does NOT change $? (the status of your last command), your options, aliases, history or what any command does.
#   - It does nothing at all if: the shell is not interactive, its output is not a terminal, the prompt already carries marks
#     (some other tool added them), or the shell is too old for it (bash before 4.4).
#
# IF YOU LOAD IT FROM A STARTUP FILE, SO THAT IT IS ALWAYS ON
#   Everyone who uses that account gets the marks in every interactive session, recorded or not. Know that:
#   - Terminals that understand OSC 133 use it. Most others ignore it. A basic one (a serial or "console" terminal, a very old
#     emulator) may print it as text, which makes the prompt look strange.
#   - Anything that reads the terminal's output sees the marks: a session log, "script", and automation that waits for the end of
#     a prompt with a pattern (expect, pexpect, some deployment tools) - which then may not match.
#   - Another tool that adds marks (VS Code's shell integration, a terminal's own) and loads after this one doubles them.
#   - A later script of yours that replaces the prompt hook outright (a prompt theme, a framework) takes the marks' hooks with
#     it and leaves half of them, so that a recording sees commands that never end. Load this last.
#   - Where PS1 is exported, a different shell started from this one (sh, dash, ksh) can show the marks as text.
%[6]s#   To turn it off again, remove the line you added and start a new shell.
#
`, kind, version, useNow, useAlways, map[bool]string{true: ", PS0", false: ""}[kind == "bash"], shellInitNotes(kind))
}

func shellInitNotes(kind string) string {
	if kind == "bash" {
		return "#   - With \"set -x\", bash shows four extra lines for the prompt functions at each prompt: it prints them before any code can\n#     turn that off.\n"
	}
	return ""
}

// shellInitScript is the file for the shell: the header, then the code, run only in an interactive shell on a terminal.
func shellInitScript(kind, version string) (string, error) {
	head := shellInitHeader(kind, version)
	switch kind {
	case "bash":
		// Aliases are off for the whole of it, and that is done before the code is read as one command: a function body is
		// expanded when it is read, and an alias of the person's for printf would be in it. The code turns them off itself, but
		// by then the case below has been read in whole.
		return head + `__trecs_a0=$(shopt -p expand_aliases 2>/dev/null || :); shopt -u expand_aliases 2>/dev/null
case $- in
*i*)
  if [ -t 1 ]; then
` + bashIntegration + `
  fi
  ;;
esac
builtin eval "$__trecs_a0"; builtin unset __trecs_a0
`, nil
	case "zsh":
		return head + `if [[ -o interactive && -t 1 ]]; then
` + zshIntegration + `
fi
`, nil
	case "fish":
		// fish 4 marks most of it on its own: what is added to it is not what is added to fish 3 (see setupShellIntegrationFor).
		return head + `if status is-interactive; and isatty stdout
    set -l __trecs_major (string split . -- $version)[1]
    if test "$__trecs_major" -ge 4 2>/dev/null
` + indent(fishNativeIntegration, "        ") + `    else
` + indent(fishIntegration, "        ") + `    end
end
`, nil
	}
	return "", fmt.Errorf("there is no file for %q (it is for %s)", kind, strings.Join(shellInitShells, ", "))
}

func indent(s, by string) string {
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = by + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// shellInitKind is the shell named by the person, or else their login shell's.
func shellInitKind(args []string) (string, error) {
	switch len(args) {
	case 0:
		if k := filepath.Base(os.Getenv("SHELL")); k != "" && k != "." {
			for _, s := range shellInitShells {
				if k == s {
					return k, nil
				}
			}
		}
		return "", fmt.Errorf("which shell? one of %s", strings.Join(shellInitShells, ", "))
	case 1:
		for _, s := range shellInitShells {
			if args[0] == s {
				return s, nil
			}
		}
		return "", fmt.Errorf("there is no file for %q (it is for %s)", args[0], strings.Join(shellInitShells, ", "))
	}
	return "", fmt.Errorf("one shell at a time")
}

const shellInitUsage = `usage: %[1]s shell-init [bash|zsh|fish]

Prints a file that marks the prompts of a shell you did not start with %[1]s record - the one on a server after ssh - the way
the recorder marks its own, so that the commands you type there are commands of the recording. Read the comments at its top;
nothing happens until a shell loads it, and it does nothing in a shell that is not interactive.

  %[1]s shell-init bash > trecs-marks.bash     then, on the server:   source trecs-marks.bash
  %[1]s shell-init bash | ssh HOST 'cat > ~/.trecs-marks.bash'
`

func runShellInit(args []string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			_, _ = fmt.Fprintf(stdout, shellInitUsage, filepath.Base(os.Args[0]))
			return 0
		}
	}
	kind, err := shellInitKind(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s shell-init: %v\n\n"+shellInitUsage, filepath.Base(os.Args[0]), err, filepath.Base(os.Args[0]))
		return 2
	}
	script, err := shellInitScript(kind, libtrecs.VersionString())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s shell-init: %v\n", filepath.Base(os.Args[0]), err)
		return 2
	}
	if _, err := io.WriteString(stdout, script); err != nil {
		return 1
	}
	return 0
}
