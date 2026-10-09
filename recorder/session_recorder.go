package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
}

// NewSessionRecorder creates a new session recorder
func NewSessionRecorder(config *libtrecs.RecordingConfig) (*SessionRecorderImpl, error) {
	if config.OutputDir == "" {
		config.OutputDir = "./recordings"
	}

	// Create output directory
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
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
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
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
		audioFile := filepath.Join(sessionDir, "audio.mp3")
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

// Start begins all recording streams
func (sr *SessionRecorderImpl) Start() error {
	sr.mutex.Lock()
	defer sr.mutex.Unlock()

	if sr.running {
		return fmt.Errorf("session recorder already running")
	}

	sr.startTime = time.Now()
	sr.metadata.StartTime = sr.startTime

	// Start terminal recorder first to ensure shell is ready
	if sr.terminalRecorder != nil {
		if err := sr.terminalRecorder.Start(); err != nil {
			return fmt.Errorf("failed to start terminal recorder: %w", err)
		}
		sr.wg.Add(1)
		go func() {
			defer sr.wg.Done()
			sr.terminalRecorder.Wait()
		}()
	}

	// Start media recorders
	if sr.audioRecorder != nil {
		if err := sr.audioRecorder.Start(); err != nil {
			return fmt.Errorf("failed to start audio recorder: %w", err)
		}
		sr.wg.Add(1)
		go func() {
			defer sr.wg.Done()
			_ = sr.audioRecorder.Wait() // Ignore error - already logged by recorder
		}()
	}

	if sr.cameraRecorder != nil {
		if err := sr.cameraRecorder.Start(); err != nil {
			return fmt.Errorf("failed to start camera recorder: %w", err)
		}
		sr.wg.Add(1)
		go func() {
			defer sr.wg.Done()
			_ = sr.cameraRecorder.Wait() // Ignore error - already logged by recorder
		}()
	}

	if sr.screenRecorder != nil {
		if err := sr.screenRecorder.Start(); err != nil {
			return fmt.Errorf("failed to start screen recorder: %w", err)
		}
		sr.wg.Add(1)
		go func() {
			defer sr.wg.Done()
			_ = sr.screenRecorder.Wait() // Ignore error - already logged by recorder
		}()
	}

	sr.running = true

	return nil
}

// Stop terminates all recording streams
func (sr *SessionRecorderImpl) Stop() error {
	sr.mutex.Lock()
	defer sr.mutex.Unlock()

	if !sr.running {
		return nil
	}

	sr.running = false
	sr.metadata.EndTime = time.Now()

	// Stop all recorders
	if sr.screenRecorder != nil {
		_ = sr.screenRecorder.Stop()
	}

	if sr.cameraRecorder != nil {
		_ = sr.cameraRecorder.Stop()
	}

	if sr.audioRecorder != nil {
		_ = sr.audioRecorder.Stop()
	}

	if sr.terminalRecorder != nil {
		_ = sr.terminalRecorder.Stop()
	}

	// Give media recorders a moment to clean up before unlocking
	time.Sleep(200 * time.Millisecond)

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
	file, err := os.Create(path)
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

// GetMetadata returns the session metadata
func (sr *SessionRecorderImpl) GetMetadata() *libtrecs.RecordingMetadata {
	return sr.metadata
}
