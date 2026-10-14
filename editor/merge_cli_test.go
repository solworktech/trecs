package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	libtrecs "trecs/lib"
)

func TestMergeSubcommandTakesFlagsBeforeAndAfterTheFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	copyFile(t, cueFixture, a)
	copyFile(t, cueFixture, b)
	for name, args := range map[string][]string{
		"flags first": {"-o", filepath.Join(dir, "1.jsonl"), "-gap", "250", a, b},
		"flags last":  {a, b, "-gap", "250", "-o", filepath.Join(dir, "1.jsonl")},
		"flags mixed": {a, "-o", filepath.Join(dir, "1.jsonl"), b, "-gap", "250"},
	} {
		_ = os.Remove(filepath.Join(dir, "1.jsonl"))
		if err := runMerge(args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fa, _, _, _ := libtrecs.LoadFrames(a)
		got, _, _, err := libtrecs.LoadFrames(filepath.Join(dir, "1.jsonl"))
		if err != nil || len(got) != 2*len(fa)+1 {
			t.Fatalf("%s: %d frames, %v", name, len(got), err)
		}
		if want := fa[len(fa)-1].Timestamp + 250; got[len(fa)].Timestamp != want {
			t.Errorf("%s: the second starts at %d, want %d", name, got[len(fa)].Timestamp, want)
		}
	}
}

func TestMergeSubcommandSaysWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	copyFile(t, cueFixture, a)
	for want, args := range map[string][]string{
		"where to write": {a, a},
		"two recordings": {"-o", filepath.Join(dir, "o"), a},
		"gap":            {"-o", filepath.Join(dir, "o"), "-gap", "-1", a, a},
	} {
		if err := runMerge(args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", want, err)
		}
	}
}
