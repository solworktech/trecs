package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
	libtrecs "trecs/lib"
)

// TerminalRecorderImpl implements TerminalRecorder
type TerminalRecorderImpl struct {
	config      *libtrecs.RecordingConfig
	outputFile  *os.File
	encoder     *json.Encoder
	cmd         *exec.Cmd
	ptmx        *os.File
	running     bool
	mutex       sync.Mutex
	startTime   time.Time
	frames      chan libtrecs.Frame
	done        chan struct{}
	captureDone chan struct{}  // closed when captureAndEchoOutput has read everything the PTY had
	oldState    *term.State    // Original terminal state (for restoration)
	sigWinch    chan os.Signal // Window resize signal handler
}

// NewTerminalRecorder creates a new terminal recorder
func NewTerminalRecorder(config *libtrecs.RecordingConfig, outputPath string) (*TerminalRecorderImpl, error) {
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create terminal output file: %w", err)
	}

	return &TerminalRecorderImpl{
		config:     config,
		outputFile: file,
		encoder:    json.NewEncoder(file),
		frames:     make(chan libtrecs.Frame, 100),
		done:       make(chan struct{}),
	}, nil
}

// Start begins terminal recording
func (tr *TerminalRecorderImpl) Start() error {
	tr.mutex.Lock()
	defer tr.mutex.Unlock()

	if tr.running {
		return fmt.Errorf("terminal recorder already running")
	}

	tr.startTime = time.Now()

	// Set stdin to raw mode so special keys (Tab, Ctrl+R, etc.) are forwarded to PTY
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set terminal to raw mode: %w", err)
	}
	tr.oldState = oldState

	// Parse shell command
	shell := tr.config.TerminalCommand
	if shell == "" {
		shell = "bash"
	}

	// Create command with shell
	tr.cmd = exec.Command(shell)

	// Set environment to prevent excessive terminal capability queries
	// that get recorded and cause garbage during playback
	tr.cmd.Env = os.Environ()
	// Override TERM to use a terminal that's simpler and less likely to
	// query capabilities in ways that break playback
	termEnv := "xterm-256color"
	found := false
	for i, env := range tr.cmd.Env {
		if strings.HasPrefix(env, "TERM=") {
			tr.cmd.Env[i] = "TERM=" + termEnv
			found = true
			break
		}
	}
	if !found {
		tr.cmd.Env = append(tr.cmd.Env, "TERM="+termEnv)
	}

	// Use pty.Start which creates PTY and properly sets up process group and TTY control
	ptmx, err := pty.Start(tr.cmd)
	if err != nil {
		_ = term.Restore(int(os.Stdin.Fd()), tr.oldState) // Ignore error
		return fmt.Errorf("failed to start shell: %w", err)
	}
	tr.ptmx = ptmx

	// Handle window size
	tr.sigWinch = make(chan os.Signal, 1)
	signal.Notify(tr.sigWinch, syscall.SIGWINCH)

	// Set initial window size, and record it as the very first line of the
	// output file - see RecordingMetaLine. Recording this is what lets
	// playback size its terminal emulation to match the real one instead
	// of estimating it from wherever the recording's own content happened
	// to draw (see lib.MeasureSize's doc comment for why that estimate is
	// only ever a fallback, not something to prefer even when it's
	// available). A width/height of 0 (GetSize failed) is written as-is;
	// a reader treats that the same as a recording made before this
	// existed - falling back to estimating.
	width, height, err := tr.setWindowSize()
	if err != nil {
		width, height = 0, 0 // PTY will use its default size; nothing to record
	}
	if err := tr.encoder.Encode(libtrecs.NewRecordingMetaLine(width, height)); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to encode recording metadata: %v\n", err)
	}

	tr.running = true

	// Start goroutine that forwards window resize signals
	go tr.handleWindowResize()

	// Start goroutine that forwards stdin to PTY (user input)
	go tr.forwardInput()

	// Start capturing and echoing output
	tr.captureDone = make(chan struct{})
	go tr.captureAndEchoOutput()

	// Wait for command to finish
	go func() {
		_ = tr.cmd.Wait() // Ignore error
		// The shell has exited, but its last output - the echo of the final
		// Enter, the exit itself - may still be sitting unread in the PTY.
		// Let the capture loop drain it to EOF before stopping: stopping
		// first closes the PTY and the output file under it, and those last
		// frames are lost (and "file already closed" is logged).
		select {
		case <-tr.captureDone:
		case <-time.After(2 * time.Second):
		}
		_ = tr.Stop() // Ignore error
	}()

	return nil
}

// setWindowSize sets the PTY window size to match the terminal, and returns
// the size that was set (0,0 if it couldn't be determined).
func (tr *TerminalRecorderImpl) setWindowSize() (width, height int, err error) {
	if tr.ptmx == nil {
		return 0, 0, nil
	}

	// Get the current window size from stdin
	width, height, err = term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return 0, 0, err
	}

	// Set the PTY window size
	if err := pty.Setsize(tr.ptmx, &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	}); err != nil {
		return 0, 0, err
	}
	return width, height, nil
}

// handleWindowResize responds to terminal window resize signals
func (tr *TerminalRecorderImpl) handleWindowResize() {
	for range tr.sigWinch {
		if tr.running {
			_, _, _ = tr.setWindowSize() // Ignore error/result - PTY will use previous size
		}
	}
}

// forwardInput reads from stdin and writes to PTY (user input forwarding)
func (tr *TerminalRecorderImpl) forwardInput() {
	_, _ = io.Copy(tr.ptmx, os.Stdin) // Ignore bytes written and error - normal when user closes terminal
}

// captureAndEchoOutput reads from PTY, records output, and echoes to stdout
func (tr *TerminalRecorderImpl) captureAndEchoOutput() {
	defer close(tr.captureDone)
	reader := bufio.NewReaderSize(tr.ptmx, 4096)
	buffer := make([]byte, 4096)

	// Runs until the PTY reports EOF/an error - which is what closing it (Stop)
	// or the shell exiting produces - so everything the shell wrote is read.
	for {
		n, err := reader.Read(buffer)
		if err != nil {
			break // EOF or error - either way, stop reading
		}

		if n > 0 {
			// Convert bytes to string
			data := string(buffer[:n])

			// Remove DCS (Device Control String) sequences that cause playback issues
			// Pattern: ESC P ... ESC \
			// These are VIM capability queries that shouldn't be recorded
			data = filterDCSSequences(data)

			// Create frame with filtered data
			frame := libtrecs.TerminalFrame{
				Timestamp: time.Since(tr.startTime).Milliseconds(),
				Data:      data,
			}

			// Write to file
			if err := tr.encoder.Encode(frame); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to encode frame: %v\n", err)
			}

			// Echo original (unfiltered) to stdout so user sees everything
			_, _ = os.Stdout.Write(buffer[:n]) // Ignore error - stdout may be broken
		}
	}
}

// Stop terminates terminal recording
func (tr *TerminalRecorderImpl) Stop() error {
	tr.mutex.Lock()
	defer tr.mutex.Unlock()

	if !tr.running {
		return nil
	}

	tr.running = false

	// Stop signal handling
	signal.Stop(tr.sigWinch)
	close(tr.sigWinch)

	// Restore terminal to original state
	if tr.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), tr.oldState) // Ignore error
	}

	// Terminate the shell process
	if tr.cmd != nil && tr.cmd.Process != nil {
		_ = tr.cmd.Process.Kill() // Ignore error - process may already be dead
	}

	// Close PTY master
	if tr.ptmx != nil {
		_ = tr.ptmx.Close() // Ignore error
	}

	// Let the capture loop finish writing whatever it was in the middle of
	// before the file goes away (it ends as soon as the PTY above is closed).
	if tr.captureDone != nil {
		select {
		case <-tr.captureDone:
		case <-time.After(time.Second):
		}
	}

	// Close output file
	if tr.outputFile != nil {
		_ = tr.outputFile.Close() // Ignore error
	}

	close(tr.done)

	return nil
}

// GetOutput returns the frame output channel
func (tr *TerminalRecorderImpl) GetOutput() chan libtrecs.Frame {
	return tr.frames
}

// Wait blocks until recording completes
func (tr *TerminalRecorderImpl) Wait() {
	<-tr.done
}

// filterDCSSequences removes Device Control String sequences that cause playback issues
// DCS sequences are: ESC P ... ESC \
// These are used by VIM for terminal capability queries and should not be recorded
func filterDCSSequences(data string) string {
	// Remove DCS sequences: ESC P ... ESC backslash
	re := regexp.MustCompile(`\x1bP[^\x1b]*\x1b\\`)
	return re.ReplaceAllString(data, "")
}
