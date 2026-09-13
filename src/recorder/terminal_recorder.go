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

	libtrecs "trecs/lib"
	"github.com/creack/pty"
	"golang.org/x/term"
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
	oldState    *term.State          // Original terminal state (for restoration)
	sigWinch    chan os.Signal       // Window resize signal handler
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
		_ = term.Restore(int(os.Stdin.Fd()), tr.oldState)  // Ignore error
		return fmt.Errorf("failed to start shell: %w", err)
	}
	tr.ptmx = ptmx

	// Handle window size
	tr.sigWinch = make(chan os.Signal, 1)
	signal.Notify(tr.sigWinch, syscall.SIGWINCH)
	
	// Set initial window size
	_ = tr.setWindowSize()  // Ignore error - PTY will use default size

	tr.running = true

	// Start goroutine that forwards window resize signals
	go tr.handleWindowResize()

	// Start goroutine that forwards stdin to PTY (user input)
	go tr.forwardInput()

	// Start capturing and echoing output
	go tr.captureAndEchoOutput()

	// Wait for command to finish
	go func() {
		_ = tr.cmd.Wait()  // Ignore error
		_ = tr.Stop()      // Ignore error
	}()

	return nil
}

// setWindowSize sets the PTY window size to match the terminal
func (tr *TerminalRecorderImpl) setWindowSize() error {
	if tr.ptmx == nil {
		return nil
	}

	// Get the current window size from stdin
	width, height, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}

	// Set the PTY window size
	return pty.Setsize(tr.ptmx, &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	})
}

// handleWindowResize responds to terminal window resize signals
func (tr *TerminalRecorderImpl) handleWindowResize() {
	for range tr.sigWinch {
		if tr.running {
			_ = tr.setWindowSize()  // Ignore error - PTY will use previous size
		}
	}
}

// forwardInput reads from stdin and writes to PTY (user input forwarding)
func (tr *TerminalRecorderImpl) forwardInput() {
	_, _ = io.Copy(tr.ptmx, os.Stdin)  // Ignore bytes written and error - normal when user closes terminal
}

// captureAndEchoOutput reads from PTY, records output, and echoes to stdout
func (tr *TerminalRecorderImpl) captureAndEchoOutput() {
	reader := bufio.NewReaderSize(tr.ptmx, 4096)
	buffer := make([]byte, 4096)

	for tr.running {
		n, err := reader.Read(buffer)	
		if err != nil {
			break  // EOF or error - either way, stop reading
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
			_, _ = os.Stdout.Write(buffer[:n])  // Ignore error - stdout may be broken
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
		_ = term.Restore(int(os.Stdin.Fd()), tr.oldState)  // Ignore error
	}

	// Terminate the shell process
	if tr.cmd != nil && tr.cmd.Process != nil {
		_ = tr.cmd.Process.Kill()  // Ignore error - process may already be dead
	}

	// Close PTY master
	if tr.ptmx != nil {
		_ = tr.ptmx.Close()  // Ignore error
	}

	// Close output file
	if tr.outputFile != nil {
		_ = tr.outputFile.Close()  // Ignore error
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
