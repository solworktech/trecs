package lib

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSavingKeepsTheFirstBackupAndTheModeAndLeavesNothingBehind(t *testing.T) {
	original, err := os.ReadFile("testdata/fish-marked.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "terminal.jsonl")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		commands, err := ParseRecording(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := RebuildRecording(path, path+".backup", commands[:len(commands)-1]); err != nil {
			t.Fatal(err)
		}
	}
	backup, err := os.ReadFile(path + ".backup")
	if err != nil || !bytes.Equal(backup, original) {
		t.Errorf("the backup is not the recording as it was before the first save (err %v)", err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
			t.Errorf("the saved recording has mode %v, not the 0640 it had", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(path + ".backup"); fi.Mode().Perm() != 0o600 {
			t.Errorf("the backup has mode %v, not 0600", fi.Mode().Perm())
		}
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".trecs-save-*"))
	if len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
