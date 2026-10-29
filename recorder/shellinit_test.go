package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `trecs shell-init`: the file marks a shell the recorder did not start, and does nothing where it is not meant to.

func initFile(t *testing.T, kind string) string {
	t.Helper()
	script, err := shellInitScript(kind, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "marks."+kind)
	if err := os.WriteFile(p, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func marksIn(raw string) []string {
	var out []string
	for _, m := range anyMark.FindAllStringSubmatch(raw, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestEveryShellInitFileSaysWhatItIsAtTheTopAndParses(t *testing.T) {
	for _, kind := range shellInitShells {
		script, err := shellInitScript(kind, "test")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"WHAT THIS IS FOR", "HOW TO USE IT", "WHAT IT DOES, WHEN LOADED", "IF YOU LOAD IT FROM A STARTUP FILE", "does nothing at all if"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s: the header lacks %q", kind, want)
			}
		}
		if !strings.HasPrefix(script, "# ") {
			t.Errorf("%s: the file does not start with its explanation", kind)
		}
		if _, err := exec.LookPath(kind); err != nil {
			continue
		}
		check := map[string][]string{"bash": {"-n"}, "zsh": {"-n"}, "fish": {"--no-execute"}}[kind]
		cmd := exec.Command(kind, append(check, initFile(t, kind))...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not read the file: %v\n%s", kind, err, out)
		}
	}
}

func TestTheBashFileMarksAnInteractiveShellAndKeepsTheStatusAndTheAliases(t *testing.T) {
	bash := testBash(t)
	file := initFile(t, "bash")
	home := t.TempDir()
	rc := "PS1='P> '\nHISTFILE=/dev/null\nalias printf='echo ALIAS-BROKE'\n. " + file + "\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "TERM=xterm-256color", "LC_ALL=C.UTF-8"}
	raw, alive := typeIntoRaw(t, bash, nil, env, []string{"false", "echo status=$?", "echo hi"})
	if !alive {
		t.Fatal("the shell ended")
	}
	marks := strings.Join(marksIn(raw), " ")
	for _, want := range []string{"A", "B", "C", "D;1", "D;0"} {
		if !strings.Contains(marks, want) {
			t.Errorf("no mark %q in %q", want, marks)
		}
	}
	if !strings.Contains(raw, "status=1") {
		t.Errorf("the status of the command before was lost: %q", raw)
	}
	if strings.Contains(raw, "ALIAS-BROKE") {
		t.Errorf("an alias of the person's was run by the file: %q", raw)
	}
}

func TestTheBashFileCanBeEvaluatedAtThePromptToo(t *testing.T) {
	bash := testBash(t)
	file := initFile(t, "bash")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("PS1='P> '\nHISTFILE=/dev/null\nalias printf='echo ALIAS-BROKE'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "TERM=xterm-256color", "LC_ALL=C.UTF-8"}
	raw, alive := typeIntoRaw(t, bash, nil, env, []string{`eval "$(cat ` + file + `)"`, "echo hi"})
	if !alive || !strings.Contains(strings.Join(marksIn(raw), " "), "D;0") {
		t.Errorf("not marked after eval (alive %v): %q", alive, raw)
	}
	if strings.Contains(raw, "ALIAS-BROKE") {
		t.Errorf("an alias of the person's was run by the file: %q", raw)
	}
}

func TestTheBashFileDoesNothingWhereTheShellIsNotInteractiveOrNotOnATerminal(t *testing.T) {
	bash := testBash(t)
	file := initFile(t, "bash")
	probe := `. ` + file + `; echo "[PS1=${PS1-unset}][PC=${PROMPT_COMMAND-unset}][PS0=${PS0-unset}]"; type __trecs_pre 2>&1 | head -1`
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "PS1=", "TERM=xterm-256color")
	run := func(args ...string) string {
		cmd := exec.Command(bash, args...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		_ = cmd.Run()
		return out.String()
	}
	for name, got := range map[string]string{
		"a script":               run("--norc", "-c", probe),
		"interactive, no tty":    run("--norc", "-i", "-c", probe),
		"a login script (no -i)": run("--norc", "--login", "-c", probe),
	} {
		if strings.Contains(got, "\x1b]133") || strings.Contains(got, "__trecs_pre is a function") || !strings.Contains(got, "[PC=unset][PS0=unset]") {
			t.Errorf("%s: the file did something: %q", name, got)
		}
	}
}

func TestTheZshAndFishFilesMarkAnInteractiveShell(t *testing.T) {
	for _, kind := range []string{"zsh", "fish"} {
		shell, err := exec.LookPath(kind)
		if err != nil {
			t.Logf("no %s", kind)
			continue
		}
		file := initFile(t, kind)
		home := t.TempDir()
		env := []string{"HOME=" + home, "TERM=xterm-256color", "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "ZDOTDIR=" + home}
		var args []string
		switch kind {
		case "zsh":
			if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("PS1='P> '\n. "+file+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		case "fish":
			cfg := filepath.Join(home, ".config", "fish")
			_ = os.MkdirAll(cfg, 0o755)
			if err := os.WriteFile(filepath.Join(cfg, "config.fish"), []byte("function fish_prompt\n echo -n 'P> '\nend\nsource "+file+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		raw, alive := typeIntoRaw(t, shell, args, env, []string{"false", "echo hi"})
		if !alive {
			t.Errorf("%s ended", kind)
		}
		marks := strings.Join(marksIn(raw), " ")
		for _, want := range []string{"A", "B", "C", "D"} {
			if !strings.Contains(marks, want) {
				t.Errorf("%s: no mark %q in %q", kind, want, marks)
			}
		}
	}
}

func TestShellInitSaysWhichShellsThereAreFilesForAndReadsTheLoginShell(t *testing.T) {
	var out, errOut bytes.Buffer
	if rc := runShellInit([]string{"ksh"}, &out, &errOut); rc != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "bash, zsh, fish") {
		t.Errorf("rc %d, out %q, err %q", rc, out.String(), errOut.String())
	}
	t.Setenv("SHELL", "/usr/bin/zsh")
	out.Reset()
	if rc := runShellInit(nil, &out, &errOut); rc != 0 || !strings.Contains(out.String(), "for zsh") {
		t.Errorf("rc %d, out %.80q", rc, out.String())
	}
	t.Setenv("SHELL", "/bin/dash")
	errOut.Reset()
	if rc := runShellInit(nil, &out, &errOut); rc != 2 {
		t.Errorf("a login shell with no file should be asked about: rc %d", rc)
	}
}
