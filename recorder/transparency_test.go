package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// The recorder only records: a shell started with its prompt marks must behave as the same shell without them. These tests
// run the same typed lines in a shell bare and in one with the integration, in situations in which the integration once
// changed what the person saw or what the shell did (see recorder/fuzz for the fuzzer that found them).

// anyMark is a prompt mark (OSC 133), ended by BEL or by ESC \ : bash, zsh and the integration end theirs with BEL, and fish 4 the ones it
// writes itself with ESC \ . Group 1 is what it says ("A", "D;0").
var anyMark = regexp.MustCompile(`\x1b\]133;([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// typeInto runs the shell on a terminal, types each line when the output has gone quiet, and returns all that was printed with
// the prompt marks taken out.
func typeInto(t *testing.T, shell string, args, env []string, lines []string) (string, bool) {
	t.Helper()
	raw, alive := typeIntoRaw(t, shell, args, env, lines)
	return anyMark.ReplaceAllString(raw, ""), alive
}

// typeIntoRaw is typeInto with the prompt marks left in.
func typeIntoRaw(t *testing.T, shell string, args, env []string, lines []string) (string, bool) {
	t.Helper()
	cmd := exec.Command(shell, args...)
	cmd.Env = append(os.Environ(), env...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ptmx.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var buf syncBuffer
	go func() { _, _ = bufCopy(&buf, ptmx) }()
	quiet := func(d time.Duration) {
		last, n := time.Now(), len(buf.String())
		for time.Since(last) < d && time.Since(last) < 10*time.Second {
			time.Sleep(10 * time.Millisecond)
			if m := len(buf.String()); m != n {
				last, n = time.Now(), m
			}
		}
	}
	quiet(600 * time.Millisecond)
	for _, l := range lines {
		_, _ = ptmx.WriteString(l + "\r")
		quiet(250 * time.Millisecond)
	}
	// is it still there? (set -e used to end it)
	mark := fmt.Sprintf("ALIVE-%d", 40+2)
	_, _ = ptmx.WriteString("builtin printf 'ALIVE-%d\\n' 42\r")
	alive := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(buf.String(), mark) { // only the answer has it: the typed line has the sum, not 42
			alive = true
			break
		}
	}
	quiet(200 * time.Millisecond)
	return buf.String(), alive
}

// bufCopy copies what a terminal prints into dst, and answers what a shell asks of the terminal, as a terminal does: fish 4.1 and
// later asks for the terminal's primary device attributes at startup, and waits ten seconds for the answer before it goes on (a
// shell asked by a recorder gets the same answer: see deviceAttributesReply).
func bufCopy(dst *syncBuffer, src *os.File) (int64, error) {
	b := make([]byte, 4096)
	var n int64
	var tail string // the end of what was read before: a question can be cut between two reads
	for {
		k, err := src.Read(b)
		if k > 0 {
			_, _ = dst.Write(b[:k])
			n += int64(k)
			text := tail + string(b[:k])
			for _, at := range deviceAttributesQuery.FindAllStringIndex(text, -1) {
				if at[1] > len(tail) { // not one that was answered when it was read
					_, _ = src.WriteString(deviceAttributesReply)
				}
			}
			tail = text[max(0, len(text)-8):]
		}
		if err != nil {
			return n, err
		}
	}
}

// bothWays runs a bash with the given rc lines bare and with the integration.
func bothWays(t *testing.T, rc string, lines []string) (bare, marked string, bareAlive, markedAlive bool) {
	t.Helper()
	bash := testBash(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("PS1='P> '\nHISTFILE=/dev/null\n"+rc+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "TERM=xterm-256color", "TMPDIR=" + t.TempDir(), "LC_ALL=C.UTF-8"}
	bare, bareAlive = typeInto(t, bash, nil, env, lines)
	integ, err := setupShellIntegration(bash)
	if err != nil || integ == nil {
		t.Fatalf("no integration: %v", err)
	}
	defer integ.cleanup()
	marked, markedAlive = typeInto(t, bash, integ.args, append(env, integ.env...), lines)
	return
}

func TestErrexitDoesNotEndAMarkedShellOverAnExcusedFailure(t *testing.T) {
	// `false && true` leaves $? at 1 and the shell lives, as bash excuses it; the functions the integration runs at the prompt
	// must not turn that status into the failure set -e ends the shell for.
	bare, marked, ba, ma := bothWays(t, "set -e", []string{"false && true", "echo status=$?", "ls /nonexistent-dir-x && echo no"})
	if !ba {
		t.Fatalf("the bare shell did not survive, so the test says nothing:\n%q", bare)
	}
	if !ma {
		t.Fatalf("the marked shell ended over a failure bash excuses:\n%q", marked)
	}
	if !strings.Contains(marked, "status=1") {
		t.Errorf("$? must still be the status of the command before the prompt:\n%q", marked)
	}
}

func TestAnErrTrapDoesNotFireForThePromptFunctions(t *testing.T) {
	bare, marked, _, ma := bothWays(t, "trap 'echo ERR-TRAP' ERR", []string{"false", "echo ok", "false && true", "echo ok2"})
	if !ma {
		t.Fatal("the marked shell ended")
	}
	if a, b := strings.Count(bare, "ERR-TRAP"), strings.Count(marked, "ERR-TRAP"); a != b {
		t.Errorf("the ERR trap fired %d times bare and %d times with the integration:\n%q", a, b, marked)
	}
}

func TestXtraceShowsNothingOfTheIntegrationButTheCalls(t *testing.T) {
	_, marked, _, ma := bothWays(t, "set -x", []string{"echo hi"})
	if !ma {
		t.Fatal("the marked shell ended")
	}
	for _, leak := range []string{"local s", "printf '\\e]133", "case $PS1", "case ${PS1", "return 0"} {
		if strings.Contains(marked, leak) {
			t.Errorf("set -x shows the inside of the integration (%q):\n%q", leak, marked)
		}
	}
}

func TestAnAliasOrAFunctionOfThePersonsDoesNotChangeTheIntegration(t *testing.T) {
	for name, rc := range map[string]string{
		"alias printf":   "alias printf='echo ALIASED'",
		"function print": "printf() { echo \"my printf: $*\"; }",
		"alias declare":  "alias declare='echo ALIASED'",
		"alias local":    "alias local='echo ALIASED'",
		"alias case":     "alias case='echo ALIASED'",
		"alias set":      "alias set='echo ALIASED'",
		"alias shopt":    "alias shopt='echo ALIASED'",
		"set -u":         "set -u",
		"unset PS2":      "unset PS2",
		"set -u, no PS2": "unset PS2; set -u",
	} {
		t.Run(name, func(t *testing.T) {
			bare, marked, _, ma := bothWays(t, rc, []string{"echo one", "false", "echo $?"})
			if !ma {
				t.Fatalf("the marked shell ended:\n%q", marked)
			}
			// An alias for a word of the integration makes it a syntax error, which prints no "ALIASED" at all.
			if strings.Contains(marked, "syntax error") != strings.Contains(bare, "syntax error") {
				t.Errorf("the integration does not parse:\n bare   %q\n marked %q", bare, marked)
			}
			if strings.Contains(marked, "ALIASED") != strings.Contains(bare, "ALIASED") || strings.Contains(marked, "my printf") != strings.Contains(bare, "my printf") ||
				strings.Contains(marked, "unbound") != strings.Contains(bare, "unbound") {
				t.Errorf("the integration shows in the output:\n bare   %q\n marked %q", bare, marked)
			}
		})
	}
}

func TestTheZshPromptHoldsNoMarkupWhenItsPercentSignsAreNotExpanded(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("unsetopt PROMPT_PERCENT\nPROMPT='P> '\nHISTFILE=/dev/null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	integ, err := setupShellIntegration(zsh)
	if err != nil || integ == nil {
		t.Fatalf("no integration: %v", err)
	}
	defer integ.cleanup()
	env := append([]string{"HOME=" + home, "TERM=xterm-256color"}, integ.env...)
	out, alive := typeInto(t, zsh, integ.args, env, []string{"echo hi"})
	if !alive {
		t.Fatalf("the shell ended: %q", out)
	}
	if strings.Contains(out, "%{") || strings.Contains(out, "%}") {
		t.Errorf("the marks show as text in the prompt: %q", out)
	}
}

func TestTheShellsTerminalHasTheSettingsOfThePersonsTerminal(t *testing.T) {
	// the person's terminal: Backspace is ^H, interrupt is ^T, Ctrl+S does not stop the output
	outerM, outerS, err := pty.Open()
	if err != nil {
		t.Skip("no pty")
	}
	defer func() { _ = outerM.Close() }()
	defer func() { _ = outerS.Close() }()
	tio, err := unix.IoctlGetTermios(int(outerS.Fd()), ioctlReadTermios)
	if err != nil {
		t.Skipf("no termios: %v", err)
	}
	tio.Cc[unix.VERASE], tio.Cc[unix.VINTR] = 0x08, 0x14
	tio.Iflag &^= unix.IXON
	if err := unix.IoctlSetTermios(int(outerS.Fd()), ioctlWriteTermios, tio); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("stty", "-a")
	ptmx, err := startShell(cmd, readTermios(outerS), func(*os.File) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ptmx.Close() }()
	var buf syncBuffer
	go func() { _, _ = bufCopy(&buf, ptmx) }()
	_ = cmd.Wait()
	time.Sleep(200 * time.Millisecond)
	out := buf.String()
	for _, want := range []string{"erase = ^H", "intr = ^T", "-ixon"} {
		if !strings.Contains(out, want) {
			t.Errorf("the shell's terminal lacks %q:\n%s", want, out)
		}
	}
}

func TestTheShellsTerminalHasItsSizeBeforeTheShellRuns(t *testing.T) {
	cmd := exec.Command("sh", "-c", "stty size")
	ptmx, err := startShell(cmd, nil, func(p *os.File) { _ = pty.Setsize(p, &pty.Winsize{Rows: 33, Cols: 111}) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ptmx.Close() }()
	var buf syncBuffer
	go func() { _, _ = bufCopy(&buf, ptmx) }()
	_ = cmd.Wait()
	time.Sleep(200 * time.Millisecond)
	if !strings.Contains(buf.String(), "33 111") {
		t.Errorf("the shell was started on a terminal of another size: %q", buf.String())
	}
}

func TestZshXtraceShowsNothingOfTheIntegration(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("setopt xtrace\nPROMPT='P> '\nHISTFILE=/dev/null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	integ, err := setupShellIntegration(zsh)
	if err != nil || integ == nil {
		t.Fatalf("no integration: %v", err)
	}
	defer integ.cleanup()
	env := append([]string{"HOME=" + home, "TERM=xterm-256color"}, integ.env...)
	out, alive := typeInto(t, zsh, integ.args, env, []string{"echo hi", "false", "echo $?"})
	if !alive {
		t.Fatalf("the shell ended: %q", out)
	}
	if strings.Contains(out, "trecs") || strings.Contains(out, "emulate -L") || strings.Contains(out, "133") {
		t.Errorf("setopt xtrace shows the integration: %q", out)
	}
}

func TestAMultiLineFishPromptKeepsItsLines(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("no fish")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "fish")
	_ = os.MkdirAll(cfg, 0o755)
	if err := os.WriteFile(filepath.Join(cfg, "config.fish"), []byte("function fish_prompt\n echo top\n echo -n 'P> '\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "TERM=xterm-256color"}
	bare, bareAlive := typeInto(t, fish, nil, env, []string{"echo hi"})
	integ, err := setupShellIntegration(fish)
	if err != nil || integ == nil {
		t.Fatalf("no integration: %v", err)
	}
	defer integ.cleanup()
	marked, alive := typeInto(t, fish, integ.args, append(env, integ.env...), []string{"echo hi"})
	if !bareAlive || !alive {
		t.Fatalf("a shell ended (bare %v, marked %v)", bareAlive, alive)
	}
	// what the person sees, without the escape sequences (fish repaints in an order of its own): the same lines, and the
	// first line of the prompt is still on a line of its own above the second
	// (a sequence ends as it is told to: fish 4 ends the ones it writes with ESC \, not BEL, and asks the terminal things of its own)
	seen := func(x string) string {
		return regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|P[^\x1b]*\x1b\\|\(B|[=>])`).ReplaceAllString(x, "")
	}
	lines := regexp.MustCompile(`top\s*\r?\n\s*P> `)
	if !lines.MatchString(seen(marked)) || strings.Count(seen(bare), "top") != strings.Count(seen(marked), "top") {
		t.Errorf("the prompt lost its lines:\n bare   %q\n marked %q", seen(bare), seen(marked))
	}
}
