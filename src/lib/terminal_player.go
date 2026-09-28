package lib

import (
	"bufio"
	"encoding/json"
	"fmt"
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
	var interruptedRemaining time.Duration

	for tp.currentIndex < len(tp.frames) {
		tp.mutex.Lock()

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
			skipDelay = true
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
		skipDelay = false
		interruptedIdx = -1
		if delay < 0 {
			delay = 0
		}

		// Wait for the delay, a seek/pause request, or the stop signal
		deadline := time.Now().Add(delay)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-tp.wake:
			timer.Stop()
			interruptedIdx = idx
			interruptedRemaining = time.Until(deadline)
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
		stale := tp.seeking || tp.paused
		tp.mutex.Unlock()
		if stale {
			interruptedIdx = idx
			interruptedRemaining = 0
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
		tp.mutex.Unlock()
	}

	// Restore terminal state after playback ends
	tp.mutex.Lock()
	if tp.oldState != nil {
		_ = flushTerminalInput()
		_ = term.Restore(int(os.Stdin.Fd()), tp.oldState)
		tp.oldState = nil
	}
	tp.playbackState = PlaybackStopped
	tp.mutex.Unlock()

	close(tp.done)
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

// Wait blocks until playback completes
func (tp *TerminalPlayerImpl) Wait() {
	<-tp.done
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
