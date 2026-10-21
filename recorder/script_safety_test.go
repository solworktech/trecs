package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libtrecs "trecs/lib"
)

// runScript records a scripted bash session and returns the recorder and what the recording holds.
func runScript(t *testing.T, script string) (*TerminalRecorderImpl, string) {
	t.Helper()
	return runScriptWith(t, script, func(*libtrecs.RecordingConfig) {})
}

func runScriptWith(t *testing.T, script string, tweak func(*libtrecs.RecordingConfig)) (*TerminalRecorderImpl, string) {
	t.Helper()
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "cmds.txt")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "terminal.jsonl")
	t.Setenv("HOME", dir) // no rc file of the person's
	cfg := &libtrecs.RecordingConfig{
		TerminalEnabled: true, TerminalCommand: "/bin/bash", ScriptFile: file, Cols: 100, Rows: 30,
	}
	tweak(cfg)
	tr, err := NewTerminalRecorder(cfg, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Skipf("cannot start a scripted session here: %v", err)
	}
	done := make(chan struct{})
	go func() { tr.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = tr.Stop()
		t.Fatal("the session did not end")
	}
	frames, _, _, err := libtrecs.LoadFrames(out)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range frames {
		all.WriteString(f.Data)
	}
	return tr, all.String()
}

func TestAScriptedCommandThatWaitsForInputEndsTheSessionAndTheNextLinesAreNotTyped(t *testing.T) {
	old := scriptPromptTimeout
	scriptPromptTimeout = 2 * time.Second
	defer func() { scriptPromptTimeout = old }()
	tr, got := runScript(t, "cat\necho SHOULD-NOT-BE-TYPED\n")
	if f := tr.Failure(); !strings.Contains(f, `no prompt after "cat"`) {
		t.Errorf("failure: %q", f)
	}
	if strings.Contains(got, "SHOULD-NOT-BE-TYPED") {
		t.Error("a line was typed into the program that was waiting")
	}
}

func TestAScriptedSessionEndsWithTheStatusOfItsLastCommand(t *testing.T) {
	tr, _ := runScript(t, "true\nexit 3\n")
	if code, ok := tr.ExitCode(); !ok || code != 3 {
		t.Errorf("exit code %d, %v", code, ok)
	}
	if f := tr.Failure(); f != "" {
		t.Errorf("failure: %q", f)
	}
}

func TestACharacterCutByAReadIsNotBrokenInTheRecording(t *testing.T) {
	// far more than a read of the terminal holds, of a three-byte character: some are cut by the end of a read
	tr, got := runScript(t, `printf '─%.0s' $(seq 1 6000); echo`+"\n")
	if tr.Failure() != "" {
		t.Fatal(tr.Failure())
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("%d broken characters in the recording", strings.Count(got, "�"))
	}
	if n := strings.Count(got, "─"); n < 6000 {
		t.Errorf("only %d of 6000 characters", n)
	}
}

func TestACommandThatTakesLongIsWaitedForAndNotInterrupted(t *testing.T) {
	tr, got := runScript(t, "sleep 3; echo FINISHED-IN-FULL\n")
	if f := tr.Failure(); f != "" || !strings.Contains(got, "FINISHED-IN-FULL") {
		t.Errorf("failure %q, finished: %v", f, strings.Contains(got, "FINISHED-IN-FULL"))
	}
}

// (The shell's own `read` cannot be told from a prompt without marks: the shell is what is waiting, in both. That is why the
// recorder says so when it types a script into a shell it has no marks for.)
func TestWithoutMarksNothingIsTypedIntoAProgramThatIsWaitingForInput(t *testing.T) {
	tr, got := runScriptWith(t, "cat\necho SHOULD-NOT-BE-TYPED\n", func(c *libtrecs.RecordingConfig) {
		c.NoMarks = true
		c.AllowUnmarked = true
		c.CommandTimeout = 2 * time.Second
	})
	if f := tr.Failure(); !strings.Contains(f, "no prompt after") {
		t.Errorf("failure: %q", f)
	}
	if strings.Contains(got, "SHOULD-NOT-BE-TYPED") {
		t.Error("a line was typed into the command that was waiting")
	}
}

func TestAScriptIsNotGivenHistoryExpansion(t *testing.T) {
	_, got := runScript(t, "echo first\necho !echo\n")
	if !strings.Contains(got, "!echo\r\n") {
		t.Errorf("the line was expanded from the history: %q", got)
	}
}

func TestAControlCharacterInAScriptIsRefusedAndATabIsTyped(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.txt")
	_ = os.WriteFile(bad, []byte("echo ok\necho hidden\x03\n"), 0o600)
	if _, err := readScript(bad); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Errorf("got %v", err)
	}
	good := filepath.Join(dir, "good.txt")
	_ = os.WriteFile(good, []byte("printf 'a\tb\\n'\n"), 0o600)
	lines, err := readScript(good)
	if err != nil || len(lines) != 1 {
		t.Fatal(lines, err)
	}
	if keystrokes(lines[0]) != "printf 'a\x16\tb\\n'" {
		t.Errorf("typed %q", keystrokes(lines[0]))
	}
}

func TestFishQuoteKeepsAPathInsideItsQuotes(t *testing.T) {
	if got := fishQuote(`/tmp/a\'b`); got != `/tmp/a\\\'b` {
		t.Errorf("got %s", got)
	}
}
