package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	libtrecs "trecs/lib"
)

// SessionRecorderImpl manages all recording streams
type SessionRecorderImpl struct {
	sessionID        string
	config           *libtrecs.RecordingConfig
	metadata         *libtrecs.RecordingMetadata
	terminalRecorder libtrecs.TerminalRecorder
	audioRecorder    libtrecs.MediaRecorder
	cameraRecorder   libtrecs.MediaRecorder
	screenRecorder   libtrecs.MediaRecorder
	mutex            sync.Mutex
	running          bool
	wg               sync.WaitGroup
	startTime        time.Time
	stopMediaOnce    sync.Once
	endOnce          sync.Once
}

// NewSessionRecorder creates a new session recorder
func NewSessionRecorder(config *libtrecs.RecordingConfig) (*SessionRecorderImpl, error) {
	if config.OutputDir == "" {
		config.OutputDir = "./recordings"
	}

	// Create output directory
	if err := os.MkdirAll(config.OutputDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate session ID: use custom name if provided, otherwise timestamp
	var sessionID string
	if config.SessionName != "" {
		sessionID = config.SessionName
	} else {
		// Format: YYYY_MM_DD_HH_MM_SS
		sessionID = time.Now().Format("2006-01-02_15-04-05")
	}

	sessionDir := filepath.Join(config.OutputDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create session directory: %w", err)
	}

	sr := &SessionRecorderImpl{
		sessionID: sessionID,
		config:    config,
		metadata: &libtrecs.RecordingMetadata{
			SessionID:     sessionID,
			SourceCommand: config.TerminalCommand,
		},
	}

	// Initialize recorders based on config
	if err := sr.initializeRecorders(sessionDir); err != nil {
		return nil, err
	}

	return sr, nil
}

// initializeRecorders sets up all enabled recorders
func (sr *SessionRecorderImpl) initializeRecorders(sessionDir string) error {
	if sr.config.TerminalEnabled {
		terminalFile := filepath.Join(sessionDir, "terminal.jsonl")
		sr.metadata.TerminalFile = terminalFile
		recorder, err := NewTerminalRecorder(sr.config, terminalFile)
		if err != nil {
			return fmt.Errorf("failed to create terminal recorder: %w", err)
		}
		sr.terminalRecorder = recorder
	}

	if sr.config.AudioEnabled {
		audioFile := filepath.Join(sessionDir, "audio."+audioExtension(sr.config.AudioCodec))
		sr.metadata.AudioFile = audioFile
		recorder, err := NewAudioRecorder(sr.config, audioFile)
		if err != nil {
			return fmt.Errorf("failed to create audio recorder: %w", err)
		}
		sr.audioRecorder = recorder
	}

	if sr.config.CameraEnabled {
		cameraFile := filepath.Join(sessionDir, "camera.mp4")
		sr.metadata.CameraFile = cameraFile
		recorder, err := NewCameraRecorder(sr.config, cameraFile)
		if err != nil {
			return fmt.Errorf("failed to create camera recorder: %w", err)
		}
		sr.cameraRecorder = recorder
	}

	if sr.config.ScreenEnabled {
		screenFile := filepath.Join(sessionDir, "screen.mp4")
		sr.metadata.ScreenFile = screenFile
		recorder, err := NewScreenRecorder(sr.config, screenFile)
		if err != nil {
			return fmt.Errorf("failed to create screen recorder: %w", err)
		}
		sr.screenRecorder = recorder
	}

	return nil
}

// namedMedia is a media recorder with the name it goes by in what is said of it.
type namedMedia struct {
	name string
	rec  libtrecs.MediaRecorder
}

func (sr *SessionRecorderImpl) media() []namedMedia {
	var out []namedMedia
	if sr.audioRecorder != nil {
		out = append(out, namedMedia{"audio", sr.audioRecorder})
	}
	if sr.cameraRecorder != nil {
		out = append(out, namedMedia{"camera", sr.cameraRecorder})
	}
	if sr.screenRecorder != nil {
		out = append(out, namedMedia{"screen", sr.screenRecorder})
	}
	return out
}

// readier is what a media recorder that can say when it is capturing offers.
type readier interface {
	WaitReady(timeout time.Duration) error
}

// Start begins all recording streams, together.
//
// ffmpeg takes a moment to open its devices after it is started, and records nothing in it. So the media are started
// first, and the terminal when every one of them is capturing: what they record and what the terminal records
// begin at the same moment, and not some part of a second apart, by a different part every time.
func (sr *SessionRecorderImpl) Start() error {
	sr.mutex.Lock()
	defer sr.mutex.Unlock()

	if sr.running {
		return fmt.Errorf("session recorder already running")
	}

	media := sr.media()
	if len(media) > 0 {
		names := make([]string, len(media))
		for i, m := range media {
			names[i] = m.name
		}
		fmt.Fprintf(os.Stderr, "recorder: starting %s capture...\n", strings.Join(names, ", "))
		for _, m := range media {
			if err := m.rec.Start(); err != nil {
				sr.stopMedia()
				return fmt.Errorf("failed to start %s recorder: %w", m.name, err)
			}
		}
		if err := sr.waitMediaReady(media); err != nil {
			sr.stopMedia()
			return err
		}
	}

	sr.startTime = time.Now()
	sr.metadata.StartTime = sr.startTime

	if sr.terminalRecorder != nil {
		if err := sr.terminalRecorder.Start(); err != nil {
			sr.stopMedia()
			return fmt.Errorf("failed to start terminal recorder: %w", err)
		}
	}

	sr.running = true

	for _, m := range media {
		sr.wg.Add(1)
		go func(rec libtrecs.MediaRecorder) {
			defer sr.wg.Done()
			_ = rec.Wait() // Ignore error - already logged by recorder
		}(m.rec)
	}
	if sr.terminalRecorder != nil {
		sr.wg.Add(1)
		go func() {
			defer sr.wg.Done()
			sr.terminalRecorder.Wait()
			// The shell has exited - Ctrl+D, or exit - and that is the end of the recording: of all of it. The media
			// are stopped now, and not left for whoever is waiting for them to be done to stop them - which is
			// the one waiting for this.
			sr.markEnded()
			sr.stopMedia()
		}()
	}

	return nil
}

// waitMediaReady waits for every media recorder that can say so to be capturing.
func (sr *SessionRecorderImpl) waitMediaReady(media []namedMedia) error {
	errs := make(chan error, len(media))
	for _, m := range media {
		go func(m namedMedia) {
			r, ok := m.rec.(readier)
			if !ok {
				errs <- nil
				return
			}
			if err := r.WaitReady(0); err != nil {
				errs <- fmt.Errorf("%s recorder: %w", m.name, err)
				return
			}
			errs <- nil
		}(m)
	}
	var first error
	for range media {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}

// markEnded notes the moment the recording ended: that of the terminal, not of the media being finished.
func (sr *SessionRecorderImpl) markEnded() {
	sr.endOnce.Do(func() { sr.metadata.EndTime = time.Now() })
}

// stopMedia stops every media recorder, together, and returns when their files are finished. Once, whoever asks
// first and whoever asks next.
func (sr *SessionRecorderImpl) stopMedia() {
	sr.stopMediaOnce.Do(func() {
		var wg sync.WaitGroup
		for _, m := range sr.media() {
			wg.Add(1)
			go func(m namedMedia) {
				defer wg.Done()
				if err := m.rec.Stop(); err != nil {
					fmt.Fprintf(os.Stderr, "recorder: %s: %v\n", m.name, err)
				}
			}(m)
		}
		wg.Wait()
	})
}

// Stop terminates all recording streams
func (sr *SessionRecorderImpl) Stop() error {
	sr.mutex.Lock()
	defer sr.mutex.Unlock()

	if !sr.running {
		return nil
	}

	sr.running = false
	sr.markEnded()

	if sr.terminalRecorder != nil {
		_ = sr.terminalRecorder.Stop()
	}
	sr.stopMedia() // returns when the files are finished

	// Save metadata
	metadataPath := filepath.Join(sr.config.OutputDir, sr.sessionID, "metadata.json")
	if err := sr.saveMetadata(metadataPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save metadata: %v\n", err)
	}

	return nil
}

// Wait blocks until all recording streams complete
func (sr *SessionRecorderImpl) Wait() error {
	sr.wg.Wait()
	return nil
}

// GetSessionID returns the session identifier
func (sr *SessionRecorderImpl) GetSessionID() string {
	return sr.sessionID
}

// saveMetadata writes session metadata to file
func (sr *SessionRecorderImpl) saveMetadata(path string) error {
	file, err := createPrivate(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close() // Ignore error - already wrote what we needed
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(sr.metadata)
}

// Failure is why the recording is not what was asked for ("" if it is): a scripted session that stopped because the shell was not
// where the script expected it, a file that could not be written.
func (sr *SessionRecorderImpl) Failure() string {
	if f, ok := sr.terminalRecorder.(interface{ Failure() string }); ok {
		return f.Failure()
	}
	return ""
}

// ShellExitCode is the status the shell exited with by itself, if it did.
func (sr *SessionRecorderImpl) ShellExitCode() (int, bool) {
	if f, ok := sr.terminalRecorder.(interface{ ExitCode() (int, bool) }); ok {
		return f.ExitCode()
	}
	return 0, false
}

// GetMetadata returns the session metadata
func (sr *SessionRecorderImpl) GetMetadata() *libtrecs.RecordingMetadata {
	return sr.metadata
}

// createPrivate creates a file only its owner can read, or truncates one - and makes it so: a recording holds whatever was on
// the screen, tokens and passwords included.
func createPrivate(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
