package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestReadScript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmds.txt")
	_ = os.WriteFile(path, []byte("# comment\r\nls -al\r\n\r\n   \n  # indented comment\necho \"a\nb\"\nprintf x \\\n  y\n"), 0o644)
	got, err := readScript(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ls -al", "echo \"a", "b\"", "printf x \\", "  y"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
	_ = os.WriteFile(path, []byte("# nothing\n\n"), 0o644)
	if _, err := readScript(path); err == nil || !strings.Contains(err.Error(), "no commands") {
		t.Errorf("an empty script must be an error, got %v", err)
	}
	if _, err := readScript(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing file must be an error")
	}
}

func TestNoteOutputSignalsEachPromptOnceEvenWhenTheMarkIsCut(t *testing.T) {
	tr := &TerminalRecorderImpl{promptCh: make(chan struct{}, 1)}
	signalled := func() bool {
		select {
		case <-tr.promptCh:
			return true
		default:
			return false
		}
	}
	tr.noteOutput("output, no prompt yet\r\n")
	if signalled() {
		t.Error("no prompt ended")
	}
	tr.noteOutput("$ \x1b]133")
	tr.noteOutput(";B\x07")
	if !signalled() {
		t.Error("a prompt mark cut between two reads must still be seen")
	}
	tr.noteOutput("typed text")
	if signalled() {
		t.Error("the same mark must not signal again on the next read")
	}
	tr.noteOutput("\x1b]133;B\x07")
	if !signalled() {
		t.Error("the next prompt must signal")
	}
}

func TestSetupShellIntegration(t *testing.T) {
	bash, err := setupShellIntegration("/usr/bin/bash")
	if err != nil || bash == nil || len(bash.args) != 2 || bash.args[0] != "--rcfile" {
		t.Fatalf("bash: %+v %v", bash, err)
	}
	rc, _ := os.ReadFile(bash.args[1])
	for _, want := range []string{"~/.bashrc", "133;A", "133;B", "133;C", "133;D", "133;A;k=s"} {
		if !strings.Contains(string(rc), want) {
			t.Errorf("bash rc lacks %q", want)
		}
	}
	if path, err := exec.LookPath("bash"); err == nil {
		if out, err := exec.Command(path, "-n", bash.args[1]).CombinedOutput(); err != nil {
			t.Errorf("bash rc does not parse: %v\n%s", err, out)
		}
	}
	bash.cleanup()
	if _, err := os.Stat(bash.args[1]); err == nil {
		t.Error("cleanup must remove the temporary files")
	}

	zsh, err := setupShellIntegration("zsh")
	if err != nil || zsh == nil {
		t.Fatalf("zsh: %+v %v", zsh, err)
	}
	var zdot string
	for _, e := range zsh.env {
		if v, ok := strings.CutPrefix(e, "ZDOTDIR="); ok {
			zdot = v
		}
	}
	for _, f := range []string{".zshenv", ".zshrc"} {
		if _, err := os.Stat(filepath.Join(zdot, f)); err != nil {
			t.Errorf("zsh: %s not written: %v", f, err)
		}
	}
	zsh.cleanup()

	fish, err := setupShellIntegration("fish")
	if err != nil || fish == nil || len(fish.args) != 2 || fish.args[0] != "--init-command" {
		t.Fatalf("fish: %+v %v", fish, err)
	}
	fish.cleanup()

	if other, err := setupShellIntegration("dash"); other != nil || err != nil {
		t.Errorf("a shell without integration gets none: %+v %v", other, err)
	}
}

var markSeq = regexp.MustCompile("\x1b\\]133;([A-D])(;[^\x07]*)?\x07")

// The bash integration, run for real: a custom prompt (one nothing could guess
// from), a command, and a command continued over two lines. What matters is the
// order of the marks bash writes.
func TestBashIntegrationEmitsMarksInOrder(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if out, err := exec.Command(bash, "-c", `[ "${BASH_VERSINFO[0]}" -gt 4 ] || { [ "${BASH_VERSINFO[0]}" -eq 4 ] && [ "${BASH_VERSINFO[1]}" -ge 4 ]; }`).CombinedOutput(); err != nil {
		t.Skipf("bash older than 4.4 (no PS0): %s", out)
	}
	home := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, ".bashrc"), []byte("PS1='λ '\n"), 0o644)
	integ, err := setupShellIntegration(bash)
	if err != nil || integ == nil {
		t.Fatal(err)
	}
	defer integ.cleanup()
	cmd := exec.Command(bash, integ.args...)
	cmd.Env = append(os.Environ(), "HOME="+home, "TERM=xterm-256color", "HISTFILE=/dev/null")
	cmd.Env = append(cmd.Env, integ.env...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ptmx.Close(); _ = cmd.Process.Kill() }()

	var buf syncBuffer
	go func() { _, _ = io.Copy(&buf, ptmx) }()
	waitFor := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if len(markSeq.FindAllString(buf.String(), -1)) >= n {
				return
			}
		}
		t.Fatalf("timed out waiting for %d marks; got %q", n, buf.String())
	}
	waitFor(3) // D A B: the first prompt
	_, _ = ptmx.WriteString("echo hi\r")
	waitFor(7) // C D A B: it ran, the next prompt
	_, _ = ptmx.WriteString("echo a \\\r")
	waitFor(9) // A;k=s B: bash asks for the rest of the line
	_, _ = ptmx.WriteString("b\r")
	waitFor(13) // C D A B

	var seq []string
	for _, m := range markSeq.FindAllStringSubmatch(buf.String(), -1) {
		seq = append(seq, m[1]+m[2])
	}
	if got, want := strings.Join(seq, " "), "D;0 A B C D;0 A B A;k=s B C D;0 A B"; got != want {
		t.Errorf("marks:\n got  %s\n want %s", got, want)
	}
	if !strings.Contains(buf.String(), "\x1b]133;B\x07") || !strings.Contains(buf.String(), "λ ") {
		t.Error("the custom prompt must be wrapped, not replaced")
	}
	if !strings.Contains(buf.String(), "\x1b]133;A\x07λ \x1b]133;B\x07") {
		t.Errorf("the marks must bracket exactly the prompt: %q", buf.String())
	}
}

// syncBuffer is a bytes.Buffer safe to read while another goroutine writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestFishMajorVersion(t *testing.T) {
	for in, want := range map[string]int{"fish, version 4.1.0\n": 4, "fish, version 3.7.1": 3, "fish, version 4.0.2": 4, "garbage": 0, "": 0} {
		if got := fishMajorVersion(in); got != want {
			t.Errorf("fishMajorVersion(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTerminalQueries(t *testing.T) {
	for _, q := range []string{"\x1b[0c", "\x1b[c"} {
		if !deviceAttributesQuery.MatchString("x" + q + "y") {
			t.Errorf("%q is a device attributes query", q)
		}
	}
	for _, not := range []string{"\x1b[>0c", "\x1b[0m", "\x1b[?25h", "\x1b[m", "plain"} {
		if deviceAttributesQuery.MatchString(not) {
			t.Errorf("%q is not a device attributes query: it must not be answered", not)
		}
	}
	// what fish asks at startup is not passed on to a real terminal
	fish := "\x1b[?u\x1b[>0q\x1b[?1049h\x1b[?1049l\x1b[0chello\x1b[31mred\x1b[m"
	if got := string(terminalQueryRe.ReplaceAll([]byte(fish), nil)); got != "\x1b[?1049h\x1b[?1049lhello\x1b[31mred\x1b[m" {
		t.Errorf("after filtering the questions: %q", got)
	}
}

func TestDetectShellWalksUpToTheShellYouAreIn(t *testing.T) {
	// recorder <- sudo <- fish <- (a terminal emulator)
	exe := map[int]string{os.Getppid(): "/usr/bin/sudo", 500: "/usr/bin/fish", 400: "/usr/bin/kitty"}
	parent := map[int]int{os.Getppid(): 500, 500: 400, 400: 1}
	oldExe, oldParent := procExe, procParent
	defer func() { procExe, procParent = oldExe, oldParent }()
	procExe = func(pid int) string { return exe[pid] }
	procParent = func(pid int) int { return parent[pid] }
	t.Setenv("SHELL", "/bin/bash") // the login shell is bash; the shell in use is fish
	if got := detectShell(); got != "/usr/bin/fish" {
		t.Errorf("detectShell = %q, want the fish the recorder was run from, not $SHELL", got)
	}

	exe[500] = "/usr/bin/make" // no shell among the ancestors: fall back to $SHELL
	if got := detectShell(); got != "/bin/bash" {
		t.Errorf("without a shell above, detectShell = %q, want $SHELL", got)
	}
	t.Setenv("SHELL", "")
	if got := detectShell(); got != "bash" {
		t.Errorf("without anything, detectShell = %q, want bash", got)
	}
	for in, want := range map[string]string{"-zsh": "zsh", "/usr/bin/fish": "fish", "/usr/bin/bash (deleted)": "bash", "dash": "dash"} {
		if got := shellName(in); got != want {
			t.Errorf("shellName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScriptedSizeExplicitWins(t *testing.T) {
	if c, r := scriptedSize(90, 25); c != 90 || r != 25 {
		t.Errorf("explicit size = %dx%d, want 90x25", c, r)
	}
	if c, r := scriptedSize(90, 0); c != 90 || r <= 0 {
		t.Errorf("one dimension explicit = %dx%d: the other must still be filled in", c, r)
	}
	if defaultCols != 172 || defaultRows != 38 {
		t.Errorf("the defaults are %dx%d, want 172x38", defaultCols, defaultRows)
	}
}
