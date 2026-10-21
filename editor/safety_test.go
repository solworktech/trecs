package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The debug log (TRECS_DEBUG=1) is in the person's own cache directory, private to them - not a name in /tmp that anyone could have
// made a link to somewhere else.
func TestTheDebugLogIsPrivateAndInTheCacheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no modes")
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	f := openDebugLog()
	if f == nil {
		t.Fatal("no log")
	}
	defer func() { _ = f.Close() }()
	if want := filepath.Join(cache, "trecs", "debug.log"); f.Name() != want {
		t.Errorf("log at %s, want %s", f.Name(), want)
	}
	for p, mode := range map[string]os.FileMode{filepath.Join(cache, "trecs"): 0o700, f.Name(): 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want %v", p, fi, err, mode)
		}
	}
}

// The file given to $EDITOR is an argument and not text in the command: a name with quotes, spaces or a $( ) in it is only a name.
func TestTheFileGivenToTheEditorIsNotInterpretedByTheShell(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // a command in the name would run here
	marker := filepath.Join(dir, "PWNED")
	name := filepath.Join(dir, "a b'; touch PWNED; '$(touch PWNED).txt")
	if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// an "editor" that succeeds only if it was given exactly one argument, which is a file that is there
	script := filepath.Join(dir, "ed.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ $# -eq 1 ] && [ -f \"$1\" ]\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := externalEditorRun(script, name); err != nil {
		t.Fatalf("the editor was not given the file as one argument: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the name of the file was run as a command")
	}
}
