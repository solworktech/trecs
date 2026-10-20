package main

import (
	"os/exec"
	"testing"

	libtrecs "trecs/lib"
)

// fakePlayer is the part of a player the sound follows: its state and where it is.
type fakePlayer struct {
	libtrecs.TerminalPlayer // the rest is not used
	state                   libtrecs.PlaybackState
	pos                     int64
	played                  bool
}

func (f *fakePlayer) PlayWithoutRawMode(string) error {
	f.state, f.played = libtrecs.PlaybackPlaying, true
	return nil
}
func (f *fakePlayer) Pause()                                   { f.state = libtrecs.PlaybackPaused }
func (f *fakePlayer) Resume()                                  { f.state = libtrecs.PlaybackPlaying }
func (f *fakePlayer) Stop()                                    { f.state = libtrecs.PlaybackStopped }
func (f *fakePlayer) SeekTo(ms int64)                          { f.pos = ms }
func (f *fakePlayer) Position() int64                          { return f.pos }
func (f *fakePlayer) GetPlaybackState() libtrecs.PlaybackState { return f.state }

// soundLog is a sound that only notes where it was started, and whether it is on.
type soundLog struct {
	*soundOut
	starts []int64
}

func newSoundLog() *soundLog {
	s := &soundLog{soundOut: newSoundOut("audio.opus")}
	s.spawn = func(file string, ms int64) (*exec.Cmd, error) {
		s.starts = append(s.starts, ms)
		return exec.Command("sleep", "30"), nil // a real process, so that stopping it is the real thing
	}
	return s
}

func (s *soundLog) on() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil
}

func TestSoundFollowsPauseResumeAndSeeks(t *testing.T) {
	f := &fakePlayer{}
	s := newSoundLog()
	defer s.stop()
	p := newAudioPlayer(f, s.soundOut, "x.jsonl")

	if err := p.PlayWithoutRawMode("x.jsonl"); err != nil || !s.on() {
		t.Fatalf("playing starts the sound: err=%v on=%v", err, s.on())
	}
	f.pos = 4200
	p.Pause()
	if s.on() {
		t.Fatal("a pause stops the sound")
	}
	p.Resume()
	if !s.on() || s.starts[len(s.starts)-1] != 4200 {
		t.Fatalf("a resume plays from where playback is: %v", s.starts)
	}
	p.Resume() // already playing: not started again
	if len(s.starts) != 2 {
		t.Fatalf("resuming what is playing does nothing: %v", s.starts)
	}
	p.SeekTo(9000)
	if s.starts[len(s.starts)-1] != 9000 || !s.on() {
		t.Fatalf("a seek while playing moves the sound at once: %v", s.starts)
	}

	// as a jump to a command does: pause, seek, resume
	p.Pause()
	p.SeekTo(1500)
	if s.on() {
		t.Fatal("a seek while paused makes no sound")
	}
	f.pos = 0 // the player has not got there yet: the sound goes to where the seek went
	p.Resume()
	if s.starts[len(s.starts)-1] != 1500 || !s.on() {
		t.Fatalf("a resume after a seek plays from the seek: %v", s.starts)
	}
	p.Pause()
	f.pos = 1700
	p.Resume() // the seek was used: it is the position now
	if s.starts[len(s.starts)-1] != 1700 {
		t.Fatalf("a seek is used once: %v", s.starts)
	}
	p.Stop()
	if s.on() {
		t.Fatal("stopping stops the sound")
	}
}

func TestSoundFailureIsToldOnceAndNotRepeated(t *testing.T) {
	f := &fakePlayer{}
	s := newSoundOut("audio.opus")
	s.spawn = func(string, int64) (*exec.Cmd, error) {
		return exec.Command("sh", "-c", "echo 'no such device' >&2; exit 1"), nil
	}
	told := make(chan string, 4)
	s.onError = func(m string) { told <- m }
	p := newAudioPlayer(f, s, "x.jsonl")
	_ = p.PlayWithoutRawMode("x.jsonl")
	if m := <-told; m != "No sound: ffmpeg: no such device" {
		t.Fatalf("told: %q", m)
	}
	p.SeekTo(10)
	p.SeekTo(20)
	if s.started != 1 {
		t.Fatalf("once it failed it is not started again: %d", s.started)
	}
}

func TestPickDevice(t *testing.T) {
	listing := "Devices:\n D. = Demuxing supported\n .E = Muxing supported\n --\n D  lavfi\n  E alsa\n DE pulse\n"
	if n, target, ok := pickDevice("linux", listing); !ok || n != "pulse" || target != "default" {
		t.Fatalf("linux prefers pulse: %q %q %v", n, target, ok)
	}
	if n, _, ok := pickDevice("linux", " D  lavfi\n  E alsa\n"); !ok || n != "alsa" {
		t.Fatalf("alsa when there is no pulse: %q", n)
	}
	if _, _, ok := pickDevice("linux", " D  lavfi\n D  x11grab\n"); ok {
		t.Fatal("no output device")
	}
	if n, target, ok := pickDevice("darwin", " E audiotoolbox\n"); !ok || n != "audiotoolbox" || target != "-" {
		t.Fatalf("darwin: %q %q", n, target)
	}
}
