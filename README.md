# Terminal Recorder & Editor

A tool for recording and editing terminal sessions with precise timestamp control.

## Directory Structure

```
recorder/
├── src/lib
│   ├── types.go             # Shared types (TerminalFrame, Command, etc.)
│   ├── frame_parser.go      # Parsing & command extraction logic
│   ├── recorder/            
│   │   ├── main.go
│   │   ├── terminal_recorder.go
│   │   ├── terminal_player.go
│   │   ├── media_recorder.go
│   │   └── session_recorder.go
│   └── editor/ 
│       ├── main.go
│       └── ncurses_editor.go
```

## Building

```sh
cd src && go get trecs
```

### Build Recorder
```sh
cd src/recorder
go build -o recorder
```

### Build Editor
```sh
cd src/editor
go build -o editor
```

## Usage

### Recording
```sh
./recorder record
# Options: -audio, -screen, -name <session-name>, etc.
```

### Playback
```sh
./recorder play -file recordings/<session-id>/terminal.jsonl
```

### Editing
```sh
./editor view -file recordings/<session-id>/terminal.jsonl
```

## File Format

Terminal recordings are stored as JSON Lines (terminal.jsonl):
```json
{"timestamp":0,"data":"...","width":0,"height":0}
{"timestamp":50,"data":"...","width":0,"height":0}
```

Each line is a `TerminalFrame` with:
- `timestamp`: milliseconds since recording start
- `data`: terminal output/input data
- `width`, `height`: terminal dimensions

## Shared Code

Both applications use shared code in `src/`:
- `types.go`: Common type definitions
- `frame_parser.go`: Recording parsing and command extraction

