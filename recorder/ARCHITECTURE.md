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
│      terminal.jsonl                      *.opus/*.mp4            │
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
- Manage FFMpeg process lifecycle: say when it is capturing (`WaitReady`, from
  ffmpeg's `-progress` report), and finish its file properly when asked
  (`Stop`: `q` on its standard input, then an interrupt, and a kill only as a
  last resort - the index of an MP4 and the header of an MP3 are written when
  ffmpeg is told to finish, and a kill leaves the file without them). It runs in a
  process group of its own, so that a Ctrl+C meant for the shell cannot stop it
  in the middle of its file
- Take the platform from `runtime.GOOS` (not an environment variable)
- Record a voice as mono Opus in Ogg by default (`-audio-codec libopus`,
  `-audio-bitrate 32k`): about a quarter of the size of 128 kbit/s stereo MP3 at
  the same quality, with timing in every page so that a browser can seek in it
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
    ├── audio.opus         ← Audio stream (.mp3, .m4a, .ogg: it is named by its codec)
    ├── camera.mp4         ← Camera stream
    └── screen.mp4         ← Screen stream
```

**Lifecycle Management:**

```
- NewSessionRecorder()
- Start()
   ├─ Start the media recorders (ffmpeg processes)
   ├─ Wait until every one of them is capturing (WaitReady)
   ├─ Start the terminal recorder - at once, so that the media and the
   │  terminal begin at the same moment, not some part of a second apart
   └─ Return to caller
- [Recording active...]
   ├─ Terminal output -> terminal.jsonl
   ├─ Audio -> audio.opus
   ├─ Camera -> camera.mp4
   ├─ Screen -> screen.mp4
- The shell exits (Ctrl+D, `exit`) - or Stop() is called (SIGTERM)
   ├─ Note the end time: the terminal's, not that of the files being finished
   ├─ Ask every media recorder to finish, together (see below)
   ├─ Wait until their files are finished
   ├─ Generate metadata.json
   └─ Return
```

The end of the terminal ends the media: the session does not wait for ffmpeg, which
runs until it is asked to stop, to be done before it stops it.

**Metadata Structure:**

```json
{
  "sessionId": "550e8400-e29b-41d4-a716-446655440000",
  "startTime": "2024-01-15T10:30:00Z",
  "endTime": "2024-01-15T10:35:30Z",
  "terminalFile": "./recordings/.../terminal.jsonl",
  "audioFile": "./recordings/.../audio.opus",
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

**Meta line.** An optional first line `{"type":"meta","width":W,"height":H,"cell_width":CW,"cell_height":CH}` gives the terminal's size in cells and the size of a cell in pixels (the last two only when the terminal reports them).

**Sixel graphics.** The output keeps the Device Control Strings that are sixel graphics (`ESC P ... q data ESC \`), whole, in the frame that completes them (`recorder/dcsfilter.go` holds one back until it ends, across reads); every other DCS - the terminal's answers to queries - is dropped. A graphic over 2 MB (`lib.MaxSixelBytes`) is replaced by the marker `ESC ] 7733 ; sixel-dropped BEL`, which no terminal shows and which does not move the cursor. Readers accept lines up to 16 MiB (`maxRecordingLine`).

**Pictures on the emulated screen.** `lib/vt_images.go` and the web player's `termScreen.js` do the same thing: a picture's box of cells (columns = ceil(width / cell width), rows likewise) is filled with placeholder characters - private use, plane 16, `0x100000 + id*4096 + row*64 + col`, with up to 15 ids and boxes up to 63x63 - and the cursor is moved to the line below the box, in the column it began at. The emulator then scrolls, erases and overwrites the pictures as it does text; `ImagePlacements()` reads the grid back and says which parts of which pictures are where. The editor paints them over the grid with sixel (`editor/sixel.go`: cropped, scaled to its own cell size, re-encoded with at most 255 colours); the web player lays an image over the cells. `apiserver/internal/preview` leaves the space empty.

### Session Metadata (metadata.json)

**Purpose:** Session catalogue and recovery

```json
{
  "sessionId": "uuid",
  "startTime": "RFC3339",
  "endTime": "RFC3339",
  "terminalFile": "path/to/terminal.jsonl",
  "audioFile": "path/to/audio.opus",
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

## Future Enhancements

- **Synchronised Playback**: Play terminal + video together
- **Streaming**: HTTP streaming of live recordings
- **Search**: Full-text search across terminal data
- **Diff Tool**: Compare two recordings
- **Web Player**: Browser-based playback
