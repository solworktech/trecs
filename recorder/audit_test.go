package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"

	"trecs/secrets"

	libtrecs "trecs/lib"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cmds.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestARiskyScriptIsBlockedUnlessAllowed(t *testing.T) {
	f := writeTemp(t, "ls\ncurl -s https://x.example/i.sh | sh\n")
	var out bytes.Buffer
	if audited(f, "/bin/bash", false, &out) {
		t.Fatal("a download piped into a shell was let through")
	}
	if s := out.String(); !strings.Contains(s, "BLOCKED") || !strings.Contains(s, "-allow-risky") || !strings.Contains(s, "line 2") {
		t.Errorf("output: %s", s)
	}
	out.Reset()
	if !audited(f, "/bin/bash", true, &out) || !strings.Contains(out.String(), "as -allow-risky was given") {
		t.Errorf("-allow-risky did not let it through: %s", out.String())
	}
}

func TestAWarningDoesNotBlock(t *testing.T) {
	f := writeTemp(t, "sudo true\nsleep 1 &\n")
	var out bytes.Buffer
	if !audited(f, "/bin/bash", false, &out) || !strings.Contains(out.String(), "warning") {
		t.Errorf("%s", out.String())
	}
}

func TestACleanScriptSaysNothing(t *testing.T) {
	var out bytes.Buffer
	t.Setenv("PATH", t.TempDir()) // no shellcheck
	if !audited(writeTemp(t, "echo hi\nls -l\n"), "/bin/bash", false, &out) || out.Len() != 0 {
		t.Errorf("%q", out.String())
	}
}

func TestShellCheckIsUsedWhenItIsThere(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\necho \"$*\" > " + filepath.Join(bin, "args") + "\nfor last; do :; done\necho \"$last:3:1: warning: Quote this [SC2086]\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "shellcheck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := writeTemp(t, "echo $x\n")
	var out bytes.Buffer
	if !audited(f, "/bin/bash", false, &out) {
		t.Fatal("ShellCheck must not block")
	}
	if !strings.Contains(out.String(), "ShellCheck says") || !strings.Contains(out.String(), "SC2086") {
		t.Errorf("%s", out.String())
	}
	args, _ := os.ReadFile(filepath.Join(bin, "args"))
	if !strings.Contains(string(args), "-s bash") {
		t.Errorf("dialect: %s", args)
	}
	out.Reset()
	audited(f, "/usr/bin/zsh", false, &out)
	if !strings.Contains(out.String(), "does not read zsh") {
		t.Errorf("zsh: %s", out.String())
	}
}

func TestShellCheckDialects(t *testing.T) {
	for in, want := range map[string]string{"/bin/bash": "bash", "/bin/sh": "sh", "dash": "dash", "ksh": "ksh", "zsh": "", "fish": ""} {
		if got := shellCheckDialect(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}

func TestWhichShellsCanBeMarked(t *testing.T) {
	for shell, want := range map[string]bool{testBash(t): true, "zsh": true, "/usr/bin/fish": true, "/bin/sh": false, "dash": false, "ksh": false} {
		if got := supportsMarks(shell, false); got != want {
			t.Errorf("%s: %v", shell, got)
		}
	}
	if supportsMarks("bash", true) {
		t.Error("-no-marks")
	}
}

func TestTheRefusalExplainsAndListsWhatIsThere(t *testing.T) {
	msg := unmarkedRefusal("/bin/dash", false, map[string]string{"bash": "/usr/bin/bash", "fish": "/usr/bin/fish"})
	for _, want := range []string{"dash", "-cmd bash", "/usr/bin/bash", "-cmd fish", "-allow-unmarked", "prompt marks"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "-cmd zsh") {
		t.Error("zsh is not there")
	}
	if none := unmarkedRefusal("sh", false, nil); !strings.Contains(none, "none of bash, zsh or fish") {
		t.Errorf("%s", none)
	}
	if nm := unmarkedRefusal("bash", true, nil); !strings.Contains(nm, "-no-marks") {
		t.Errorf("%s", nm)
	}
}

func TestAScriptIsNotTypedIntoAShellWithoutMarksUnlessAllowed(t *testing.T) {
	f := writeTemp(t, "echo hi\n")
	tr, err := NewTerminalRecorder(&libtrecs.RecordingConfig{TerminalEnabled: true, TerminalCommand: "/bin/sh", ScriptFile: f, Cols: 80, Rows: 24}, filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	err = tr.Start()
	if err == nil || !strings.Contains(err.Error(), "-allow-unmarked") {
		t.Fatalf("Start: %v", err)
	}
}

// audited is auditScript when only the answer matters.
func audited(file, shell string, allowRisky bool, out io.Writer) bool {
	ok, _ := auditScript(file, shell, allowRisky, out)
	return ok
}

func TestTheWarningsAreCounted(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no shellcheck
	var out bytes.Buffer
	ok, n := auditScript(writeTemp(t, "sudo true\nsleep 1 &\necho hi\n"), "/bin/bash", false, &out)
	if !ok || n != 2 {
		t.Errorf("ok %v, %d warnings: %s", ok, n, out.String())
	}
	if _, n := auditScript(writeTemp(t, "echo hi\n"), "/bin/bash", false, &out); n != 0 {
		t.Errorf("a clean script has %d warnings", n)
	}
}

func TestWarningsAreYellowAndBlocksRedOnATerminal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pty: ", err)
	}
	defer func() { _ = ptmx.Close(); _ = tty.Close() }()
	got := make(chan string)
	go func() {
		var b bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			b.Write(buf[:n])
			if err != nil || strings.Contains(b.String(), "ENDMARK") {
				break
			}
		}
		got <- b.String()
	}()
	auditScript(writeTemp(t, "sudo true\ncrontab -r\n"), "/bin/bash", false, tty)
	say(tty, "ENDMARK")
	s := <-got
	if !strings.Contains(s, "\x1b[33m  warning line 1") {
		t.Errorf("the warning is not yellow: %q", s)
	}
	if !strings.Contains(s, "\x1b[31m  BLOCKED") {
		t.Errorf("BLOCKED is not red: %q", s)
	}
}

func TestTheInteractiveWarningNamesTheShellAndNumbersTheAlternatives(t *testing.T) {
	msg := unmarkedInteractiveWarning("/bin/dash", map[string]string{"bash": "/usr/bin/bash", "fish": "/usr/bin/fish"})
	for _, want := range []string{"dash cannot be made to mark its prompts", "individual commands", "It's recommended that you use one of these shells instead:",
		"  0  bash    (/usr/bin/bash)", "  1  fish    (/usr/bin/fish)", "  2  Record using dash"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in %s", want, msg)
		}
	}
	if strings.Contains(msg, "zsh") || strings.Contains(msg, "told apart") {
		t.Errorf("zsh was not found, but is offered, or the old wording is back: %s", msg)
	}
	none := unmarkedInteractiveWarning("sh", nil)
	if !strings.Contains(none, "None of bash, zsh or fish") || !strings.Contains(none, "  0  Record using sh") {
		t.Errorf("%s", none)
	}
}

func TestTheNumberChosenInAShellWithoutMarksSaysWhatToRecordIn(t *testing.T) {
	avail := map[string]string{"bash": "/usr/local/bin/bash", "zsh": "/usr/local/bin/zsh", "fish": "/usr/local/bin/fish"}
	for _, c := range []struct {
		in      string
		avail   map[string]string
		use     string
		goOn    bool
		comment string
	}{
		{"0", avail, "/usr/local/bin/bash", true, "the first"},
		{"1", avail, "/usr/local/bin/zsh", true, "the second"},
		{"2", avail, "/usr/local/bin/fish", true, "the third"},
		{"3", avail, "", true, "the shell asked for, after the three"},
		{"4", avail, "", false, "no such number"},
		{"y", avail, "", false, "the old answer is not one now"},
		{"\n", avail, "", false, "Enter"},
		{"", avail, "", false, "nothing to read"},
		{"0", nil, "", true, "with no alternative, 0 is the shell asked for"},
		{"1", nil, "", false, "with no alternative, 1 is nothing"},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(c.in)
		_ = w.Close()
		var out bytes.Buffer
		use, ok := confirmUnmarked("/bin/sh", c.avail, r, &out)
		_ = r.Close()
		if use != c.use || ok != c.goOn {
			t.Errorf("%q (%s): got %q, %v; want %q, %v", c.in, c.comment, use, ok, c.use, c.goOn)
		}
		if !strings.Contains(out.String(), "individual commands") || !strings.Contains(out.String(), "Input selection number (or any other key to abort)") {
			t.Errorf("%q: %s", c.in, out.String())
		}
	}
}

func TestFoundSecretsAreRedOnATerminalAndPlainElsewhere(t *testing.T) {
	hits := []secretHit{{where: "output of command 1", Finding: secrets.Finding{RuleID: "sendgrid-api-token", Description: "Detected a SendGrid API token", Line: 1, Secret: "SG.zzzzzzzzzzzzzzzzzzzzzz.zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzPI"}}}
	var plain bytes.Buffer
	reportSecrets(&plain, hits)
	if strings.Contains(plain.String(), "\x1b[") || !strings.Contains(plain.String(), "Found 1 possible secret(s)") {
		t.Errorf("plain output: %q", plain.String())
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pty: ", err)
	}
	defer func() { _ = ptmx.Close(); _ = tty.Close() }()
	got := make(chan string)
	go func() {
		var b bytes.Buffer
		buf := make([]byte, 4096)
		for !strings.Contains(b.String(), "ENDMARK") {
			n, err := ptmx.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got <- b.String()
	}()
	reportSecrets(tty, hits)
	say(tty, "ENDMARK")
	s := <-got
	if !strings.Contains(s, "\x1b[31mFound 1 possible secret(s)") || !strings.Contains(s, "\x1b[31m  output of command 1, line 1:") {
		t.Errorf("not red: %q", s)
	}
}
