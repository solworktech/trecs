# Architecture & Design

## System Overview

This system is built as a modular, concurrent recording and playback with clean separation of concerns.

```
┌──────────────────────────────────────────────────────────────────┐
│                        CLI Interface                             │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │  SessionRecorder (Orchestration Layer)                     │  │
│  │  - Manages lifecycle of all streams                        │  │
│  │  - Coordinates start/stop across recorders                 │  │
│  │  - Generates session metadata                              │  │
│  │  - Handles graceful shutdown                               │  │
│  └────────────────────────────────────────────────────────────┘  │
│           │                                    │                 │
│     ┌─────┴──────────────┐            ┌───────-┴─────────────┐   │
│     │ Terminal Recorder  │            │  Media Recorders     │   │
│     │ - PTY capture      │            │  - Audio Recorder    │   │
│     │ - Frame encoding   │            │  - Camera Recorder   │   │
│     │ - Timestamp sync   │            │  - Screen Recorder   │   │
│     └─────┬──────────────┘            └───────┬─────────────-┘   │
│           │                                   │                  │
│      terminal.jsonl                      *.mp3/*.mp4             │
│     (JSON Lines)                       (FFMpeg output)           │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │  Terminal Player (Playback Engine)                         │  │
│  │  - Frame loading and caching                               │  │
│  │  - Timing reproduction                                     │  │
│  │  - Speed control                                           │  │
│  │  - Seeking capability                                      │  │
│  └────────────────────────────────────────────────────────────┘  │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

### Types & Interfaces (types.go)

Defines the contract between components:

```go
// Recording configuration
type RecordingConfig struct {
    TerminalEnabled bool
    AudioEnabled bool
    CameraEnabled bool
    ScreenEnabled bool
    OutputDir string
    // ... codec/device options
}

// Interfaces for loose coupling
type TerminalRecorder interface {
    Start() error
    Stop() error
    GetOutput() chan Frame
}

type MediaRecorder interface {
    Start() error
    Stop() error
    Wait() error
}

type TerminalPlayer interface {
    Play(file string) error
    Pause()
    Resume()
    Seek(time time.Time) error
    SetSpeed(speed float64)
}
```

### Terminal Recorder (terminal_recorder.go)

**Responsibilities:**
- Open and manage PTY (pseudo-terminal)
- Execute shell command in PTY
- Capture all output with millisecond timestamps
- Encode frames as JSON and persist to file

**Key Design Decisions:**

- **PTY Usage**: Captures full terminal state including:
   - ANSI color codes
   - Cursor control sequences
   - Full-screen terminal applications
   - Interactive shell features

- **Frame Format**:
   ```json
   {
     "timestamp": 1245,
     "data": "command output here",
     "width": 120,
     "height": 40
   }
   ```

- **Buffering Strategy**:
   - Uses buffered I/O (4KB buffers)
   - JSON encoding for persistence
   - Timestamps relative to session start (millisecond precision)

- **Process Lifecycle**:
   - Creates PTY in `Start()`
   - Spawns user's shell command
   - Monitors process in background goroutine
   - Graceful cleanup on termination

**Data Flow:**

```
User Shell -> PTY -> Capture Loop (timestamp, raw bytes) -> JSON
```

### Media Recorder (media_recorder.go)

**Responsibilities:**

- Abstract FFMpeg interaction for different media types
- Build platform-specific FFMpeg arguments
- Manage FFMpeg process lifecycle
- Provide unified interface for audio/camera/screen

**Architecture Pattern: Strategy**

Each media type uses a different strategy for building FFMpeg arguments:

```go
func (mr *MediaRecorderImpl) Start() error {
    args := []string{}
    
    switch mr.mediaType {
    case "audio":
        args = mr.buildAudioArgs()
    case "camera":
        args = mr.buildCameraArgs()
    case "screen":
        args = mr.buildScreenArgs()
    }
    
    // Common handling
    cmd := exec.Command("ffmpeg", args...)
}
```

**Platform Awareness:**

```
Audio Input:
├─ Linux   -> PulseAudio (-f pulse)
├─ macOS   -> AVFoundation (-f avfoundation)
└─ Windows -> DirectShow (-f dshow)

Screen Capture:
├─ Linux   -> X11Grab (-f x11grab)
├─ macOS   -> AVFoundation (-f avfoundation)
└─ Windows -> GDI Grab (-f gdigrab)

Camera Capture:
├─ Linux   -> V4L2 (-f v4l2)
├─ macOS   -> AVFoundation (-f avfoundation)
└─ Windows -> DirectShow (-f dshow)
```

**Async Process Management:**

```go
// Non-blocking start
mr.cmd.Start()

// Background wait
go func() {
    mr.done <- mr.cmd.Wait()
}()

// Caller can continue
return nil
```

### Session Recorder (session_recorder.go)

**Responsibilities:**
- Orchestrate all recording components
- Manage session lifecycle
- Create directory structure
- Generate and persist metadata
- Coordinate graceful shutdown

**Session Structure:**

```
recordings/
└── [uuid]/
    ├── metadata.json      ← Central metadata
    ├── terminal.jsonl     ← Terminal data
    ├── audio.mp3          ← Audio stream
    ├── camera.mp4         ← Camera stream
    └── screen.mp4         ← Screen stream
```

**Lifecycle Management:**

```
- NewSessionRecorder()
- Start()
   ├─ Initialize all recorders
   ├─ Start terminal recorder (wait for shell)
   ├─ Start media recorders (async)
   └─ Return to caller
- [Recording active...]
   ├─ Terminal output -> terminal.jsonl
   ├─ Audio -> audio.mp3
   ├─ Camera -> camera.mp4
   ├─ Screen -> screen.mp4
- Stop()
   ├─ Signal all recorders
   ├─ Wait for cleanup
   ├─ Generate metadata.json
   └─ Return
```

**Metadata Structure:**

```json
{
  "sessionId": "550e8400-e29b-41d4-a716-446655440000",
  "startTime": "2024-01-15T10:30:00Z",
  "endTime": "2024-01-15T10:35:30Z",
  "terminalFile": "./recordings/.../terminal.jsonl",
  "audioFile": "./recordings/.../audio.mp3",
  "cameraFile": "./recordings/.../camera.mp4",
  "screenFile": "./recordings/.../screen.mp4",
  "sourceCommand": "bash"
}
```

### Terminal Player (terminal_player.go)

**Responsibilities:**
- Load terminal recording file
- Manage playback timing
- Provide playback controls
- Output to stdout

**Playback Algorithm:**

```
- Load() - Read and cache all frames from JSON
- Play() - Start playback loop
   ├─ Calculate delta-time from previous frame
   ├─ Apply speed multiplier
   ├─ Sleep for calculated duration
   ├─ Output frame data to stdout
   └─ Repeat until end or stop
```

**Speed Control Implementation:**

```go
// Original: frame_ts=1000ms -> delay=500ms
// Speed 2.0: delay = 500ms / 2.0 = 250ms
// Speed 0.5: delay = 500ms / 0.5 = 1000ms

delay = time.Duration(float64(delay) / speed)
```

**Seeking:**

```go
// Binary search through frames
func (tp *TerminalPlayerImpl) Seek(timestamp time.Time) {
    for i, frame := range tp.frames {
        if frame.Timestamp >= timestamp.Milliseconds() {
            tp.currentIndex = i
            break
        }
    }
}
```

**Pause/Resume:**

```go
// Pause sets flag
tp.paused = true

// Playback loop checks and waits
if tp.paused {
    <-tp.pauseResume  // Blocks until signal
}

// Resume sends signal
tp.pauseResume <- struct{}{}
```

## Data Formats

### Terminal Recording (terminal.jsonl)

**Format:** `JSON` 

**Rationale:**
- Streamable (not need to load entire file)
- Human-readable for inspection
- Each line is independent
- Compressible (gzip friendly)

**Example:**
```json
{"timestamp": 0, "data": "Welcome to bash\n"}
{"timestamp": 145, "data": "user@host:~$ "}
{"timestamp": 250, "data": "echo hello\n"}
{"timestamp": 350, "data": "hello\n"}
```

**Advantages:**
- Can inspect/grep individual lines
- Streaming playback possible
- Line-by-line encoding/decoding
- Easy to filter/transform

### Session Metadata (metadata.json)

**Purpose:** Session catalogue and recovery

```json
{
  "sessionId": "uuid",
  "startTime": "RFC3339",
  "endTime": "RFC3339",
  "terminalFile": "path/to/terminal.jsonl",
  "audioFile": "path/to/audio.mp3",
  "cameraFile": "path/to/camera.mp4",
  "screenFile": "path/to/screen.mp4",
  "sourceCommand": "executed command"
}
```

## Concurrency Model

### Recording (Multiple Goroutines)

```
Main Thread
├─ CLI parsing
├─ Config validation
├─ Recorder creation
└─ Start -> Spawns goroutines
   │
   ├─ Terminal Reader (1 goroutine)
   │  └─ Reads PTY output -> Encodes JSON
   │
   ├─ Audio Recorder (1 goroutine)
   │  └─ Waits for FFMpeg completion
   │
   ├─ Camera Recorder (1 goroutine)
   │  └─ Waits for FFMpeg completion
   │
   └─ Screen Recorder (1 goroutine)
      └─ Waits for FFMpeg completion

Main blocks on signal.Notify()
```

**Synchronization:**
- Each recorder is independent
- WaitGroup coordinates shutdown
- Mutex protects state changes
- Channels coordinate completion

### Playback (Simpler)

```
Main Thread
├─ Load all frames into memory
├─ Start playback loop
└─ Block on completion signal
   │
   └─ Playback Goroutine
      ├─ Timer-based frame emission
      ├─ Check pause/seek flags
      └─ Output to stdout
```

## Error Handling Strategy

**Three-layer approach:**

- **Configuration Validation** (main.go)
   - File paths accessible
   - Codecs available
   - Devices connected

- **Component Level** (individual recorders)
   - Process startup failures
   - I/O errors
   - Resource exhaustion

- **Session Level** (session_recorder.go)
   - Partial failure tolerance
   - Graceful degradation
   - State consistency

## Extensions & Customization

### Adding a New Media Type

To add recording for a new stream (e.g., GPU, network):

- Create new recorder:
   ```go
   type GPURecorder struct {
       // GPU-specific fields
   }
   
   func (gr *GPURecorder) Start() error { }
   func (gr *GPURecorder) Stop() error { }
   func (gr *GPURecorder) Wait() error { }
   ```

- Add to `SessionRecorder`:
   ```go
   if sr.config.GPUEnabled {
       sr.gpuRecorder = NewGPURecorder(...)
       sr.wg.Add(1)
       go func() {
           defer sr.wg.Done()
           sr.gpuRecorder.Wait()
       }()
   }
   ```

- Update CLI flags in main.go

### Modifying Playback Behaviour

Override in `TerminalPlayer`:

```go
// Custom rendering
func (tp *TerminalPlayerImpl) playbackLoop() {
    // Can add custom frame filtering
    // Can add custom timing logic
    // Can add custom output formatting
}
```

## Performance Characteristics

| Component | CPU | Memory | Disk I/O |
|-----------|-----|--------|----------|
| Terminal Recording | 1-2% | ~10MB | Continuous |
| Audio Recording | <1% | ~5MB | Continuous |
| Camera Recording | 10-20% | ~50MB | Continuous |
| Screen Recording | 15-30% | ~100MB | Continuous |
| Playback | 2-5% | ~Size of file | Sequential read |

**Optimization Tips:**
- Reduce screen resolution for lower CPU
- Lower framerate for screen/camera
- Use faster codec settings
- Run on system with SSD for I/O

## Testing Strategy

Recommended test cases:

```go
// Terminal tests
TestTerminalRecorderStart()
TestTerminalRecorderCapture()
TestTerminalRecorderNCurses()

// Media tests
TestAudioRecorderFFMpegArgs()
TestCameraRecorderPlatformDetection()

// Session tests
TestSessionRecorderLifecycle()
TestSessionRecorderMetadata()

// Playback tests
TestPlayerLoadFrames()
TestPlayerTiming()
TestPlayerSeeking()
```

## Future Enhancements

- **Synchronised Playback**: Play terminal + video together
- **Streaming**: HTTP streaming of live recordings
- **Search**: Full-text search across terminal data
- **Diff Tool**: Compare two recordings
- **Web Player**: Browser-based playback
