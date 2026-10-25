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
	bash := testBash(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "cmds.txt")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "terminal.jsonl")
	t.Setenv("HOME", dir) // no rc file of the person's
	cfg := &libtrecs.RecordingConfig{
		TerminalEnabled: true, TerminalCommand: bash, ScriptFile: file, Cols: 100, Rows: 30,
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

// Old fish (3.1) draws its prompt twice at startup, 2 ms apart. The second prompt mark must not be taken for the end of the
// first command: that left one "the shell is ready" signal over, so that every later command was released as soon as it was
// entered, and the next line of the script was typed into `apt update` while it ran.
func TestAPromptDrawnTwiceAtStartDoesNotReleaseTheNextLineWhileACommandRuns(t *testing.T) {
	bash := testBash(t)
	rc := "PS1='\\[\\e]133;A\\a\\]$ \\[\\e]133;B\\a\\]'\n" + // the person's own marks, so that the recorder's are not added
		"__once() { [ -n \"$__d\" ] && return; __d=1; (sleep 0.03; printf '\\e]133;B\\a' >/dev/tty) & disown; }\n" +
		"PS0='\\e]133;C\\a'\n" +
		"PROMPT_COMMAND=__once\n"
	dir := t.TempDir()
	file := filepath.Join(dir, "cmds.txt")
	if err := os.WriteFile(file, []byte("true\nsleep 4\necho SECOND-LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "terminal.jsonl")
	t.Setenv("HOME", dir)
	cfg := &libtrecs.RecordingConfig{TerminalEnabled: true, TerminalCommand: bash, ScriptFile: file, Cols: 100, Rows: 30, HumanLike: true}
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
	case <-time.After(40 * time.Second):
		_ = tr.Stop()
		t.Fatal("the session did not end")
	}
	frames, _, _, err := libtrecs.LoadFrames(out)
	if err != nil {
		t.Fatal(err)
	}
	// the marks in order, with the time of the frame that held them
	type mark struct {
		kind byte
		at   int64
	}
	var marks []mark
	for _, f := range frames {
		d := string(f.Data)
		for i := 0; i+7 < len(d); i++ {
			if strings.HasPrefix(d[i:], "\x1b]133;") {
				marks = append(marks, mark{d[i+6], f.Timestamp})
			}
		}
	}
	// `sleep 4` is the second command started (C). The prompt after it (B) is when the shell is ready; the next line must then be
	// typed like a person types it (a pause, then the keys: a second or more) before that line is entered (the next C). When the
	// signal was left over, the line was typed into the running sleep, and entered the moment the shell was back: no time at all.
	cs, sleepEnd, nextStart := 0, int64(-1), int64(-1)
	for _, m := range marks {
		switch {
		case m.kind == 'C':
			cs++
			if cs == 3 && nextStart < 0 {
				nextStart = m.at
			}
		case m.kind == 'B' && cs == 2 && sleepEnd < 0:
			sleepEnd = m.at
		}
	}
	if sleepEnd < 0 || nextStart < 0 {
		t.Fatalf("the session is not what the test expects: %v", marks)
	}
	if gap := nextStart - sleepEnd; gap < 1000 {
		t.Errorf("the line after `sleep 4` was entered %d ms after its prompt came back: it was typed into the running command", gap)
	}
}

// The same, without -human-like: the first line is entered the moment the first prompt is there, so the second prompt mark
// arrives after it was entered. The shell must be given the moment it needs to finish drawing before the first line goes in.
func TestAPromptDrawnTwiceAtStartIsWaitedOutWithoutHumanLike(t *testing.T) {
	bash := testBash(t)
	rc := "PS1='\\[\\e]133;A\\a\\]$ \\[\\e]133;B\\a\\]'\n" +
		"__once() { [ -n \"$__d\" ] && return; __d=1; (sleep 0.03; printf '\\e]133;B\\a' >/dev/tty) & disown; }\n" +
		"PS0='\\e]133;C\\a'\n" +
		"PROMPT_COMMAND=__once\n"
	dir := t.TempDir()
	file := filepath.Join(dir, "cmds.txt")
	if err := os.WriteFile(file, []byte("read -t 1 x; echo got=$x\necho SECOND-LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "terminal.jsonl")
	t.Setenv("HOME", dir)
	cfg := &libtrecs.RecordingConfig{TerminalEnabled: true, TerminalCommand: bash, ScriptFile: file, Cols: 100, Rows: 30}
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
	// The first command waits a second for a line of input. If the next line of the script was typed while it ran, it reads it.
	var all strings.Builder
	for _, f := range frames {
		all.WriteString(f.Data)
	}
	if got := all.String(); strings.Contains(got, "got=echo SECOND-LINE") || !strings.Contains(got, "got=\r\n") {
		t.Errorf("the next line was typed into the command that was running: %q", got)
	}
}
