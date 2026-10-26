package lib

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func audioFixture(t *testing.T, seconds int) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffprobe")
	}
	path := filepath.Join(t.TempDir(), "audio.opus")
	out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+strconv.Itoa(seconds), "-c:a", "libopus", "-b:a", "32k", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot make a fixture: %v %s", err, out)
	}
	return path
}

func audioSeconds2(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTheSoundIsCutAndGivenSilenceToMatchTheTimeline(t *testing.T) {
	file := audioFixture(t, 10)
	if err := ApplyAudio(file, []Span{{AtMs: 2000, DurMs: 3000}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := audioSeconds2(t, file); got < 6.8 || got > 7.3 {
		t.Errorf("cut: %.2f s, want about 7", got)
	}
	if err := ApplyAudio(file, nil, []AudioEdit{{At: 1000, Dur: 4000}}); err != nil {
		t.Fatal(err)
	}
	if got := audioSeconds2(t, file); got < 10.8 || got > 11.3 {
		t.Errorf("silence in: %.2f s, want about 11", got)
	}
	if err := ApplyAudio(file, nil, []AudioEdit{{Remove: true, At: 1000, Dur: 4000}}); err != nil {
		t.Fatal(err)
	}
	if got := audioSeconds2(t, file); got < 6.8 || got > 7.3 {
		t.Errorf("silence out: %.2f s, want about 7", got)
	}
}

func TestNothingToDoToTheSoundIsNotAnError(t *testing.T) {
	if err := ApplyAudio(filepath.Join(t.TempDir(), "none.opus"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := ChangeAudio("whatever", AudioEdit{At: 1, Dur: 0}); err != nil {
		t.Fatal(err)
	}
}
