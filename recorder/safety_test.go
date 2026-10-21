package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"trecs/secrets"

	libtrecs "trecs/lib"
)

func TestSplitUTF8HoldsBackACharacterCutByTheRead(t *testing.T) {
	box := []byte("─│") // three bytes each
	for cut := 0; cut <= len(box); cut++ {
		whole, rest := splitUTF8(box[:cut])
		if !utf8.Valid(whole) || string(whole)+string(rest) != string(box[:cut]) {
			t.Errorf("cut %d: %q + %q", cut, whole, rest)
		}
		if cut == 1 || cut == 2 || cut == 4 || cut == 5 {
			if len(rest) == 0 {
				t.Errorf("cut %d: a half character was let through", cut)
			}
		}
	}
	if whole, rest := splitUTF8([]byte("plain ascii")); string(whole) != "plain ascii" || rest != nil {
		t.Errorf("ascii: %q %q", whole, rest)
	}
	if whole, rest := splitUTF8([]byte{'a', 0xff}); len(rest) != 0 || len(whole) != 2 {
		t.Errorf("an invalid byte is not the start of anything: %q %q", whole, rest)
	}
}

func TestTermIsThePersonsOwnUnlessThereIsNone(t *testing.T) {
	for in, want := range map[string]string{"xterm-kitty": "xterm-kitty", "tmux-256color": "tmux-256color", "dumb": "xterm-256color", "": "xterm-256color"} {
		got := withTerm([]string{"A=1", "TERM=" + in})
		if got[1] != "TERM="+want {
			t.Errorf("TERM=%q: %v", in, got)
		}
	}
	if got := withTerm([]string{"A=1"}); got[len(got)-1] != "TERM=xterm-256color" {
		t.Errorf("no TERM: %v", got)
	}
}

func TestRecordingFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := createPrivate(p) // one that is there is made private too
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 || fi.Size() != 0 {
		t.Errorf("mode %v, size %d", fi.Mode().Perm(), fi.Size())
	}
	sr, err := NewSessionRecorder(&libtrecs.RecordingConfig{OutputDir: filepath.Join(dir, "out"), SessionName: "s", TerminalEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = sr
	for _, d := range []string{filepath.Join(dir, "out"), filepath.Join(dir, "out", "s")} {
		if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %v", d, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "out", "s", "terminal.jsonl")); fi.Mode().Perm() != 0o600 {
		t.Errorf("terminal.jsonl has mode %v", fi.Mode().Perm())
	}
}

func TestARecordingThatIsThereIsNotWrittenOverWithoutAsking(t *testing.T) {
	out := t.TempDir()
	cfg := func() *libtrecs.RecordingConfig {
		return &libtrecs.RecordingConfig{OutputDir: out, SessionName: "demo", TerminalEnabled: true, AudioEnabled: true, AudioCodec: "libopus"}
	}
	var msg bytes.Buffer
	// nothing there: go on
	if err := chooseSessionTarget(cfg(), strings.NewReader(""), &msg, false, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(out, "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "demo", "terminal.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// nobody to ask: an error naming what is in the way
	err := chooseSessionTarget(cfg(), strings.NewReader(""), &msg, false, false)
	if err == nil || !strings.Contains(err.Error(), "terminal.jsonl") || !strings.Contains(err.Error(), "-overwrite") {
		t.Errorf("got %v", err)
	}
	// -overwrite says it is wanted
	if err := chooseSessionTarget(cfg(), strings.NewReader(""), &msg, false, true); err != nil {
		t.Errorf("-overwrite: %v", err)
	}
	// a person types YES
	msg.Reset()
	if err := chooseSessionTarget(cfg(), strings.NewReader("YES\n"), &msg, true, false); err != nil {
		t.Errorf("YES: %v", err)
	}
	if !strings.Contains(msg.String(), "already has terminal.jsonl") {
		t.Errorf("the person was not told what is there: %q", msg.String())
	}
	// a different recording in the same folder (no audio file there) is not in the way of an audio-only session
	only := cfg()
	only.TerminalEnabled = false
	if err := chooseSessionTarget(only, strings.NewReader(""), &msg, false, false); err != nil {
		t.Errorf("audio only: %v", err)
	}
	// an end of input is not an answer
	if err := chooseSessionTarget(cfg(), strings.NewReader(""), &msg, true, false); err == nil {
		t.Error("an empty answer went on")
	}
}

func TestPathCompletionOffersWhatIsOnDisk(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"demos", "docs", ".hidden"} {
		_ = os.Mkdir(filepath.Join(dir, d), 0o700)
	}
	_ = os.WriteFile(filepath.Join(dir, "demo.txt"), nil, 0o600)
	got, n := pathCompleter{}.Do([]rune(dir+"/de"), len(dir)+3)
	var names []string
	for _, g := range got {
		names = append(names, string(g))
	}
	if n != 2 || strings.Join(names, " ") != "mo.txt mos/" {
		t.Errorf("got %q, %d", names, n)
	}
	if got, _ := (pathCompleter{}).Do([]rune(dir+"/"), len(dir)+1); len(got) != 3 {
		t.Errorf("hidden names are offered without a dot: %d", len(got))
	}
}

func TestSecretsHoldBackAnUploadUnlessAllowed(t *testing.T) {
	hits := []secretHit{{where: "output of command 2", Finding: findingForTest()}}
	var out bytes.Buffer
	if letSecretsGo(nil, false, strings.NewReader(""), &out, false, false) != true {
		t.Error("nothing found should go on")
	}
	if letSecretsGo(hits, false, strings.NewReader(""), &out, false, false) {
		t.Error("with nobody to ask, a secret went")
	}
	if !letSecretsGo(hits, true, strings.NewReader(""), &out, false, false) {
		t.Error("-allow-secrets did not go on")
	}
	if !letSecretsGo(hits, false, strings.NewReader("YES\n"), &out, true, false) {
		t.Error("YES did not go on")
	}
	for _, no := range []string{"yes\n", "y\n", "\n", ""} {
		if letSecretsGo(hits, false, strings.NewReader(no), &out, true, false) {
			t.Errorf("%q went on", no)
		}
	}
	if strings.Contains(out.String(), "wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx") {
		t.Error("the secret itself was printed")
	}
	if !strings.Contains(out.String(), "ghp_…Ux") {
		t.Errorf("the masked secret was not: %q", out.String())
	}
}

func TestARecordingIsScannedForSecretsInWhatWasTypedAndWhatWasPrinted(t *testing.T) {
	data, err := os.ReadFile("../lib/testdata/fish-marked.jsonl")
	if err != nil {
		t.Skip("fixture:", err)
	}
	// add a command's worth of output that holds a token
	extra := `{"timestamp":999999,"data":"\u001b]133;A\u0007$ \u001b]133;B\u0007echo\r\n"}` + "\n" +
		`{"timestamp":1000000,"data":"\u001b]133;C\u0007GITHUB_TOKEN=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\r\n\u001b]133;D;0\u0007"}` + "\n" +
		`{"timestamp":1000100,"data":"\u001b]133;A\u0007$ \u001b]133;B\u0007"}` + "\n"
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	if err := os.WriteFile(path, append(data, []byte(extra)...), 0o600); err != nil {
		t.Fatal(err)
	}
	hits, err := scanRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hits {
		if h.RuleID == "github-pat" && strings.HasPrefix(h.where, "output of command") {
			found = true
		}
	}
	if !found {
		t.Errorf("not found in %+v", hits)
	}
}

func findingForTest() secrets.Finding {
	return secrets.Finding{RuleID: "github-pat", Description: "Uncovered a GitHub Personal Access Token", Line: 3, Secret: "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"}
}
