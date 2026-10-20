package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	libtrecs "trecs/lib"
)

// The sound of a recording is played by the ffmpeg command line tool, which is started at the position playback is at and killed
// when playback stops. audioPlayer is the TerminalPlayer the editor uses when there is a sound: it does what the player does, and
// keeps the sound where the terminal is - on a pause, a resume and every seek - which is what the web player does with its audio
// element. Nothing else in the editor needs to know about it.

// soundOut starts and stops the ffmpeg process. One of it is shared by the players of an editor (a reload makes a new player).
type soundOut struct {
	file    string
	spawn   func(file string, ms int64) (*exec.Cmd, error) // what starts the sound: ffmpeg, but a test's own
	onError func(msg string)                               // told when ffmpeg fails (it is called from a goroutine of its own)

	mu      sync.Mutex
	cmd     *exec.Cmd
	gen     int
	failed  bool // ffmpeg said it cannot play: it is not started again for each seek
	started int  // how many times the sound was started (for tests)
}

func newSoundOut(file string) *soundOut {
	return &soundOut{file: file, spawn: ffmpegCommand}
}

// start plays the sound from ms into it, instead of what is playing now.
func (s *soundOut) start(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	if s.failed || s.file == "" {
		return
	}
	cmd, err := s.spawn(s.file, ms)
	if err != nil {
		s.fail(err.Error())
		return
	}
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, &stderr
	if err := cmd.Start(); err != nil {
		s.fail(err.Error())
		return
	}
	s.cmd = cmd
	s.started++
	s.gen++
	gen := s.gen
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		if gen != s.gen || err == nil { // killed by a stop or a seek, or the sound has ended: nothing is wrong
			return
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		if msg == "" {
			msg = err.Error()
		}
		s.fail("ffmpeg: " + msg)
	}()
}

// fail is told once: after it the sound is off for the rest of the session, the recording plays on without it.
func (s *soundOut) fail(msg string) {
	s.failed = true
	if s.onError != nil {
		go s.onError("No sound: " + msg)
	}
}

func (s *soundOut) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *soundOut) stopLocked() {
	if s.cmd == nil {
		return
	}
	s.gen++ // so that its end is not reported as a failure
	_ = s.cmd.Process.Kill()
	s.cmd = nil
}

// audioPlayer is a TerminalPlayer that plays the sound along with the terminal.
type audioPlayer struct {
	libtrecs.TerminalPlayer
	out  *soundOut
	path string // the recording: where the times of the frames are read, for a seek to a frame

	mu      sync.Mutex
	stamps  []int64 // the timestamp of each frame, loaded at the first seek to a frame
	pending int64   // where a seek made while paused went: the sound starts there when playback resumes
	moved   bool    // pending is valid
}

func newAudioPlayer(inner libtrecs.TerminalPlayer, out *soundOut, recording string) *audioPlayer {
	return &audioPlayer{TerminalPlayer: inner, out: out, path: recording}
}

func (p *audioPlayer) playing() bool {
	return p.GetPlaybackState() == libtrecs.PlaybackPlaying
}

func (p *audioPlayer) PlayWithoutRawMode(file string) error {
	if err := p.TerminalPlayer.PlayWithoutRawMode(file); err != nil {
		return err
	}
	p.mu.Lock()
	p.moved = false
	p.mu.Unlock()
	p.out.start(0)
	return nil
}

func (p *audioPlayer) Pause() {
	p.TerminalPlayer.Pause()
	p.out.stop()
}

func (p *audioPlayer) Resume() {
	wasPaused := !p.playing()
	p.TerminalPlayer.Resume()
	if !wasPaused {
		return
	}
	p.mu.Lock()
	at, moved := p.pending, p.moved
	p.moved = false
	p.mu.Unlock()
	if !moved {
		at = p.Position()
	}
	p.out.start(at)
}

func (p *audioPlayer) Stop() {
	p.out.stop()
	p.TerminalPlayer.Stop()
}

func (p *audioPlayer) SeekTo(ms int64) {
	p.TerminalPlayer.SeekTo(ms)
	p.moveTo(ms)
}

func (p *audioPlayer) SeekToFrame(index int) {
	p.TerminalPlayer.SeekToFrame(index)
	p.moveTo(p.frameTime(index))
}

// moveTo is a seek: the sound goes there at once if playback is running, and when it is resumed if not.
func (p *audioPlayer) moveTo(ms int64) {
	if ms < 0 {
		ms = 0
	}
	if p.playing() {
		p.out.start(ms)
		return
	}
	p.mu.Lock()
	p.pending, p.moved = ms, true
	p.mu.Unlock()
}

// resync starts the sound again from where playback is, if it is running (the file changed under it).
func (p *audioPlayer) resync() {
	if p.playing() {
		p.out.start(p.Position())
	}
}

func (p *audioPlayer) frameTime(index int) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stamps == nil {
		p.stamps = []int64{}
		if frames, _, _, err := libtrecs.LoadFrames(p.path); err == nil {
			for _, f := range frames {
				p.stamps = append(p.stamps, f.Timestamp)
			}
		}
	}
	if index < 0 || index >= len(p.stamps) {
		return 0
	}
	return p.stamps[index]
}

// ffmpegCommand is `ffmpeg -ss <position> -i <file> -f <output device>`. Which devices there are depends on how this ffmpeg was
// built and on the system, so it is asked, once.
func ffmpegCommand(file string, ms int64) (*exec.Cmd, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("ffmpeg is not installed (install it to hear the recording's audio)")
	}
	dev, target, err := soundDevice(bin)
	if err != nil {
		return nil, err
	}
	return exec.Command(bin, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", float64(ms)/1000), "-i", file, "-vn", "-f", dev, target), nil
}

var (
	deviceOnce   sync.Once
	deviceName   string
	deviceTarget string
	deviceErr    error
)

// outputDevices are the ffmpeg audio outputs worth trying on each system, best first, with what they are given as the file.
func outputDevices(goos string) [][2]string {
	switch goos {
	case "darwin":
		return [][2]string{{"audiotoolbox", "-"}}
	case "freebsd", "netbsd", "openbsd", "dragonfly":
		return [][2]string{{"sndio", "default"}, {"pulse", "default"}, {"oss", "/dev/dsp"}, {"alsa", "default"}}
	default:
		return [][2]string{{"pulse", "default"}, {"alsa", "default"}}
	}
}

// pickDevice is the first of the wanted outputs that ffmpeg lists among its muxers (`ffmpeg -devices`: " E  pulse ...").
func pickDevice(goos, listing string) (string, string, bool) {
	have := map[string]bool{}
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.Contains(f[0], "E") && !strings.HasPrefix(f[0], "-") {
			have[f[1]] = true
		}
	}
	for _, d := range outputDevices(goos) {
		if have[d[0]] {
			return d[0], d[1], true
		}
	}
	return "", "", false
}

func soundDevice(bin string) (string, string, error) {
	deviceOnce.Do(func() {
		if env := os.Getenv("TRECS_AUDIO_DEVICE"); env != "" { // e.g. alsa or pulse; the target is "default"
			deviceName, deviceTarget = env, "default"
			return
		}
		out, err := exec.Command(bin, "-hide_banner", "-devices").CombinedOutput()
		if err != nil {
			deviceErr = fmt.Errorf("ffmpeg -devices: %v", err)
			return
		}
		name, target, ok := pickDevice(runtime.GOOS, string(out))
		if !ok {
			deviceErr = errors.New("this ffmpeg has no audio output (pulse, alsa, audiotoolbox...)")
			return
		}
		deviceName, deviceTarget = name, target
	})
	return deviceName, deviceTarget, deviceErr
}
