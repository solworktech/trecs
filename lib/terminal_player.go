package lib

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// TerminalPlayerImpl implements TerminalPlayer
type TerminalPlayerImpl struct {
	file         *os.File
	frames       []TerminalFrame
	currentIndex int
	speed        float64
	paused       bool
	mutex        sync.Mutex
	done         chan struct{}
	pauseResume  chan struct{}
	// wake interrupts playbackLoop's wait between frames when a seek or
	// pause is requested, so the loop re-evaluates immediately instead of
	// sleeping through the rest of the gap and then emitting a frame that
	// was chosen before the request.
	wake            chan struct{}
	seeking         bool
	targetTimestamp int64
	// targetIndex, when >= 0, is an exact frame to seek to (SeekToFrame);
	// otherwise the seek is by targetTimestamp (SeekTo).
	targetIndex    int
	oldState       *term.State
	playbackState  PlaybackState
	rawModeEnabled bool
	outputFrames   bool
	frameCallback  func(frame TerminalFrame)

	// Where playback is between frames. The loop only reports a frame when it
	// is delivered, so a long silence in the recording (a command waiting on
	// the network, say) would leave the position standing still for as long as
	// it lasts, and a progress bar that looks hung. While the loop waits for
	// the next frame these describe that wait, and Position() interpolates
	// across it. All protected by mutex.
	waiting      bool
	waitFrom     int64         // timestamp the wait started from (ms)
	waitTo       int64         // timestamp of the frame being waited for (ms)
	waitDeadline time.Time     // when that frame is due
	waitFull     time.Duration // the whole wait, as scheduled (speed applied)
	lastPos      int64         // position when not mid-wait: last frame / seek / pause point
}

// SetFrameCallback registers a function called on every frame during playback
func (tp *TerminalPlayerImpl) SetFrameCallback(cb func(frame TerminalFrame)) {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	tp.frameCallback = cb
}

// PlayWithoutRawMode plays without entering raw mode or outputting frames
func (tp *TerminalPlayerImpl) PlayWithoutRawMode(terminalFile string) error {
	tp.mutex.Lock()
	tp.rawModeEnabled = false
	tp.outputFrames = false // NEW
	tp.mutex.Unlock()
	return tp.Play(terminalFile)
}

// NewTerminalPlayer creates a new terminal player
func NewTerminalPlayer() TerminalPlayer {
	return &TerminalPlayerImpl{
		speed: 1.0,
		done:  make(chan struct{}),
		// Buffered (1): Resume() signals with a non-blocking send, and a
		// jump does Pause -> SeekTo -> Resume within microseconds. With an
		// unbuffered channel that signal is dropped if the loop hasn't
		// reached its receive yet, leaving it blocked on pauseResume with
		// paused already false - permanently stuck. A stale token is
		// harmless: the loop re-checks paused after every receive.
		pauseResume:    make(chan struct{}, 1),
		wake:           make(chan struct{}, 1),
		playbackState:  PlaybackStopped,
		rawModeEnabled: true,
		outputFrames:   true,
	}
}

// Play begins playback of a terminal recording
func (tp *TerminalPlayerImpl) Play(terminalFile string) error {
	tp.mutex.Lock()

	file, err := os.Open(terminalFile)
	if err != nil {
		tp.mutex.Unlock()
		return fmt.Errorf("failed to open terminal file: %w", err)
	}

	tp.file = file

	// Load all frames into memory for easier seeking
	if err := tp.loadFrames(); err != nil {
		tp.mutex.Unlock()
		_ = file.Close()
		return err
	}

	tp.currentIndex = 0
	tp.paused = false

	// Enter raw mode only if enabled (default is true)
	if tp.rawModeEnabled {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			tp.mutex.Unlock()
			_ = file.Close()
			return fmt.Errorf("failed to enter raw mode: %w", err)
		}
		tp.oldState = oldState
	}

	tp.playbackState = PlaybackPlaying
	tp.mutex.Unlock()

	// Start playback loop
	go tp.playbackLoop()

	return nil
}

// loadFrames reads all frames from the terminal file
// loadFrames reads this player's terminal.jsonl file, keeping only frame
// lines. Annotation lines (see recordingLine/loadFrames in frame_parser.go,
// which this deliberately mirrors) are skipped rather than unmarshalled
// directly into a bare TerminalFrame - an annotation line has no "data"
// field, so doing that would silently turn it into a bogus zero-Data frame
// carrying the annotation's own (typically much earlier) timestamp. Since
// RebuildRecording writes every annotation after all the real frames,
// that bogus frame would land last in tp.frames, and GetTotalDurationMs
// reads exactly that slot - this is what made a recording's reported
// duration collapse to an early annotation's timestamp instead of the
// recording's real length once it had any annotations saved.
func (tp *TerminalPlayerImpl) loadFrames() error {
	scanner := bufio.NewScanner(tp.file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var raw recordingLine
		if err := json.Unmarshal(line, &raw); err != nil {
			return fmt.Errorf("failed to parse frame: %w", err)
		}
		if raw.Type == "annotation" {
			continue
		}
		tp.frames = append(tp.frames, TerminalFrame{
			Timestamp: raw.Timestamp,
			Data:      raw.Data,
			Width:     raw.Width,
			Height:    raw.Height,
		})
	}
	return scanner.Err()
}

// playbackLoop manages the playback timing
func (tp *TerminalPlayerImpl) playbackLoop() {
	if len(tp.frames) == 0 {
		close(tp.done)
		return
	}

	// skipDelay makes the first frame after a seek play immediately: its
	// normal delay is the gap between it and the frame before it in the
	// original recording, which after a seek is just however long the
	// person originally paused between commands - not something to sit
	// through again.
	skipDelay := false

	// When a pause interrupts a frame's wait, how much of it was left, so
	// resuming continues from there instead of restarting the full delay.
	interruptedIdx := -1
	var interruptedRemaining, interruptedFull time.Duration

	// finished is true once playback has reached the end of the frames and
	// been reported as PlaybackStopped, until a seek (replay, or jumping
	// back to an earlier command) sends it somewhere valid again. This
	// loop deliberately never exits on its own after reaching the end -
	// only Stop() (closing tp.done) does that - specifically so a later
	// seek still has a live loop to deliver frames to: SeekToFrame/SeekTo
	// only ever set state for this loop to notice and wake it via
	// tp.wake, they don't play anything themselves. A goroutine that had
	// already returned would leave those seeks updating state nobody was
	// left to act on - the progress bar moving with nothing actually
	// playing, which is exactly the bug this avoids.
	finished := false

	for {
		tp.mutex.Lock()

		if !finished && tp.currentIndex >= len(tp.frames) && !tp.seeking {
			if tp.oldState != nil {
				_ = flushTerminalInput()
				_ = term.Restore(int(os.Stdin.Fd()), tp.oldState)
				tp.oldState = nil
			}
			tp.playbackState = PlaybackStopped
			finished = true
		}

		if finished && !tp.seeking {
			tp.mutex.Unlock()
			select {
			case <-tp.wake:
				// Might be a real seek, might be a Pause() call while
				// already finished (harmless) - either way, loop back to
				// the top and let the checks above decide.
				continue
			case <-tp.done:
				return
			}
		}

		if tp.seeking {
			// Jump to seek position
			if tp.targetIndex >= 0 {
				if tp.targetIndex < len(tp.frames) {
					tp.currentIndex = tp.targetIndex
				}
			} else {
				for i, frame := range tp.frames {
					if frame.Timestamp >= tp.targetTimestamp {
						tp.currentIndex = i
						break
					}
				}
			}
			tp.seeking = false
			tp.waiting = false
			if ci := min(tp.currentIndex, len(tp.frames)-1); tp.targetIndex >= 0 {
				tp.lastPos = tp.frames[ci].Timestamp
			} else {
				tp.lastPos = min(tp.targetTimestamp, tp.frames[len(tp.frames)-1].Timestamp)
			}
			skipDelay = true
			// A seek always leaves "finished" behind, even one that lands
			// exactly back at the end again - that will be re-detected at
			// the top of the next iteration and handled the same way as
			// reaching it normally would.
			finished = false
			// A seek is about to resume delivering frames immediately
			// (right below, in this same loop) unless the caller is
			// deliberately holding it paused (jumpToCommandForPlayback
			// pauses first, seeks, then explicitly resumes) - so the
			// reported state must reflect that now, not whatever it was
			// left at by the seek's target position (typically
			// PlaybackStopped, if this seek is a replay from the finished
			// state). Nothing else sets it back to Playing on this path:
			// Resume() only ever acts when tp.paused is already true, so a
			// bare SeekToFrame with no preceding Pause() - the common case
			// for "replay from the start" - would otherwise leave
			// GetPlaybackState() (and Wait(), which polls exactly this)
			// permanently reporting Stopped while frames were actually
			// still being delivered.
			if tp.paused {
				tp.playbackState = PlaybackPaused
			} else {
				tp.playbackState = PlaybackPlaying
			}
			tp.mutex.Unlock()
			continue
		}

		if tp.paused {
			tp.mutex.Unlock()
			// Wait for resume signal
			select {
			case <-tp.pauseResume:
			case <-tp.done:
				return
			}
			continue
		}

		idx := tp.currentIndex
		currentFrame := tp.frames[idx]
		tp.mutex.Unlock()

		// Calculate delay before this frame
		var delay time.Duration
		switch {
		case skipDelay:
			delay = 0
		case interruptedIdx == idx:
			delay = interruptedRemaining
		case idx == 0:
			delay = time.Duration(currentFrame.Timestamp) * time.Millisecond
			delay = time.Duration(float64(delay) / tp.speed)
		default:
			prevFrame := tp.frames[idx-1]
			deltaMS := currentFrame.Timestamp - prevFrame.Timestamp
			delay = time.Duration(deltaMS) * time.Millisecond
			// Apply speed multiplier
			delay = time.Duration(float64(delay) / tp.speed)
		}
		resumed := interruptedIdx == idx
		skipDelay = false
		interruptedIdx = -1
		if delay < 0 {
			delay = 0
		}

		// Wait for the delay, a seek/pause request, or the stop signal
		deadline := time.Now().Add(delay)
		full := delay
		if resumed {
			full = interruptedFull // the wait as first scheduled, not what is left of it
		}
		var from int64
		if idx > 0 {
			from = tp.frames[idx-1].Timestamp
		}
		tp.mutex.Lock()
		tp.waiting, tp.waitFrom, tp.waitTo, tp.waitDeadline, tp.waitFull = true, from, currentFrame.Timestamp, deadline, full
		tp.mutex.Unlock()

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-tp.wake:
			timer.Stop()
			interruptedIdx = idx
			interruptedRemaining = time.Until(deadline)
			interruptedFull = full
			tp.mutex.Lock()
			tp.freezePositionLocked() // hold where it had got to
			tp.mutex.Unlock()
			continue
		case <-tp.done:
			timer.Stop()
			return
		}

		// The wait can end in the same instant a seek or pause was
		// requested. This frame was chosen before that request, so it must
		// not be emitted after it - a seek would otherwise deliver one
		// stale frame from the old position first.
		tp.mutex.Lock()
		tp.waiting = false
		stale := tp.seeking || tp.paused
		if stale {
			tp.lastPos = tp.waitTo // the wait had run out
		}
		tp.mutex.Unlock()
		if stale {
			interruptedIdx = idx
			interruptedRemaining = 0
			interruptedFull = full
			continue
		}

		// Output the frame directly to stdout as bytes
		if tp.outputFrames {
			_, _ = os.Stdout.Write([]byte(currentFrame.Data))
		}

		if tp.frameCallback != nil {
			tp.frameCallback(currentFrame)
		}

		tp.mutex.Lock()
		tp.currentIndex++
		tp.lastPos = currentFrame.Timestamp
		tp.mutex.Unlock()
	}
}

// wakeLoop nudges playbackLoop out of its wait between frames. Non-blocking:
// a pending nudge already means "re-evaluate", so extras are dropped.
func (tp *TerminalPlayerImpl) wakeLoop() {
	select {
	case tp.wake <- struct{}{}:
	default:
	}
}

// flushTerminalInput discards any pending input from the terminal
func flushTerminalInput() error {
	buf := make([]byte, 1024)
	fd := int(os.Stdin.Fd())

	// Get current flags
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil
	}

	// Set non-blocking
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK)

	// Read and discard pending data
	for {
		n, _ := syscall.Read(fd, buf)
		if n <= 0 {
			break
		}
	}

	// Restore original flags
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)

	return nil
}

// Pause pauses playback
func (tp *TerminalPlayerImpl) Pause() {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	tp.freezePositionLocked() // before the loop notices, so Position() can't show a stale value meanwhile
	tp.paused = true
	tp.playbackState = PlaybackPaused
	tp.wakeLoop()
}

// Resume resumes playback from pause
func (tp *TerminalPlayerImpl) Resume() {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	if tp.paused {
		tp.paused = false
		tp.playbackState = PlaybackPlaying
		select {
		case tp.pauseResume <- struct{}{}:
		default:
		}
	}
}

// Stop terminates playback
func (tp *TerminalPlayerImpl) Stop() {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	select {
	case <-tp.done:
	default:
		close(tp.done)
	}

	if tp.file != nil {
		_ = tp.file.Close()
	}

	// Restore terminal state if in raw mode
	if tp.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), tp.oldState)
		tp.oldState = nil
	}

	tp.playbackState = PlaybackStopped
}

// SeekTo seeks to a specific timestamp in milliseconds since recording start
func (tp *TerminalPlayerImpl) SeekTo(targetMs int64) {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	tp.targetTimestamp = targetMs
	tp.targetIndex = -1
	tp.seeking = true
	tp.wakeLoop()
}

// SeekToFrame seeks to an exact frame index, unambiguous where SeekTo is not
// (frames can share a timestamp).
func (tp *TerminalPlayerImpl) SeekToFrame(index int) {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	tp.targetIndex = index
	tp.seeking = true
	tp.wakeLoop()
}

// SetSpeed sets the playback speed multiplier
func (tp *TerminalPlayerImpl) SetSpeed(speed float64) {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	if speed <= 0 {
		speed = 1.0
	}
	tp.speed = speed
}

// Wait blocks until playback reaches PlaybackStopped: naturally finishing
// or Stop() being called (a mere Pause() does not count and does not
// unblock it). It polls rather than blocking on tp.done, because tp.done is
// now reserved purely for "the playback goroutine has permanently exited"
// (Stop()) - natural completion deliberately leaves that goroutine running,
// idle, so a later seek can still resume it (see playbackLoop) - so it
// alone is no longer a reliable "has playback stopped" signal.
//
// A pending, not-yet-applied seek also holds this open: SeekToFrame/SeekTo
// only set state for playbackLoop to notice and don't themselves touch
// playbackState, so a caller doing SeekToFrame(0); Wait() right after a
// natural stop could otherwise catch the still-stale PlaybackStopped from
// before the goroutine has actually picked the seek up and started
// delivering frames again - returning immediately from what looks like a
// replay that never happened, rather than waiting for the replay it just
// asked for.
func (tp *TerminalPlayerImpl) Wait() {
	for {
		tp.mutex.Lock()
		state := tp.playbackState
		pendingSeek := tp.seeking
		tp.mutex.Unlock()
		if state == PlaybackStopped && !pendingSeek {
			return
		}
		select {
		case <-tp.done:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// GetCurrentFrameIndex returns the current frame index
func (tp *TerminalPlayerImpl) GetCurrentFrameIndex() int {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	return tp.currentIndex
}

// GetCurrentFrame returns the current frame
func (tp *TerminalPlayerImpl) GetCurrentFrame() *TerminalFrame {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	if tp.currentIndex >= 0 && tp.currentIndex < len(tp.frames) {
		return &tp.frames[tp.currentIndex]
	}
	return nil
}

// GetTotalFrames returns the total number of frames
func (tp *TerminalPlayerImpl) GetTotalFrames() int {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	return len(tp.frames)
}

// GetPlaybackState returns the current playback state
func (tp *TerminalPlayerImpl) GetPlaybackState() PlaybackState {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	return tp.playbackState
}

// GetTotalDurationMs returns the timestamp of the last loaded frame, i.e.
// the recording's total duration in milliseconds. Returns 0 if no frames
// have been loaded yet (before Play/PlayWithoutRawMode has run).
func (tp *TerminalPlayerImpl) GetTotalDurationMs() int64 {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	if len(tp.frames) == 0 {
		return 0
	}
	return tp.frames[len(tp.frames)-1].Timestamp
}

// RestoreTerminal forcefully restores terminal to normal mode
func (tp *TerminalPlayerImpl) RestoreTerminal() error {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	if tp.oldState != nil {
		if err := term.Restore(int(os.Stdin.Fd()), tp.oldState); err != nil {
			return err
		}
		tp.oldState = nil
	}

	return nil
}

// Position is where playback is now, in milliseconds into the recording. Unlike
// the timestamp of the last frame delivered, it keeps advancing through a quiet
// stretch of the recording, so a progress bar built on it never looks stuck.
// It never runs ahead of the frame being waited for, and holds still while
// paused.
func (tp *TerminalPlayerImpl) Position() int64 {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	return tp.positionLocked()
}

func (tp *TerminalPlayerImpl) positionLocked() int64 {
	if !tp.waiting {
		return tp.lastPos
	}
	return tp.interpolatedLocked()
}

// interpolatedLocked is how far through the current wait the clock says we are.
func (tp *TerminalPlayerImpl) interpolatedLocked() int64 {
	if tp.waitFull <= 0 {
		return tp.waitTo
	}
	frac := 1 - float64(time.Until(tp.waitDeadline))/float64(tp.waitFull)
	frac = math.Max(0, math.Min(1, frac))
	return tp.waitFrom + int64(frac*float64(tp.waitTo-tp.waitFrom))
}

// freezePositionLocked stops the clock where it is: the position becomes a fixed
// value (Position() reports it until playback moves again) instead of following
// the wait. Pause and the loop's own interruption both use it, so the position
// never jumps back to the last frame in between.
func (tp *TerminalPlayerImpl) freezePositionLocked() {
	if tp.waiting {
		tp.lastPos = tp.interpolatedLocked()
		tp.waiting = false
	}
}
