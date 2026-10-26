package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	libtrecs "trecs/lib"
)

// TerminalRecorderImpl implements TerminalRecorder
type TerminalRecorderImpl struct {
	config      *libtrecs.RecordingConfig
	outputFile  *os.File
	cmd         *exec.Cmd
	ptmx        *os.File
	running     bool
	mutex       sync.Mutex
	startTime   time.Time
	frames      chan libtrecs.Frame
	done        chan struct{}
	inputDone   chan struct{}  // closed when forwardInput has stopped reading the keyboard
	inputStop   atomic.Bool    // Stop sets it: the keyboard is the person's again
	captureDone chan struct{}  // closed when captureAndEchoOutput has read everything the PTY had
	oldState    *term.State    // Original terminal state (for restoration)
	sigWinch    chan os.Signal // Window resize signal handler

	cellW, cellH    int           // a character cell in pixels, 0 if the terminal did not say (see setWindowSize)
	interactive     bool          // a person at a terminal, as opposed to a scripted session
	marks           bool          // the shell was started with prompt marks (see shellint.go)
	cleanup         func()        // removes the shell integration's temporary files
	promptCh        chan struct{} // signalled when a prompt ends (mark B): the shell is ready
	lastOutput      int64         // unix nanos of the shell's last output
	commandsStarted int64         // how many command-start marks (C) the shell has written
	markTail        string        // the end of the last read, for a mark cut between two

	failMu    sync.Mutex
	failure   string // why the recording is not what was asked for (see fail); "" if it is
	exitCode  int    // the shell's own exit status, if it exited by itself
	exited    bool
	shellGone chan struct{} // closed when the shell has exited
	writeFail bool          // the recording file could not be written to: nothing more is tried
}

// fail notes why the recording is not what was asked for, and says so. The first reason is the one kept.
func (tr *TerminalRecorderImpl) fail(msg string) {
	tr.failMu.Lock()
	if tr.failure == "" {
		tr.failure = msg
	}
	tr.failMu.Unlock()
	fmt.Fprintf(os.Stderr, "\r\nrecorder: %s\r\n", msg) // the terminal is in raw mode: a line ends with both
}

// Failure is why the recording is not what was asked for: "" if it is.
func (tr *TerminalRecorderImpl) Failure() string {
	tr.failMu.Lock()
	defer tr.failMu.Unlock()
	return tr.failure
}

// ExitCode is the status the shell exited with by itself; false if it was stopped instead.
func (tr *TerminalRecorderImpl) ExitCode() (int, bool) {
	tr.failMu.Lock()
	defer tr.failMu.Unlock()
	return tr.exitCode, tr.exited
}

// write puts one line in the recording file. Once that fails (the disk is full, the file is gone) it is said once, and the
// session carries on unrecorded rather than complaining at every frame onto the screen the person is working in. What was
// written is also handed to OnRecord, if there is one (a live stream): the file is written first, and a stream that fails does not
// touch the recording.
func (tr *TerminalRecorderImpl) write(v any) {
	if tr.writeFail {
		return
	}
	var line bytes.Buffer
	if err := json.NewEncoder(&line).Encode(v); err == nil {
		_, err = tr.outputFile.Write(line.Bytes())
		if err != nil {
			tr.writeFail = true
			tr.fail(fmt.Sprintf("the recording can no longer be written (%v): everything from here on is missing from it", err))
			return
		}
	} else {
		tr.writeFail = true
		tr.fail(fmt.Sprintf("the recording can no longer be written (%v): everything from here on is missing from it", err))
		return
	}
	if tr.config.OnRecord != nil {
		tr.config.OnRecord(v)
	}
}

// withTerm gives the shell the terminal it is in: TERM is passed on as it is, so that what runs in it sees the terminal the person
// has (terminfo-driven programs, colours, a multiplexer). Only when there is none to pass on - a CI job has no TERM, or "dumb" -
// is one given, as a shell for a recording is expected to be in a terminal.
func withTerm(env []string) []string {
	for i, e := range env {
		if v, ok := strings.CutPrefix(e, "TERM="); ok {
			if v == "" || v == "dumb" {
				env[i] = "TERM=xterm-256color"
			}
			return env
		}
	}
	return append(env, "TERM=xterm-256color")
}

// splitUTF8 cuts off the end of b if it is the start of a character whose rest has not been read yet. The pty is read in
// pieces of any size, and a character cut between two is two broken ones in a file of JSON text.
func splitUTF8(b []byte) (whole, rest []byte) {
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i], b[i:]
			}
			break
		}
	}
	return b, nil
}

// NewTerminalRecorder creates a new terminal recorder
func NewTerminalRecorder(config *libtrecs.RecordingConfig, outputPath string) (*TerminalRecorderImpl, error) {
	file, err := createPrivate(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create terminal output file: %w", err)
	}

	return &TerminalRecorderImpl{
		config:     config,
		outputFile: file,
		frames:     make(chan libtrecs.Frame, 100),
		shellGone:  make(chan struct{}),
		done:       make(chan struct{}),
		promptCh:   make(chan struct{}, 1),
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

	// A scripted session (-commands-file) types the commands itself and needs no
	// terminal: it can run where there is none, in a CI job.
	var script []string
	if tr.config.ScriptFile != "" {
		var err error
		if script, err = readScript(tr.config.ScriptFile); err != nil {
			return fmt.Errorf("commands file: %w", err)
		}
	} else if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("standard input is not a terminal: use -commands-file to record without one")
	}
	tr.interactive = tr.config.ScriptFile == ""

	// Set stdin to raw mode so special keys (Tab, Ctrl+R, etc.) are forwarded to PTY
	var outer *termSettings // the person's terminal as it is, before it is made raw: the shell's gets the same
	if tr.interactive {
		outer = readTermios(os.Stdin)
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("failed to set terminal to raw mode: %w", err)
		}
		tr.oldState = oldState
	}

	// Parse shell command
	shell := tr.config.TerminalCommand
	if shell == "" {
		shell = detectShell()
	}
	fmt.Fprintf(os.Stderr, "recorder: recording %s\n", describeShell(shell))

	// Start the shell so that it marks where prompts and commands begin and end.
	var shellArgs, shellEnv []string
	if !tr.config.NoMarks {
		switch integ, err := setupShellIntegrationFor(shell, tr.config.ScriptFile != ""); {
		case err != nil:
			fmt.Fprintf(os.Stderr, "recorder: shell integration unavailable (%v); recording without prompt marks\n", err)
		case integ != nil:
			shellArgs, shellEnv, tr.cleanup, tr.marks = integ.args, integ.env, integ.cleanup, true
		}
	}

	if tr.config.ScriptFile != "" && !tr.marks {
		if !tr.config.AllowUnmarked {
			if tr.cleanup != nil {
				tr.cleanup()
			}
			return errors.New(unmarkedRefusal(shell, tr.config.NoMarks, markShellsAvailable()))
		}
		fmt.Fprintln(os.Stderr, "recorder: this shell has no prompt marks (-allow-unmarked): lines are typed when it is quiet, which is wrong if the script asks for input.")
	}

	// Create command with shell
	tr.cmd = exec.Command(shell, shellArgs...)

	tr.cmd.Env = withTerm(os.Environ())

	// Use pty.Start which creates PTY and properly sets up process group and TTY control
	tr.cmd.Env = append(tr.cmd.Env, shellEnv...)
	ptmx, err := startShell(tr.cmd, outer, func(p *os.File) {
		tr.ptmx = p
		_, _, _ = tr.setWindowSize() // before the shell runs; the size is read again below, for the recording's first line
	})
	if err != nil {
		tr.ptmx = nil
		if tr.oldState != nil {
			_ = term.Restore(int(os.Stdin.Fd()), tr.oldState) // Ignore error
		}
		if tr.cleanup != nil {
			tr.cleanup()
		}
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
	metaLine := libtrecs.NewRecordingMetaLine(width, height)
	metaLine.Env, metaLine.Shell = libtrecs.DetectEnv(), libtrecs.ShellName(shell)
	metaLine.CellWidth, metaLine.CellHeight = tr.cellW, tr.cellH
	tr.write(metaLine)

	tr.running = true

	// Start goroutine that forwards window resize signals
	go tr.handleWindowResize()

	// Start goroutine that forwards stdin to PTY (user input)
	if tr.interactive {
		tr.inputDone = make(chan struct{})
		go tr.forwardInput()
	} else {
		go tr.feedScript(script)
	}

	// Start capturing and echoing output
	tr.captureDone = make(chan struct{})
	go tr.captureAndEchoOutput()

	// Wait for command to finish
	go func() {
		_ = tr.cmd.Wait() // the status is kept below
		close(tr.shellGone)
		if ps := tr.cmd.ProcessState; ps != nil && ps.Exited() {
			tr.failMu.Lock()
			tr.exitCode, tr.exited = ps.ExitCode(), true
			tr.failMu.Unlock()
		}
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

	// Get the current window size from stdin - or, for a scripted session, which
	// has no terminal to ask, from the configuration (the same every time, so a
	// recording made in CI looks like the one made on a laptop).
	var xpix, ypix uint16
	if tr.interactive {
		width, height, err = term.GetSize(int(os.Stdin.Fd()))
		if err != nil {
			return 0, 0, err
		}
		// The window's size in pixels, if the terminal says (many do not): programs that draw pictures (img2sixel,
		// chafa, lsix...) size them by it, and a recording of a picture needs the size of a cell to be played back.
		if ws, werr := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ); werr == nil {
			xpix, ypix = ws.Xpixel, ws.Ypixel
		}
	} else {
		width, height = scriptedSize(tr.config.Cols, tr.config.Rows)
	}
	tr.cellW, tr.cellH = 0, 0
	if xpix > 0 && ypix > 0 && width > 0 && height > 0 {
		tr.cellW, tr.cellH = int(xpix)/width, int(ypix)/height
	}

	// Set the PTY window size
	if err := pty.Setsize(tr.ptmx, &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
		X:    xpix,
		Y:    ypix,
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

// forwardInput reads from stdin and writes to PTY (user input forwarding). It stops when Stop says so, and it must: a reader left
// blocked in a read of the keyboard takes the first key the person presses after the recording - the answer to "Upload to the Trecs
// Platform? [Y/n]" - and sends it to a shell that is gone, so that "n" is lost and the Enter after it answers yes. It waits for the
// keyboard with poll, a tenth of a second at a time, and reads only what is there.
func (tr *TerminalRecorderImpl) forwardInput() {
	defer close(tr.inputDone)
	fd := int(os.Stdin.Fd())
	buf := make([]byte, 4096)
	for !tr.inputStop.Load() {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return
		}
		if n == 0 || tr.inputStop.Load() { // nothing yet; or the recording is over, and what is there is not for the shell
			continue
		}
		m, err := os.Stdin.Read(buf)
		if m > 0 {
			if _, werr := tr.ptmx.Write(buf[:m]); werr != nil {
				return // the shell is gone
			}
		}
		if err != nil {
			return // closed, or end of input
		}
	}
}

// stopInput makes forwardInput let go of the keyboard, and waits for it to.
func (tr *TerminalRecorderImpl) stopInput() {
	tr.inputStop.Store(true)
	if tr.inputDone != nil {
		select {
		case <-tr.inputDone:
		case <-time.After(time.Second):
		}
	}
}

// captureAndEchoOutput reads from PTY, records output, and echoes to stdout
func (tr *TerminalRecorderImpl) captureAndEchoOutput() {
	defer close(tr.captureDone)
	reader := bufio.NewReaderSize(tr.ptmx, 4096)
	buffer := make([]byte, 4096)
	filter := newDCSFilter()
	var carry []byte // the start of a character whose end is in the next read

	// Runs until the PTY reports EOF/an error - which is what closing it (Stop)
	// or the shell exiting produces - so everything the shell wrote is read.
	for {
		n, err := reader.Read(buffer)
		if err != nil {
			break // EOF or error - either way, stop reading
		}

		if n > 0 {
			tr.noteOutput(string(buffer[:n]))

			// A character cut by the end of this read waits for the rest of it
			whole, rest := splitUTF8(append(carry, buffer[:n]...))
			carry = append([]byte(nil), rest...)
			data := string(whole)

			// Remove DCS (Device Control String) sequences that cause playback issues
			// (ESC P ... ESC \: VIM capability queries and the like that shouldn't be
			// recorded) - but not sixel graphics, which are output (see dcsFilter). A
			// graphic cut by the end of this read is held until the rest has come, so
			// this may be nothing at all.
			data = filter.Filter(data)

			// Create frame with filtered data (a chunk that is only the start of a graphic
			// being held back makes no frame; one that was all a dropped string does, empty
			// as it always has)
			if len(whole) > 0 && (data != "" || !filter.holding()) {
				tr.write(libtrecs.TerminalFrame{
					Timestamp: time.Since(tr.startTime).Milliseconds(),
					Data:      data,
				})
			}

			// Echo original (unfiltered) to stdout so user sees everything
			echo := buffer[:n]
			if !tr.interactive { // see terminalQueryRe
				echo = terminalQueryRe.ReplaceAll(echo, nil)
			}
			_, _ = os.Stdout.Write(echo) // Ignore error - stdout may be broken
		}
	}
	// the output ended on an escape that began nothing, or on a character that never ended
	if rest := filter.Flush() + string(carry); rest != "" {
		tr.write(libtrecs.TerminalFrame{Timestamp: time.Since(tr.startTime).Milliseconds(), Data: rest})
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

	tr.stopInput()

	// Stop signal handling
	signal.Stop(tr.sigWinch)
	close(tr.sigWinch)

	// Restore terminal to original state
	if tr.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), tr.oldState) // Ignore error
	}

	if tr.cleanup != nil {
		tr.cleanup()
	}

	// End the shell as closing the terminal would: hung up on first, so that it saves its history and hangs up on its jobs, and
	// only killed if it does not go.
	if tr.cmd != nil && tr.cmd.Process != nil {
		select {
		case <-tr.shellGone:
		default:
			_ = tr.cmd.Process.Signal(syscall.SIGHUP)
			select {
			case <-tr.shellGone:
			case <-time.After(2 * time.Second):
				_ = tr.cmd.Process.Kill() // Ignore error - process may already be dead
			}
		}
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

// shellInForeground says whether the shell is what the terminal is waiting on - no command of its own is running in the foreground.
// When that cannot be told, it is taken to be so.
func (tr *TerminalRecorderImpl) shellInForeground() bool {
	if tr.ptmx == nil || tr.cmd == nil || tr.cmd.Process == nil {
		return true
	}
	conn, err := tr.ptmx.SyscallConn() // not Fd(): that would make the file blocking
	if err != nil {
		return true
	}
	pgrp, ok := 0, false
	_ = conn.Control(func(fd uintptr) {
		if g, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil {
			pgrp, ok = g, true
		}
	})
	return !ok || pgrp == tr.cmd.Process.Pid
}
