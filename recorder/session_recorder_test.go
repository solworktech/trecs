package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	libtrecs "trecs/lib"
)

// events, in the order they happened
type eventLog struct {
	mu     sync.Mutex
	events []string
	at     map[string]time.Time
}

func newEventLog() *eventLog { return &eventLog{at: map[string]time.Time{}} }

func (l *eventLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
	l.at[e] = time.Now()
}

func (l *eventLog) index(e string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, x := range l.events {
		if x == e {
			return i
		}
	}
	return -1
}

func (l *eventLog) count(e string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, x := range l.events {
		if x == e {
			n++
		}
	}
	return n
}

type fakeTerminal struct {
	log      *eventLog
	startErr error
	done     chan struct{}
	once     sync.Once
}

func newFakeTerminal(log *eventLog) *fakeTerminal {
	return &fakeTerminal{log: log, done: make(chan struct{})}
}

func (f *fakeTerminal) Start() error {
	f.log.add("start terminal")
	return f.startErr
}
func (f *fakeTerminal) Stop() error                    { f.exit(); return nil }
func (f *fakeTerminal) Wait()                          { <-f.done }
func (f *fakeTerminal) GetOutput() chan libtrecs.Frame { return nil }
func (f *fakeTerminal) exit()                          { f.once.Do(func() { f.log.add("terminal ended"); close(f.done) }) }

// a fake ffmpeg: it takes readyAfter to be capturing, stopTakes to finish its file, and runs until it is stopped
type fakeMedia struct {
	name       string
	log        *eventLog
	readyAfter time.Duration
	readyErr   error
	stopTakes  time.Duration
	exited     chan struct{}
	once       sync.Once
}

func newFakeMedia(name string, log *eventLog) *fakeMedia {
	return &fakeMedia{name: name, log: log, exited: make(chan struct{})}
}

func (f *fakeMedia) Start() error { f.log.add("start " + f.name); return nil }
func (f *fakeMedia) WaitReady(time.Duration) error {
	time.Sleep(f.readyAfter)
	if f.readyErr != nil {
		return f.readyErr
	}
	f.log.add("ready " + f.name)
	return nil
}
func (f *fakeMedia) Stop() error {
	f.log.add("stop " + f.name)
	time.Sleep(f.stopTakes)
	f.once.Do(func() { close(f.exited) })
	return nil
}
func (f *fakeMedia) Wait() error { <-f.exited; return nil }

func newSession(t *testing.T, term libtrecs.TerminalRecorder, media ...*fakeMedia) *SessionRecorderImpl {
	t.Helper()
	dir := t.TempDir()
	sr := &SessionRecorderImpl{
		sessionID: "s", config: &libtrecs.RecordingConfig{OutputDir: dir}, metadata: &libtrecs.RecordingMetadata{SessionID: "s"},
		terminalRecorder: term,
	}
	for _, m := range media {
		switch m.name {
		case "audio":
			sr.audioRecorder = m
		case "screen":
			sr.screenRecorder = m
		case "camera":
			sr.cameraRecorder = m
		}
	}
	if err := mkdirAll(filepath.Join(dir, "s")); err != nil {
		t.Fatal(err)
	}
	return sr
}

func waitsWithin(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

// What the recording of the terminal and the recordings of the media begin with is the same moment: the media are
// capturing before the terminal starts, however long each takes to be.
func TestTheTerminalStartsWhenEveryMediaIsCapturing(t *testing.T) {
	log := newEventLog()
	audio, screen := newFakeMedia("audio", log), newFakeMedia("screen", log)
	audio.readyAfter, screen.readyAfter = 100*time.Millisecond, 400*time.Millisecond
	term := newFakeTerminal(log)
	sr := newSession(t, term, audio, screen)

	if err := sr.Start(); err != nil {
		t.Fatal(err)
	}
	for _, media := range []string{"audio", "screen"} {
		if log.index("ready "+media) < 0 || log.index("ready "+media) > log.index("start terminal") {
			t.Errorf("the terminal was started before the %s was capturing: %v", media, log.events)
		}
		if log.index("start "+media) > log.index("start terminal") {
			t.Errorf("the %s was started after the terminal: %v", media, log.events)
		}
	}
	term.exit()
	waitsWithin(t, 3*time.Second, "Wait", func() { _ = sr.Wait() })
}

// Ctrl+D ends the shell. That is the end of the recording, and the screen and the microphone must end with it - not
// go on until someone presses Ctrl+C. Wait, which waits for all of them, is what the recorder waits in.
func TestTheEndOfTheTerminalStopsTheMediaAndWaitReturns(t *testing.T) {
	log := newEventLog()
	audio, screen := newFakeMedia("audio", log), newFakeMedia("screen", log)
	audio.stopTakes, screen.stopTakes = 150*time.Millisecond, 300*time.Millisecond // finishing a file takes a while
	term := newFakeTerminal(log)
	sr := newSession(t, term, audio, screen)
	if err := sr.Start(); err != nil {
		t.Fatal(err)
	}

	term.exit() // Ctrl+D
	waitsWithin(t, 3*time.Second, "Wait after the end of the terminal", func() { _ = sr.Wait() })
	if log.count("stop audio") != 1 || log.count("stop screen") != 1 {
		t.Errorf("%v", log.events)
	}
	// The recording ended when the shell did, not when the files had been finished.
	ended := sr.GetMetadata().EndTime
	if ended.IsZero() || ended.Sub(log.at["terminal ended"]) > 100*time.Millisecond {
		t.Errorf("the end was noted %v after the terminal's", ended.Sub(log.at["terminal ended"]))
	}
	// and what the recorder does next, which is to stop it, finds it done
	waitsWithin(t, time.Second, "Stop", func() { _ = sr.Stop() })
	if log.count("stop audio") != 1 || log.count("stop screen") != 1 {
		t.Errorf("stopped again: %v", log.events)
	}
}

func TestStopFromOutsideEndsEverything(t *testing.T) { // SIGTERM
	log := newEventLog()
	audio, term := newFakeMedia("audio", log), newFakeTerminal(log)
	sr := newSession(t, term, audio)
	if err := sr.Start(); err != nil {
		t.Fatal(err)
	}
	waitsWithin(t, 3*time.Second, "Stop", func() { _ = sr.Stop() })
	waitsWithin(t, 3*time.Second, "Wait", func() { _ = sr.Wait() })
	if log.count("terminal ended") != 1 || log.count("stop audio") != 1 {
		t.Errorf("%v", log.events)
	}
	if err := sr.Stop(); err != nil {
		t.Error(err)
	}
}

func TestIfAMediaRecorderCannotCaptureTheTerminalIsNotStarted(t *testing.T) {
	log := newEventLog()
	audio, screen := newFakeMedia("audio", log), newFakeMedia("screen", log)
	audio.readyErr = errors.New("no such device")
	term := newFakeTerminal(log)
	sr := newSession(t, term, audio, screen)

	err := sr.Start()
	if err == nil || !errors.Is(err, audio.readyErr) && !contains(err.Error(), "no such device") {
		t.Fatalf("%v", err)
	}
	if !contains(err.Error(), "audio recorder") {
		t.Errorf("it should say which: %v", err)
	}
	if log.index("start terminal") >= 0 {
		t.Errorf("the terminal was started: %v", log.events)
	}
	if log.count("stop audio") != 1 || log.count("stop screen") != 1 {
		t.Errorf("the others must not be left running: %v", log.events)
	}
	if sr.running {
		t.Error("running")
	}
}

func TestIfTheTerminalCannotStartTheMediaAreStopped(t *testing.T) {
	log := newEventLog()
	audio, term := newFakeMedia("audio", log), newFakeTerminal(log)
	term.startErr = fmt.Errorf("standard input is not a terminal")
	sr := newSession(t, term, audio)
	if err := sr.Start(); err == nil {
		t.Fatal("started")
	}
	if log.count("stop audio") != 1 {
		t.Errorf("%v", log.events)
	}
}

func TestASessionOfTheTerminalAloneEndsWhenTheShellDoes(t *testing.T) {
	log := newEventLog()
	term := newFakeTerminal(log)
	sr := newSession(t, term)
	if err := sr.Start(); err != nil {
		t.Fatal(err)
	}
	term.exit()
	waitsWithin(t, time.Second, "Wait", func() { _ = sr.Wait() })
	if sr.GetMetadata().EndTime.IsZero() {
		t.Error("no end")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
func mkdirAll(p string) error     { return os.MkdirAll(p, 0o755) }
