package lib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeRecording creates a minimal .jsonl recording with frames at the given
// millisecond timestamps (short delays, so tests run fast) and returns its
// path.
func writeRecording(t *testing.T, timestamps ...int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rec.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for i, ts := range timestamps {
		line, err := json.Marshal(recordingLine{Timestamp: ts, Data: string(rune('a' + i))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// recvFrames collects frames delivered via the callback into a channel-backed
// slice, safe to read from the test goroutine.
type frameRecorder struct {
	mu     sync.Mutex
	frames []TerminalFrame
}

func (r *frameRecorder) callback(f TerminalFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, f)
}

func (r *frameRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func (r *frameRecorder) data() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.frames))
	for i, f := range r.frames {
		out[i] = f.Data
	}
	return out
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true within timeout")
}

func TestPlayerWaitReturnsAtNaturalCompletion(t *testing.T) {
	// This is the exact property recorder's `-play` CLI depends on: play a
	// short recording through to its end, and Wait() must return promptly
	// rather than hanging - the actual regression that motivated changing
	// Wait() off of tp.done, which playbackLoop no longer closes when it
	// merely reaches the end (see playbackLoop's own comment on why).
	path := writeRecording(t, 0, 10, 20)
	p := NewTerminalPlayer()
	rec := &frameRecorder{}
	p.SetFrameCallback(rec.callback)
	if err := p.PlayWithoutRawMode(path); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() did not return after natural completion")
	}
	if p.GetPlaybackState() != PlaybackStopped {
		t.Errorf("state = %v, want PlaybackStopped", p.GetPlaybackState())
	}
	if got := rec.count(); got != 3 {
		t.Errorf("delivered %d frames, want 3", got)
	}
}

func TestPlayerWaitDoesNotReturnOnMerePause(t *testing.T) {
	path := writeRecording(t, 0, 200, 400)
	p := NewTerminalPlayer()
	if err := p.PlayWithoutRawMode(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return p.GetCurrentFrameIndex() >= 1 })
	p.Pause()

	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("Wait() returned on a mere Pause(), not just Stop()/natural completion")
	case <-time.After(200 * time.Millisecond):
	}
	p.Stop()
	<-done
}

func TestPlayerCanReplayAfterNaturalCompletion(t *testing.T) {
	// The actual reported bug: after playback finishes on its own, a seek
	// (replay from the start, or ^P/^N jumping to an earlier command) must
	// really resume frame delivery, not just move the reported position
	// with nothing left running to act on it.
	path := writeRecording(t, 0, 10, 20)
	p := NewTerminalPlayer()
	rec := &frameRecorder{}
	p.SetFrameCallback(rec.callback)
	if err := p.PlayWithoutRawMode(path); err != nil {
		t.Fatal(err)
	}
	p.Wait()
	if got := rec.count(); got != 3 {
		t.Fatalf("first pass delivered %d frames, want 3", got)
	}

	p.SeekToFrame(0)
	p.Resume()
	waitFor(t, time.Second, func() bool { return rec.count() >= 4 })
	p.Wait()

	if got := rec.data(); len(got) != 6 || got[0] != "a" || got[3] != "a" {
		t.Fatalf("replay did not deliver the frames again from the start: %v", got)
	}
	if p.GetPlaybackState() != PlaybackStopped {
		t.Errorf("state after replay finished = %v, want PlaybackStopped", p.GetPlaybackState())
	}
}

func TestPlayerCanSeekBackwardAfterNaturalCompletion(t *testing.T) {
	// Mirrors what jumpToCommandForPlayback (^P/^N) actually does: Pause,
	// SeekToFrame, Resume - and specifically from the finished state, where
	// the underlying goroutine used to have already exited.
	path := writeRecording(t, 0, 10, 20, 30)
	p := NewTerminalPlayer()
	rec := &frameRecorder{}
	p.SetFrameCallback(rec.callback)
	if err := p.PlayWithoutRawMode(path); err != nil {
		t.Fatal(err)
	}
	p.Wait()
	if got := rec.count(); got != 4 {
		t.Fatalf("first pass delivered %d frames, want 4", got)
	}

	p.Pause()
	p.SeekToFrame(1)
	p.Resume()
	waitFor(t, time.Second, func() bool { return p.GetPlaybackState() == PlaybackStopped && rec.count() > 4 })

	got := rec.data()
	// frames at index 1,2,3 ("b","c","d") replay after the seek
	want := []string{"a", "b", "c", "d", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("frame %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestPlayerStopAfterNaturalCompletionActuallyExits(t *testing.T) {
	path := writeRecording(t, 0, 10)
	p := NewTerminalPlayer()
	if err := p.PlayWithoutRawMode(path); err != nil {
		t.Fatal(err)
	}
	p.Wait()

	pImpl := p.(*TerminalPlayerImpl)
	p.Stop()
	select {
	case <-pImpl.done:
	case <-time.After(time.Second):
		t.Fatal("tp.done was not closed by Stop() after natural completion")
	}

	// A seek after Stop() must not panic or otherwise misbehave, even
	// though nothing is listening for it any more.
	p.SeekToFrame(0)
	p.Resume()
	time.Sleep(50 * time.Millisecond)
}

func TestPlayerMultipleReplaysInARow(t *testing.T) {
	path := writeRecording(t, 0, 5)
	p := NewTerminalPlayer()
	rec := &frameRecorder{}
	p.SetFrameCallback(rec.callback)
	if err := p.PlayWithoutRawMode(path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		p.Wait()
		if p.GetPlaybackState() != PlaybackStopped {
			t.Fatalf("pass %d: state = %v, want PlaybackStopped", i, p.GetPlaybackState())
		}
		p.SeekToFrame(0)
		p.Resume()
	}
	p.Wait()
	if got := rec.count(); got != 8 { // 4 passes x 2 frames
		t.Errorf("delivered %d frames across 4 passes, want 8: %v", got, rec.data())
	}
}
