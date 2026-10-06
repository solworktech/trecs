package main

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// The shell to record, when -cmd isn't given: the one the recorder was run from.
//
// $SHELL is the user's login shell, not the one they are in - someone who started
// bash and then typed `fish` is in fish. The shell in use is the recorder's
// parent process (or, when it was run through sudo, env, make..., the nearest
// ancestor that is a shell), found by asking the OS: /proc on Linux, ps elsewhere.
// $$ is no help (it is not the PID in every shell: fish has $fish_pid).

var knownShells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ash": true, "ksh": true,
	"mksh": true, "tcsh": true, "csh": true, "nu": true, "xonsh": true, "elvish": true, "pwsh": true,
}

// shellName is a program's name as a shell is known by it: its base name, without
// the leading "-" a login shell is started with, or a " (deleted)" left by an update.
func shellName(path string) string {
	path = strings.TrimSuffix(strings.TrimSpace(path), " (deleted)")
	return strings.TrimPrefix(filepath.Base(path), "-")
}

// the OS lookups, replaceable in tests
var (
	procExe    = osProcExe
	procParent = osProcParent
)

// detectShell returns the shell the recorder was run from, else $SHELL, else bash.
func detectShell() string {
	pid := os.Getppid()
	for depth := 0; pid > 1 && depth < 10; depth++ {
		if exe := procExe(pid); exe != "" && knownShells[shellName(exe)] {
			return exe
		}
		pid = procParent(pid)
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "sh"
}

func osProcExe(pid int) string {
	if runtime.GOOS == "linux" {
		if exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe"); err == nil {
			return strings.TrimSuffix(exe, " (deleted)")
		}
	}
	// macOS and the BSDs have no /proc: ps prints the command, as a path there
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "-")
}

func osProcParent(pid int) int {
	if runtime.GOOS == "linux" {
		if stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
			// "pid (comm) state ppid ...": comm may hold spaces and parentheses, so
			// count from the last ")"
			s := string(stat)
			if fields := strings.Fields(s[strings.LastIndex(s, ")")+1:]); len(fields) >= 2 {
				if ppid, err := strconv.Atoi(fields[1]); err == nil {
					return ppid
				}
			}
		}
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid
}

// The terminal's size for a scripted session, which has no one to ask but may
// still have a terminal: an explicit -cols/-rows wins; otherwise the size of
// whichever of stdout, stderr and stdin is a terminal; otherwise the default.
// (A CI runner has none, so a default is needed; 172x38 is wide enough that
// ordinary output isn't wrapped.)
const (
	defaultCols = 172
	defaultRows = 38
)

func scriptedSize(cols, rows int) (int, int) {
	var ttyCols, ttyRows int
	for _, f := range []*os.File{os.Stdout, os.Stderr, os.Stdin} {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 && h > 0 {
			ttyCols, ttyRows = w, h
			break
		}
	}
	return cmp.Or(cols, ttyCols, defaultCols), cmp.Or(rows, ttyRows, defaultRows)
}

func describeShell(shell string) string { return fmt.Sprintf("%s (%s)", shellName(shell), shell) }
