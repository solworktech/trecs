package lib

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// markedRecording: three commands at 1 s, 11 s and 21 s, each lasting 1.5 s. So the pauses are 8.5 s between commands.

func TestIdleSpansAreThePartOfEachPauseOverTheLimit(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	got := IdleSpans(cmds, 2*time.Second)
	want := []Span{{4500, 6500}, {14500, 6500}} // from 2.5 s + 2 s to the next command at 11 s; the wait of 1 s before the first is short
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	if got := IdleSpans(cmds, 10*time.Second); len(got) != 0 {
		t.Errorf("no pause is over 10 s: %v", got)
	}
	// a wait before the first frame counts as a pause
	if got := IdleSpans(cmds, 500*time.Millisecond); len(got) == 0 || got[0].AtMs != 500 {
		t.Errorf("the first second is a pause over 0.5 s: %v", got)
	}
}

func TestTrimIdleMovesEverythingAfterAPauseAndKeepsTheOriginal(t *testing.T) {
	path := markedRecording(t)
	before, _ := os.ReadFile(path)
	backup := filepath.Join(t.TempDir(), "before.jsonl")
	res, err := TrimIdle(path, backup, "", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 13*time.Second || res.Before != 22500*time.Millisecond || res.After != 9500*time.Millisecond {
		t.Errorf("%+v", res)
	}
	if b, _ := os.ReadFile(backup); string(b) != string(before) {
		t.Error("the backup is not the recording as it was")
	}
	cmds := loadCommands(t, path)
	if len(cmds) != 3 || LastTimestamp(cmds) != 9500 {
		t.Fatalf("%d commands, ends at %d", len(cmds), LastTimestamp(cmds))
	}
	if cmds[1].PromptFrame.Timestamp != 4500 || cmds[2].PromptFrame.Timestamp != 8000 {
		t.Errorf("the commands begin at %d and %d", cmds[1].PromptFrame.Timestamp, cmds[2].PromptFrame.Timestamp)
	}
	// what happens inside a command keeps its pace
	if d := cmds[1].OutputFrames[0].Timestamp - cmds[1].PromptFrame.Timestamp; d != 900 {
		t.Errorf("the output came %d ms after the prompt, as before it was 900", d)
	}

	// again: nothing is over the limit now, so nothing is written
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	again, err := TrimIdle(path, backup, "", 2*time.Second)
	if err != nil || again.Pauses != 0 || again.BackupPath != "" {
		t.Fatalf("%+v %v", again, err)
	}
	if _, err := os.Stat(backup); err == nil {
		t.Error("a backup was made of a recording that was not changed")
	}
}

func TestTheTimeAnImageIsOnShowIsNotAPause(t *testing.T) {
	cmds := loadCommands(t, markedRecording(t))
	out, err := AddImage(cmds, newImage(t, 3000, 5, "shot.png")) // 5 s of the 8.5 s between the first command and the second
	if err != nil {
		t.Fatal(err)
	}
	// the pause is now 13.5 s: 0.5 s before the image, the image's 5 s, and 8 s after. The limit is 2 s of those that are not the image.
	got := IdleSpans(out, 2*time.Second)
	if len(got) != 2 || got[0] != (Span{9500, 6500}) || got[1].DurMs != 6500 {
		t.Fatalf("%v, want one cut of 6.5 s from 9.5 s, and another of the same length", got)
	}
	cut := CutSpans(out, got)
	img := RecordingImages(cut)
	if len(img) != 1 || img[0].Timestamp != 3000 || ImageDurationMs(img[0]) != 5000 {
		t.Fatalf("the image: %+v", img)
	}
	// and the gap it sits in is still there: the next command begins its 5 s after the image and 2 s of the pause are left
	if p := cut[1].PromptFrame.Timestamp; p != 2500+500+5000+1500 {
		t.Errorf("the second command begins at %d", p)
	}
}

func TestTheLongestPauseToKeepHasAMinimum(t *testing.T) {
	path := markedRecording(t)
	if _, err := TrimIdle(path, filepath.Join(t.TempDir(), "b"), "", 0); err == nil {
		t.Error("a limit of nothing was accepted")
	}
	if _, err := TrimIdle(path, filepath.Join(t.TempDir(), "b"), "", 50*time.Millisecond); err == nil {
		t.Error("a limit of 50 ms was accepted")
	}
}

func TestTheSoundIsCutWithThePausesAndTheTwoStayTogether(t *testing.T) {
	path := markedRecording(t)
	sound := audioFixture(t, 23) // the recording is 22.5 s
	res, err := TrimIdle(path, filepath.Join(t.TempDir(), "before.jsonl"), sound, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := audioSeconds2(t, sound); got < 9.5 || got > 10.5 { // 23 s less the 13 s cut
		t.Errorf("the sound is %.1f s, the recording %s", got, res.After)
	}
	if _, err := os.Stat(sound + ".backup"); err != nil || res.AudioBackup == "" {
		t.Errorf("the sound as it was is not kept: %v", err)
	}
}

func TestNothingIsChangedWhenTheSoundCannotBeCut(t *testing.T) {
	path := markedRecording(t)
	sound := audioFixture(t, 23)
	before, _ := os.ReadFile(path)
	t.Setenv("PATH", t.TempDir()) // no ffmpeg
	if _, err := TrimIdle(path, filepath.Join(t.TempDir(), "before.jsonl"), sound, 2*time.Second); err == nil {
		t.Fatal("the recording was cut with sound that could not be")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the recording was changed")
	}
}
