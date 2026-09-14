package lib 

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
	"golang.org/x/sys/unix"
)

// TerminalPlayerImpl implements TerminalPlayer
type TerminalPlayerImpl struct {
	file           *os.File
	frames         []TerminalFrame
	currentIndex   int
	speed          float64
	paused         bool
	mutex          sync.Mutex
	done           chan struct{}
	pauseResume    chan struct{}
	seeking        bool
	targetTimestamp int64
	oldState       *term.State  // Store terminal state for restoration
	playbackState  PlaybackState
}

// NewTerminalPlayer creates a new terminal player
func NewTerminalPlayer() *TerminalPlayerImpl {
	return &TerminalPlayerImpl{
		speed:       1.0,
		done:        make(chan struct{}),
		pauseResume: make(chan struct{}),
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
		_ = file.Close()  // Ignore error
		return err
	}

	tp.currentIndex = 0
	tp.paused = false
	
	// Enter raw mode so escape sequences are interpreted correctly
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		tp.mutex.Unlock()
		_ = file.Close()
		return fmt.Errorf("failed to enter raw mode: %w", err)
	}
	tp.oldState = oldState
	tp.mutex.Unlock()
	tp.playbackState = PlaybackPlaying

	// Start playback loop
	go tp.playbackLoop()

	return nil
}

// loadFrames reads all frames from the terminal file
func (tp *TerminalPlayerImpl) loadFrames() error {
	_, _ = tp.file.Seek(0, 0)  // Ignore error - file should be readable
	scanner := bufio.NewScanner(tp.file)
	tp.frames = make([]TerminalFrame, 0)

	for scanner.Scan() {
		var frame TerminalFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return fmt.Errorf("failed to parse frame: %w", err)
		}
		tp.frames = append(tp.frames, frame)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	fmt.Printf("Loaded %d frames\n", len(tp.frames))
	return nil
}

// playbackLoop manages the playback timing
func (tp *TerminalPlayerImpl) playbackLoop() {
	if len(tp.frames) == 0 {
		close(tp.done)
		return
	}

	for tp.currentIndex < len(tp.frames) {
		tp.mutex.Lock()

		if tp.seeking {
			// Jump to seek position
			for i, frame := range tp.frames {
				if frame.Timestamp >= tp.targetTimestamp {
					tp.currentIndex = i
					break
				}
			}
			tp.seeking = false
			tp.mutex.Unlock()
			continue
		}

		if tp.paused {
			tp.mutex.Unlock()
			// Wait for resume signal
			<-tp.pauseResume
			continue
		}

		currentFrame := tp.frames[tp.currentIndex]
		tp.mutex.Unlock()

		// Calculate delay to next frame
		var delay time.Duration
		if tp.currentIndex == 0 {
			delay = time.Duration(currentFrame.Timestamp) * time.Millisecond
		} else {
			prevFrame := tp.frames[tp.currentIndex-1]
			deltaMS := currentFrame.Timestamp - prevFrame.Timestamp
			delay = time.Duration(deltaMS) * time.Millisecond
		}

		// Apply speed multiplier
		delay = time.Duration(float64(delay) / tp.speed)

		// Wait for delay or stop signal
		select {
		case <-time.After(delay):
		case <-tp.done:
			return
		}

		// Output the frame directly to stdout as bytes
		// This ensures escape sequences are interpreted correctly
		_, _ = os.Stdout.Write([]byte(currentFrame.Data))

		tp.mutex.Lock()
		tp.currentIndex++
		tp.mutex.Unlock()
	}

	// Restore terminal state after playback ends
	tp.mutex.Lock()
	if tp.oldState != nil {
		// Flush any pending input from the terminal (responses to queries, etc)
		// This prevents garbage from appearing in the shell prompt
		_ = flushTerminalInput()
		
		_ = term.Restore(int(os.Stdin.Fd()), tp.oldState)
		tp.oldState = nil
	}
	tp.mutex.Unlock()

	close(tp.done)
}

// Pause pauses playback
func (tp *TerminalPlayerImpl) Pause() {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	tp.paused = true
	tp.playbackState = PlaybackPaused
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
		_ = tp.file.Close()  // Ignore error
	}
	
	// Restore terminal state if in raw mode
	if tp.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), tp.oldState)
		tp.oldState = nil
	}
}

// SeekTo seeks to a specific timestamp in milliseconds since recording start
func (tp *TerminalPlayerImpl) SeekTo(targetMs int64) {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	tp.targetTimestamp = targetMs
	tp.seeking = true
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

// flushTerminalInput discards any pending input from the terminal
// This prevents terminal responses from appearing in the shell prompt
func flushTerminalInput() error {
	buf := make([]byte, 1024)
	fd := int(os.Stdin.Fd())

	// Get current flags
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil  // If we can't flush, just continue
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

// Wait blocks until playback completes
func (tp *TerminalPlayerImpl) Wait() {
	<-tp.done
}

func (tp *TerminalPlayerImpl) GetCurrentFrameIndex() int {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	return tp.currentIndex
}

func (tp *TerminalPlayerImpl) GetCurrentFrame() *TerminalFrame {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	if tp.currentIndex >= 0 && tp.currentIndex < len(tp.frames) {
		return &tp.frames[tp.currentIndex]
	}
	return nil
}

func (tp *TerminalPlayerImpl) GetTotalFrames() int {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	return len(tp.frames)
}

func (tp *TerminalPlayerImpl) GetPlaybackState() PlaybackState {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	return tp.playbackState
}
