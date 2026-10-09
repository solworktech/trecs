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
	defer f.Close()
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

// A recording with a long silence in it: two frames at once, then nothing for
// 1.2 seconds (a command waiting on the network), then the output.
const quietGapMs = 1250

func startQuietRecording(t *testing.T) (TerminalPlayer, *frameRecorder) {
	t.Helper()
	path := writeRecording(t, 0, 50, quietGapMs)
	p := NewTerminalPlayer()
	rec := &frameRecorder{}
	p.SetFrameCallback(rec.callback)
	if err := p.PlayWithoutRawMode(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	waitFor(t, time.Second, func() bool { return rec.count() >= 2 })
	return p, rec
}

func TestPositionAdvancesThroughAQuietStretch(t *testing.T) {
	p, rec := startQuietRecording(t)
	a := p.Position()
	time.Sleep(300 * time.Millisecond)
	b := p.Position()
	time.Sleep(300 * time.Millisecond)
	c := p.Position()
	// the last frame delivered is at 50ms; before this fix that is what a
	// progress bar would have shown for the whole gap
	if !(a < b && b < c) || b-a < 200 || c-b < 200 {
		t.Errorf("position stood still or crawled through the gap: %d, %d, %d (want it to follow the clock)", a, b, c)
	}
	if c >= quietGapMs {
		t.Errorf("position %d ran ahead of the frame being waited for (%d)", c, quietGapMs)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 3 })
	if got := p.Position(); got != quietGapMs {
		t.Errorf("after the last frame position = %d, want %d", got, quietGapMs)
	}
}

func TestPositionHoldsWhilePausedAndResumesWithoutJumping(t *testing.T) {
	p, rec := startQuietRecording(t)
	time.Sleep(300 * time.Millisecond)
	p.Pause()
	time.Sleep(50 * time.Millisecond) // let the loop notice
	held := p.Position()
	time.Sleep(400 * time.Millisecond)
	if after := p.Position(); after < held-5 || after > held+5 {
		t.Errorf("position moved while paused: %d -> %d", held, after)
	}
	if held < 250 {
		t.Errorf("paused ~300ms into the gap but position = %d", held)
	}
	p.Resume()
	time.Sleep(200 * time.Millisecond)
	if resumed := p.Position(); resumed < held+100 || resumed >= quietGapMs {
		t.Errorf("after resuming position = %d, want it to carry on from %d", resumed, held)
	}
	// the wait is not restarted from scratch: the rest of the gap is what's left
	start := time.Now()
	waitFor(t, 3*time.Second, func() bool { return rec.count() == 3 })
	if left := time.Since(start); left > 1000*time.Millisecond {
		t.Errorf("took %v more after resuming; a 1.2s gap already ~0.5s in should have ~0.7s left", left)
	}
}

func TestPositionAfterSeekingIntoTheGap(t *testing.T) {
	p, _ := startQuietRecording(t)
	p.Pause()
	p.SeekTo(600)
	waitFor(t, time.Second, func() bool { return p.Position() == 600 })
	time.Sleep(100 * time.Millisecond)
	if got := p.Position(); got != 600 {
		t.Errorf("seeked to 600 while paused: position = %d", got)
	}
}
