package lib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectEnvPrefersPrettyName(t *testing.T) {
	cases := map[string]string{
		"NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\n": "Ubuntu 24.04.1 LTS",
		"PRETTY_NAME='Alpine Linux v3.20'\n":                               "Alpine Linux v3.20",
		"PRETTY_NAME=Arch\n":                                               "Arch",
		"PRETTY_NAME=\"Say \\\"hi\\\"  there\"\n":                          "Say \"hi\" there",
	}
	for in, want := range cases {
		got := detectEnv(writeFile(t, in), func(string) string { t.Error("uname must not be asked"); return "" })
		if got != want {
			t.Errorf("os-release %q: got %q, want %q", in, got, want)
		}
	}
}

func TestDetectEnvFallsBackToUname(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	var asked []string
	got := detectEnv(missing, func(flag string) string { asked = append(asked, flag); return "Darwin\n" })
	if got != "Darwin" || len(asked) != 1 || asked[0] != "-o" {
		t.Errorf("got %q asking %v, want Darwin from -o alone", got, asked)
	}
	// a file with no PRETTY_NAME is as good as none
	got = detectEnv(writeFile(t, "NAME=Foo\n"), func(flag string) string {
		if flag == "-o" {
			return "" // a uname without -o
		}
		return "OpenBSD\n"
	})
	if got != "OpenBSD" {
		t.Errorf("got %q, want OpenBSD from -s", got)
	}
	// nothing answers: the Go runtime's name for the OS
	if got = detectEnv(missing, func(string) string { return "" }); got == "" {
		t.Error("want the runtime's OS name when nothing else answers")
	}
}

func TestCleanEnvBoundsTheValue(t *testing.T) {
	if got := cleanEnv("a\x1b[31m\tb\n"); got != "a[31m b" {
		t.Errorf("got %q", got)
	}
	if got := cleanEnv(strings.Repeat("é", 500)); len([]rune(got)) != maxEnvLen {
		t.Errorf("got %d runes, want %d", len([]rune(got)), maxEnvLen)
	}
	if got := ShellName("/usr/local/bin/fish"); got != "fish" {
		t.Errorf("ShellName = %q", got)
	}
}

func TestEnvSurvivesMergeFormatAndEditorSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.jsonl")
	f, _ := os.Create(path)
	enc := json.NewEncoder(f)
	line := NewRecordingMetaLine(100, 30)
	line.Env, line.Shell = "Ubuntu 24.04.1 LTS", "bash"
	_ = enc.Encode(line)
	_ = enc.Encode(TerminalFrame{Timestamp: 5, Data: "\x1b]133;A\x07$ \x1b]133;B\x07ls\r\n"})
	_ = enc.Encode(TerminalFrame{Timestamp: 900, Data: "a b\r\n\x1b]133;D;0\x07"})
	_ = f.Close()

	_, _, meta, err := LoadFrames(path)
	if err != nil || meta.Env != "Ubuntu 24.04.1 LTS" || meta.Shell != "bash" || meta.Width != 100 {
		t.Fatalf("read back %+v, %v", meta, err)
	}
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	_, _, meta, err = LoadFrames(path)
	if err != nil || meta.Env != "Ubuntu 24.04.1 LTS" || meta.Shell != "bash" || meta.Width != 100 || meta.Height != 30 {
		t.Errorf("after an editor save the header is %+v, %v - want it kept", meta, err)
	}
}
