package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testBash is a bash the prompt marks work in (4.4 or newer), or the test is skipped. /bin/bash is not one on macOS, which
// ships 3.2: Homebrew's is looked for too.
func testBash(t *testing.T) string {
	t.Helper()
	cands := []string{"/bin/bash", "/opt/homebrew/bin/bash", "/usr/local/bin/bash"}
	if p, err := exec.LookPath("bash"); err == nil {
		cands = append(cands, p)
	}
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil && bashSupportsMarks(p) {
			return p
		}
	}
	t.Skip("no bash 4.4 or newer here (macOS's is 3.2: brew install bash)")
	return ""
}

// A bash too old for the integration is not started as if it were marked: a scripted recording then says so at once, rather
// than waiting a minute for a prompt mark that never comes.
func TestAnOldBashIsNotTakenForAMarkedShell(t *testing.T) {
	for ver, want := range map[string]bool{"3 2": false, "4 3": false, "4 4": true, "5 0": true, "5 2": true} {
		dir := t.TempDir()
		fake := filepath.Join(dir, "bash")
		if err := os.WriteFile(fake, []byte("#!/bin/sh\necho "+ver+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if got := bashSupportsMarks(fake); got != want {
			t.Errorf("bash %s: %v, want %v", ver, got, want)
		}
		integ, err := setupShellIntegration(fake)
		if err != nil {
			t.Fatal(err)
		}
		if (integ != nil) != want {
			t.Errorf("bash %s: integration %v, want %v", ver, integ != nil, want)
		}
		if integ != nil {
			integ.cleanup()
		}
	}
}

// For a person at the keyboard and for a script alike, an old bash is a shell that cannot be marked: the checks that run
// before recording (and offer another shell, or refuse) are the ones that say so, not only the start of the session.
func TestAnOldBashGetsTheUnmarkedShellTreatment(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "bash")
	if err := os.WriteFile(old, []byte("#!/bin/sh\necho 3 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if supportsMarks(old, false) {
		t.Fatal("bash 3.2 was taken for a shell that can be marked")
	}
	avail := map[string]string{"zsh": "/bin/zsh"}
	if w := unmarkedInteractiveWarning(old, avail); !strings.Contains(w, "older than bash 4.4") || !strings.Contains(w, "/bin/zsh") {
		t.Errorf("the warning: %s", w)
	}
	if m := unmarkedRefusal(old, false, avail); !strings.Contains(m, "older than bash 4.4") || !strings.Contains(m, "-cmd zsh") {
		t.Errorf("the refusal: %s", m)
	}
	t.Setenv("PATH", dir) // the only bash there is
	if _, ok := markShellsAvailable()["bash"]; ok {
		t.Error("an old bash was offered as a shell to record with")
	}
}
