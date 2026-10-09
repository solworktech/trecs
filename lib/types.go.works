package lib

import "time"

// TerminalFrame represents a terminal capture with timestamp
type TerminalFrame struct {
	Timestamp int64  `json:"timestamp"` // milliseconds since start
	Data      string `json:"data"`      // captured data (may contain escape sequences)
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// Frame represents a single captured frame with timestamp
type Frame struct {
	Timestamp time.Time
	Data      []byte
	StreamID  string
}

// RecordingConfig holds configuration for a recording session
type RecordingConfig struct {
	// Terminal recording
	TerminalEnabled bool
	TerminalCommand string // Command to run in terminal (e.g., "bash")

	// Session naming
	SessionName string // Custom session name (optional - uses timestamp if empty)

	// FFMpeg-based recordings
	AudioEnabled bool
	AudioDevice  string // e.g., "default" or "hw:0,0"
	AudioCodec   string // e.g., "aac", "libmp3lame"
	AudioBitrate string // e.g., "128k"

	CameraEnabled   bool
	CameraDevice    string // e.g., "/dev/video0"
	CameraFormat    string // e.g., "x11grab", "v4l2"
	CameraSize      string // e.g., "1920x1080"
	CameraFramerate string // e.g., "30"

	ScreenEnabled   bool
	ScreenDisplay   string // e.g., ":0" for X11 or "1" for macOS
	ScreenFormat    string // e.g., "x11grab", "gdigrab"
	ScreenSize      string // e.g., "1920x1080"
	ScreenFramerate string // e.g., "30"

	// Output directory
	OutputDir string
}

// TerminalRecorder records terminal output with timestamps
type TerminalRecorder interface {
	Start() error
	Stop() error
	Wait()
	GetOutput() chan Frame
}

// MediaRecorder handles FFMpeg-based recording
type MediaRecorder interface {
	Start() error
	Stop() error
	Wait() error
}

// SessionRecorder manages all recording streams
type SessionRecorder interface {
	Start() error
	Stop() error
	Wait() error
	GetSessionID() string
	GetMetadata() *RecordingMetadata
}

// TerminalPlayer plays back terminal recordings
type TerminalPlayer interface {
	Play(terminalFile string) error
	PlayWithoutRawMode(terminalFile string) error
	Pause()
	Resume()
	Stop()
	SeekTo(milliseconds int64)
	SetSpeed(speed float64)
	Wait()
	GetCurrentFrameIndex() int
	GetCurrentFrame() *TerminalFrame
	GetTotalFrames() int
	GetPlaybackState() PlaybackState
	RestoreTerminal() error
	SetFrameCallback(cb func(frame TerminalFrame))
}

type PlaybackState string

const (
	PlaybackPlaying PlaybackState = "playing"
	PlaybackPaused  PlaybackState = "paused"
	PlaybackStopped PlaybackState = "stopped"
)

// RecordingMetadata stores information about a recording session
type RecordingMetadata struct {
	SessionID     string
	StartTime     time.Time
	EndTime       time.Time
	TerminalFile  string
	AudioFile     string
	CameraFile    string
	ScreenFile    string
	SourceCommand string
}

// Command represents a user command and its output
type Command struct {
	// PromptFrame is the raw frame containing the shell prompt (title,
	// colour codes, and the prompt string itself) that was displayed
	// immediately before this command was typed. It carries no semantic
	// meaning for editing, but must be replayed before InputFrames so a
	// rebuilt recording still shows a prompt line.
	PromptFrame TerminalFrame
	HasPrompt   bool

	// FirstRawFrameIndex/LastRawFrameIndex are indices into the original,
	// unfiltered frame list as loaded from the recording file (the same
	// list the player counts through during playback). They span every
	// frame belonging to this command, including any that were classified
	// as pure setup noise and never stored in InputFrames/OutputFrames.
	// Use these - not len(InputFrames)+len(OutputFrames) - to work out
	// which command a live player frame index belongs to; the frame
	// lists alone omit frames the parser discarded, so a running count
	// over them drifts out of sync with the player's real position.
	FirstRawFrameIndex int
	LastRawFrameIndex  int

	// Input frames (user typing)
	InputFrames []TerminalFrame
	InputText   string // Reconstructed user input
	StartTime   int64  // First frame timestamp

	// Output frames (terminal response)
	OutputFrames []TerminalFrame
	OutputText   string // Reconstructed output (escape codes stripped)
	EndTime      int64  // Last frame timestamp

	// Original output (with escape codes preserved)
	OutputTextRaw string
}
