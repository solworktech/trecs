package cloud

import (
	"os"
	"path/filepath"
	"testing"
)

// A session's audio is named by its codec; upload finds it whichever it is, the newest format first.
func TestResolveSessionFilesFindsTheAudioWhateverItsFormat(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	terminal := write("terminal.jsonl")

	if _, audio, err := ResolveSessionFiles(dir); err != nil || audio != "" {
		t.Fatalf("no audio: %q %v", audio, err)
	}
	mp3 := write("audio.mp3")
	if _, audio, _ := ResolveSessionFiles(dir); audio != mp3 {
		t.Errorf("an MP3 recorded before Opus was the default: %q", audio)
	}
	opus := write("audio.opus")
	got, audio, err := ResolveSessionFiles(dir)
	if err != nil || got != terminal || audio != opus {
		t.Errorf("both: %q %q %v", got, audio, err)
	}
	// given the terminal file itself, the same directory is looked in
	if _, audio, _ := ResolveSessionFiles(terminal); audio != opus {
		t.Errorf("%q", audio)
	}
	for _, ext := range []string{"m4a", "ogg"} {
		if err := os.Remove(filepath.Join(dir, "audio.opus")); err == nil || ext == "ogg" {
			_ = os.Remove(filepath.Join(dir, "audio.mp3"))
		}
		p := write("audio." + ext)
		if _, audio, _ := ResolveSessionFiles(dir); audio != p {
			t.Errorf("%s: %q", ext, audio)
		}
		_ = os.Remove(p)
	}
}
