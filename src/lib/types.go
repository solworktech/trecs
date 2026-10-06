package lib

import "time"

// TerminalFrame represents a terminal capture with timestamp
type TerminalFrame struct {
	Timestamp int64  `json:"timestamp"` // milliseconds since start
	Data      string `json:"data"`      // captured data (may contain escape sequences)
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// RecordingMetaLine is the on-disk shape of the metadata line a recorder
// writes once, as the very first line of a .jsonl recording, capturing the
// real terminal size at record time. See RecordingMeta (frame_parser.go)
// for how it's read back - the two are deliberately separate types: this
// one is what a writer constructs, that one is what a reader gets, and nothing
// requires them to evolve together.
type RecordingMetaLine struct {
	Type   string `json:"type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// NewRecordingMetaLine builds the metadata line for a terminal of the given
// size.
func NewRecordingMetaLine(width, height int) RecordingMetaLine {
	return RecordingMetaLine{Type: "meta", Width: width, Height: height}
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

	// NoMarks turns off shell integration. Normally bash, zsh and fish are started
	// so that they mark where each prompt and each command's input and output
	// begin and end (OSC 133), which is what lets a recording be taken apart into
	// commands without guessing from how the prompt looks.
	NoMarks bool

	// ScriptFile names a file of commands, one per line, that the recorder types
	// into the shell itself instead of reading a keyboard: no terminal is needed,
	// so a recording can be made in a CI job. HumanLike types them with the
	// pauses a person would make (otherwise each command is entered at once).
	// Cols and Rows are the terminal's size when there is no terminal to ask
	// (default 100x30).
	ScriptFile string
	HumanLike  bool
	Cols, Rows int

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
	// SeekToFrame seeks to an exact frame index. Prefer it over SeekTo
	// whenever the index is known: several frames can share one timestamp
	// (a command's last output frame and the next command's prompt often
	// land in the same millisecond), and SeekTo lands on the first of them.
	SeekToFrame(index int)
	SetSpeed(speed float64)
	Wait()
	GetCurrentFrameIndex() int
	GetCurrentFrame() *TerminalFrame
	GetTotalFrames() int
	GetPlaybackState() PlaybackState
	RestoreTerminal() error
	SetFrameCallback(cb func(frame TerminalFrame))
	// GetTotalDurationMs returns the timestamp of the last frame in the
	// recording (milliseconds since recording start), i.e. its total
	// duration. Returns 0 if no frames are loaded.
	GetTotalDurationMs() int64

	// Position is where playback is now, in milliseconds. Unlike the timestamp
	// of the last frame delivered it keeps advancing through a quiet stretch
	// of the recording, so a progress bar built on it never looks stuck.
	Position() int64
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

	// ContinuationPrompt is the prompt ("> ") the shell drew in front of each
	// line after the first when the command ran over several lines; empty
	// otherwise. Needed to read the command back from InputFrames.
	ContinuationPrompt string

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
	InputText   string // Reconstructed user input; a command that ran over several lines keeps its line breaks
	StartTime   int64  // First frame timestamp

	// Output frames (terminal response)
	OutputFrames []TerminalFrame
	OutputText   string // Reconstructed output (escape codes stripped)
	EndTime      int64  // Last frame timestamp

	// Original output (with escape codes preserved)
	OutputTextRaw string

	// Annotation is an optional note attached to this command, shown
	// during playback (both TUI and web) as a dismissible overlay/modal
	// rather than being part of the terminal output itself. Follows the
	// same HasX-flag pattern as PromptFrame/HasPrompt above: HasAnnotation
	// is false and Annotation is the zero value when there is none.
	Annotation    Annotation
	HasAnnotation bool
}

// Annotation is a timestamped note attached to a command. Timestamp is
// milliseconds since recording start, the same axis TerminalFrame uses -
// normally set to the command's own start time (see
// commandFirstTimestamp), which is what lets an annotation be written to
// and read from the recording file independently of which command index
// it happens to belong to after edits or deletions shift things around.
type Annotation struct {
	ID        string `json:"id"`
	Timestamp int64  `json:"timestamp"`
	Text      string `json:"text"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	// DurationSeconds, if greater than zero, means this annotation is
	// shown automatically - pausing playback, displaying it, then clearing
	// the screen and resuming - right before its command's content
	// starts rendering. Zero (the default) means it's never shown
	// automatically; it's still viewable on demand via the manual
	// show/hide toggle (Ctrl+A in the TUI) regardless of this value.
	DurationSeconds int `json:"durationSeconds,omitempty"`
}
