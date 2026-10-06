package lib

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// recordingLine is the on-disk shape of one line of a .jsonl recording,
// covering both possible line kinds - "type" distinguishes them. A line
// with no "type" at all is treated as a frame, for backward compatibility
// with every recording made before annotations existed. Unmarshalling
// every line directly into a bare TerminalFrame (as this file used to)
// would silently turn an annotation line - which has no "data" field -
// into a bogus zero-Data frame and corrupt command grouping; LoadFrames
// below checks "type" first specifically to avoid that.
type recordingLine struct {
	Type            string `json:"type,omitempty"`
	Timestamp       int64  `json:"timestamp"`
	Data            string `json:"data,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	ID              string `json:"id,omitempty"`
	Text            string `json:"text,omitempty"`
	Author          string `json:"author,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
}

// RecordingMeta carries recording-level metadata captured once at record
// time (see recorder/terminal_recorder.go) - currently just the real
// terminal size. Width and Height are 0 for a recording made before this
// existed, or if the size genuinely couldn't be determined at record time;
// callers should treat that as "unknown", not as a literal 0x0 terminal.
type RecordingMeta struct {
	Width  int
	Height int
}

// LoadFrames reads a .jsonl recording and splits it into frames,
// annotations, and recording-level metadata (see RecordingMeta) by line.
// Exported alongside ParseRecording for callers that
// need the raw, unfiltered frame stream itself - e.g. MeasureSize, which
// must see every frame ParseRecording's command-grouping would otherwise
// filter out as "setup noise".
func LoadFrames(filePath string) ([]TerminalFrame, []Annotation, RecordingMeta, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, RecordingMeta{}, fmt.Errorf("failed to open recording file: %w", err)
	}
	defer func() { _ = file.Close() }()

	var frames []TerminalFrame
	var annotations []Annotation
	var meta RecordingMeta
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var raw recordingLine
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, nil, RecordingMeta{}, fmt.Errorf("failed to parse line: %w", err)
		}
		switch raw.Type {
		case "annotation":
			annotations = append(annotations, Annotation{
				ID:              raw.ID,
				Timestamp:       raw.Timestamp,
				Text:            raw.Text,
				Author:          raw.Author,
				CreatedAt:       raw.CreatedAt,
				DurationSeconds: raw.DurationSeconds,
			})
			continue
		case "meta":
			// Recorded once, at the very start of the file (see
			// terminal_recorder.go) - if a future format ever writes more
			// than one (e.g. to capture a resize mid-recording), the last
			// one wins, but nothing does that yet.
			meta.Width = raw.Width
			meta.Height = raw.Height
			continue
		}
		frames = append(frames, TerminalFrame{
			Timestamp: raw.Timestamp,
			Data:      raw.Data,
			Width:     raw.Width,
			Height:    raw.Height,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, RecordingMeta{}, err
	}

	return frames, annotations, meta, nil
}

// attachAnnotations matches each annotation to the command it belongs to:
// the latest command whose start is at or before the annotation's
// timestamp. This is deliberately not an inclusive [start, end] range
// check - a command's last output frame and the next command's prompt
// frame commonly land on the exact same millisecond (the prompt reappears
// immediately after output ends), which would make that timestamp match
// both commands ambiguously and let one annotation silently overwrite
// another depending on processing order. Commands are chronological, so
// once a command's start is past the annotation's timestamp, every
// command after it is too.
func attachAnnotations(commands []Command, annotations []Annotation) {
	for _, ann := range annotations {
		best := -1
		for i := range commands {
			if commandFirstTimestamp(commands[i]) <= ann.Timestamp {
				best = i
			} else {
				break
			}
		}
		if best >= 0 {
			commands[best].Annotation = ann
			commands[best].HasAnnotation = true
		}
	}
}

// ParseRecording reads a terminal.jsonl file, extracts commands, and
// attaches any annotations to the command each belongs to.
func ParseRecording(filePath string) ([]Command, error) {
	frames, annotations, _, err := LoadFrames(filePath)
	if err != nil {
		return nil, err
	}

	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames found in recording")
	}

	// Extract prompt from first frame
	prompt := extractPrompt(frames[0].Data)

	// Group frames into commands
	commands, err := groupIntoCommands(frames, prompt)
	if err != nil {
		return nil, err
	}

	attachAnnotations(commands, annotations)

	return commands, nil
}

// extractPrompt gets the shell prompt from the first frame
func extractPrompt(firstFrameData string) string {
	lines := strings.Split(firstFrameData, "\n")
	if len(lines) > 0 {
		lastLine := lines[len(lines)-1]
		for _, char := range "$#>" {
			if idx := strings.LastIndex(lastLine, string(char)); idx != -1 {
				return lastLine[:idx+1]
			}
		}
	}
	return ""
}

// groupIntoCommands organises frames into command input + output pairs
func groupIntoCommands(frames []TerminalFrame, prompt string) ([]Command, error) {
	// A recording made with shell integration says itself where everything
	// starts and ends (see prompt_marks.go); the heuristics below are for the
	// ones that were not.
	if hasPromptMarks(frames) {
		return dropSessionEnd(groupByMarks(frames)), nil
	}

	var commands []Command
	var currentCommand *Command
	var inputPhase = true
	// After Enter it isn't yet clear whether the command ran or the shell is
	// asking for another line: frames that only switch modes are held until
	// something visible arrives, and the verdict is read from all of them.
	pending := false
	var held []TerminalFrame

	for i := 1; i < len(frames); i++ {
		frame := frames[i]
		data := frame.Data

		// Skip title/setup frames that aren't actual commands
		if isSetupFrame(data) {
			continue
		}

		if pending && !inputPhase && currentCommand != nil {
			var sb strings.Builder
			for _, h := range held {
				sb.WriteString(h.Data)
			}
			sb.WriteString(data)
			after := sb.String()
			if visibleText(after) == "" {
				held = append(held, frame)
				continue
			}
			pending = false
			if p, ok := continuationPrompt(after); ok {
				currentCommand.InputFrames = append(append(currentCommand.InputFrames, held...), frame)
				currentCommand.ContinuationPrompt = p
				held, inputPhase = nil, true
				continue
			}
			currentCommand.OutputFrames = append(currentCommand.OutputFrames, held...)
			held = nil // this frame is handled below like any other
		}

		// The shell echoes Enter as CRLF. For a command that finishes at once,
		// that echo and the command's first output are read together as one
		// frame - or, for a command with no output at all (`true`), together
		// with the next prompt. Judged by length alone such a frame is not
		// "input", so the input phase never ended and the following commands
		// were merged into this one. The CRLF ends the input; whatever follows
		// it is handled below as ordinary output (or the next prompt).
		// Until Enter, everything the terminal shows is the line being edited:
		// typed characters, but also readline redrawing it (Ctrl-R, history
		// recall - which may be several lines, drawn with cursor moves and
		// reverse video). None of it is told apart by how it looks; what ends the
		// input is Enter (see enterIndex) - or, for a shell without bracketed
		// paste, a short frame with a newline in it.
		enterAt := -1
		if inputPhase {
			enterAt = enterIndex(data)
		}
		enterEcho := enterAt >= 0

		if inputPhase {
			if currentCommand == nil {
				// This is the very first command in the recording; its
				// prompt is the file's initial frame (frames[0]), which
				// the loop above never visits directly.
				currentCommand = &Command{
					InputFrames:        []TerminalFrame{},
					OutputFrames:       []TerminalFrame{},
					StartTime:          frame.Timestamp,
					PromptFrame:        frames[0],
					HasPrompt:          true,
					FirstRawFrameIndex: 0,
				}
			}
			if enterEcho {
				// Split the frame in two without losing or repeating a byte:
				// RebuildRecording writes prompt + input + output frames back
				// out, so the pieces must add up to exactly the original.
				if enterAt > 0 {
					enter := frame
					enter.Data = data[:enterAt]
					currentCommand.InputFrames = append(currentCommand.InputFrames, enter)
				} else if len(currentCommand.InputFrames) == 0 && visibleText(data) == "exit" {
					// Ctrl-D at an empty prompt: nothing was typed, bash itself
					// prints "exit" - the command that ended the session.
					currentCommand.InputFrames = append(currentCommand.InputFrames, frame)
					currentCommand.InputText = reconstructInput(currentCommand.InputFrames, currentCommand.PromptFrame, currentCommand.ContinuationPrompt)
					inputPhase = false
					continue
				}
				currentCommand.InputText = reconstructInput(currentCommand.InputFrames, currentCommand.PromptFrame, currentCommand.ContinuationPrompt)
				inputPhase, pending, held = false, true, nil
				data = data[enterAt:]
				frame.Data = data
				if data == "" {
					continue
				}
				if visibleText(data) == "" {
					held = append(held, frame)
					continue
				}
				pending = false
				if p, ok := continuationPrompt(data); ok {
					currentCommand.InputFrames = append(currentCommand.InputFrames, frame)
					currentCommand.ContinuationPrompt = p
					inputPhase = true
					continue
				}
				// fall through to the output handling below
			} else {
				currentCommand.InputFrames = append(currentCommand.InputFrames, frame)

				// Check if input ends (a short frame with a newline in it - the
				// shell without bracketed paste)
				if isUserInput(data) && strings.Contains(data, "\n") {
					currentCommand.InputText = reconstructInput(currentCommand.InputFrames, currentCommand.PromptFrame, currentCommand.ContinuationPrompt)
					inputPhase, pending, held = false, true, nil
				}
				continue
			}
		}

		// We're in output phase - accumulate until we see next prompt
		if !inputPhase {
			// Check if we've reached the next prompt
			if isRealPrompt(data, prompt) {
				// Output read in the same chunk as the next prompt precedes it:
				// it belongs to the command that just ended, and only the
				// prompt itself to the next one (again, a partition of the
				// frame's bytes).
				if cut := promptStartIndex(data); cut > 0 && currentCommand != nil {
					out := frame
					out.Data = data[:cut]
					currentCommand.OutputFrames = append(currentCommand.OutputFrames, out)
					frame.Data = data[cut:]
				}
				// Save current command
				if currentCommand != nil && len(currentCommand.InputFrames) > 0 {
					currentCommand.OutputText = deriveOutputText(currentCommand.OutputFrames)
					currentCommand.OutputTextRaw = reconstructOutput(currentCommand.OutputFrames)
					currentCommand.EndTime = frames[i-1].Timestamp
					currentCommand.LastRawFrameIndex = i - 1
					commands = append(commands, *currentCommand)
				}
				// Start new command - this prompt frame is what will be
				// displayed right before whatever the user types next, so
				// it belongs to the command we're about to open.
				currentCommand = &Command{
					InputFrames:        []TerminalFrame{},
					OutputFrames:       []TerminalFrame{},
					StartTime:          frame.Timestamp,
					PromptFrame:        frame,
					HasPrompt:          true,
					FirstRawFrameIndex: i,
				}
				inputPhase = true
			} else {
				// Accumulate output
				if currentCommand != nil {
					currentCommand.OutputFrames = append(currentCommand.OutputFrames, frame)
				}
			}
		}
	}

	if pending && currentCommand != nil { // the recording ended right after Enter
		currentCommand.OutputFrames = append(currentCommand.OutputFrames, held...)
	}

	// Save last command
	if currentCommand != nil && len(currentCommand.InputFrames) > 0 {
		currentCommand.InputText = reconstructInput(currentCommand.InputFrames, currentCommand.PromptFrame, currentCommand.ContinuationPrompt)
		currentCommand.OutputText = deriveOutputText(currentCommand.OutputFrames)
		currentCommand.OutputTextRaw = reconstructOutput(currentCommand.OutputFrames)
		if len(frames) > 0 {
			currentCommand.EndTime = frames[len(frames)-1].Timestamp
			currentCommand.LastRawFrameIndex = len(frames) - 1
		}
		commands = append(commands, *currentCommand)
	}

	return dropSessionEnd(commands), nil
}

// isSetupFrame detects frames that are just window title/setup, not actual commands
func isSetupFrame(data string) bool {
	// Real prompts contain $ or # or >, setup frames don't
	if strings.Contains(data, "$") || strings.Contains(data, "#") || strings.Contains(data, ">") {
		return false
	}
	// Frames with just title update and escape codes, no real content
	return strings.Contains(data, "\x1b]0;") && !containsReturnOrNewline(data) && len(data) < 150
}

// isUserInput checks if this frame is actual user input (not output, not setup)
func isUserInput(data string) bool {
	// If it has window title sequences, it's not input
	if strings.Contains(data, "\x1b]0;") {
		return false
	}

	// If it has too many color/cursor sequences, it's probably output
	if strings.Count(data, "\x1b[") > 2 {
		return false
	}

	// Input is typically short
	if len(data) > 50 {
		return false
	}

	return true
}

// isRealPrompt detects the actual shell prompt
// promptStartIndex is where the prompt itself begins in a frame: its
// bracketed-paste switch or its window-title sequence, whichever comes first
// (-1 if neither).
func promptStartIndex(data string) int {
	at := -1
	for _, marker := range []string{"\x1b[?2004h", "\x1b]0;"} {
		if i := strings.Index(data, marker); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	return at
}

func isRealPrompt(data string, prompt string) bool {
	// Real prompt must have:
	// 1. The actual prompt character
	// 2. The title sequence (indicates fresh prompt line)

	hasPromptChar := strings.Contains(data, "$") || strings.Contains(data, "#") || strings.Contains(data, ">")
	hasTitle := strings.Contains(data, "\x1b]0;")

	return hasPromptChar && hasTitle
}

func containsReturnOrNewline(data string) bool {
	return strings.Contains(data, "\r") || strings.Contains(data, "\n")
}

// lineEditor is a one-line terminal: just enough of one to follow what readline
// does to the line being edited - which is far more than typing and backspace.
// Ctrl-R (reverse-i-search) redraws the whole line, led by a CR, and rewrites it
// as you type; history recall erases and rewrites; Ctrl-A/E and the arrow keys
// move the cursor so later keystrokes land mid-line. Only a terminal model gets
// all of these right, so the command is read off the line the way the screen
// showed it.
type lineEditor struct {
	// A command can run over several lines (a trailing backslash, an open
	// quote), each one a row of its own.
	rows [][]rune
	r    int
	col  int
}

func newLineEditor() *lineEditor { return &lineEditor{rows: [][]rune{nil}} }

func (e *lineEditor) reset() {
	e.rows, e.r, e.col = [][]rune{nil}, 0, 0
}

func (e *lineEditor) lines() []string {
	out := make([]string, len(e.rows))
	for i, r := range e.rows {
		out[i] = string(r)
	}
	return out
}

func (e *lineEditor) write(data string) {
	rs := []rune(data)
	for i := 0; i < len(rs); {
		ch := rs[i]
		switch {
		case ch == 0x1b:
			i = e.escape(rs, i)
			continue
		case ch == '\r':
			e.col = 0
		case ch == '\n':
			if e.r++; e.r >= len(e.rows) {
				e.rows = append(e.rows, nil)
			}
		case ch == '\b':
			e.col = max(0, e.col-1)
		case ch >= 32 && ch != 0x7f:
			e.put(ch)
		}
		// anything else (BEL, tab...) doesn't change the line
		i++
	}
}

func (e *lineEditor) put(ch rune) {
	row := e.rows[e.r]
	for len(row) < e.col {
		row = append(row, ' ')
	}
	if e.col < len(row) {
		row[e.col] = ch
	} else {
		row = append(row, ch)
	}
	e.rows[e.r] = row
	e.col++
}

// escape returns the index just past the escape sequence starting at rs[i].
func (e *lineEditor) escape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return i + 1
	}
	switch rs[i+1] {
	case '[':
		j := i + 2
		for j < len(rs) && (rs[j] < '@' || rs[j] > '~') {
			j++
		}
		if j < len(rs) {
			e.csi(string(rs[i+2:j]), rs[j])
		}
		return j + 1
	case ']', 'P', '_', '^', 'X': // a string sequence (title, ...): up to BEL or ESC \
		// A window title (OSC 0/2) is written just before the prompt: a prompt
		// being drawn again - after Ctrl-L, a completion listing, an i-search -
		// starts the line over, so whatever was on screen before it is not part
		// of the command.
		if rs[i+1] == ']' && i+3 < len(rs) && (rs[i+2] == '0' || rs[i+2] == '2') && rs[i+3] == ';' {
			e.reset()
		}
		j := i + 2
		for j < len(rs) && rs[j] != 0x07 && !(rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\') {
			j++
		}
		if j < len(rs) && rs[j] == 0x1b {
			return j + 2
		}
		return j + 1
	}
	j := i + 1 // ESC, intermediates (charset selection), final byte
	for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
		j++
	}
	return j + 1
}

func (e *lineEditor) csi(params string, final rune) {
	p := strings.TrimLeft(params, "?>=!")
	if k := strings.IndexByte(p, ';'); k >= 0 {
		p = p[:k]
	}
	n, err := strconv.Atoi(p)
	has := err == nil
	k := 1
	if has && n > 0 {
		k = n
	}
	row := e.rows[e.r]
	switch final {
	case 'K': // erase in line
		mode := 0
		if has {
			mode = n
		}
		switch mode {
		case 0:
			if e.col < len(row) {
				row = row[:e.col]
			}
		case 1:
			for c := 0; c <= e.col && c < len(row); c++ {
				row[c] = ' '
			}
		default:
			row = nil
		}
	case 'P': // delete characters
		if e.col < len(row) {
			end := min(e.col+k, len(row))
			row = append(row[:e.col], row[end:]...)
		}
	case '@': // insert blanks
		at := min(e.col, len(row))
		out := make([]rune, 0, len(row)+k)
		out = append(out, row[:at]...)
		for range k {
			out = append(out, ' ')
		}
		row = append(out, row[at:]...)
	case 'X': // erase characters
		for c := e.col; c < e.col+k && c < len(row); c++ {
			row[c] = ' '
		}
	case 'A': // cursor up (readline redrawing a multi-line command)
		e.r = max(0, e.r-k)
	case 'B': // cursor down
		e.r += k
		for len(e.rows) <= e.r {
			e.rows = append(e.rows, nil)
		}
	case 'J': // clear screen
		if !has || n == 2 || n == 3 {
			e.reset()
		}
	case 'H', 'f': // cursor home
		e.r, e.col = 0, 0
	case 'C':
		e.col += k
	case 'D':
		e.col = max(0, e.col-k)
	case 'G':
		e.col = max(0, k-1)
	}
	switch final {
	case 'A', 'B', 'J', 'H', 'f': // these moved the cursor off this row (or cleared it)
	default:
		e.rows[e.r] = row
	}
}

// reconstructInput is the command that was typed: what the line says after the
// prompt when Enter was pressed, read off a model of the terminal (see
// lineEditor). promptFrame is the frame that drew the prompt. A command that ran
// over several lines comes back with its line breaks; ps2 is the continuation
// prompt ("> ") the shell drew in front of each line after the first, which is
// not part of the command.
func reconstructInput(frames []TerminalFrame, promptFrame TerminalFrame, ps2 string) string {
	e := newLineEditor()
	promptData := promptFrame.Data
	e.write(promptData[strings.LastIndex(promptData, "\n")+1:])
	prompt := e.lines()[0]
	if prompt == "" {
		// The prompt frame held no prompt - the first command's often doesn't: a
		// recording starts with a frame that only switches modes, and the prompt
		// is drawn in the next one, which is among the input frames. Take it
		// from there.
		for _, f := range frames {
			if strings.Contains(f.Data, "\x1b]0;") {
				d := newLineEditor()
				d.write(f.Data[strings.LastIndex(f.Data, "\n")+1:])
				prompt = d.lines()[0]
				break
			}
		}
	}
	for _, f := range frames {
		e.write(f.Data)
	}
	lines := e.lines()
	if prompt != "" && !strings.HasPrefix(lines[0], prompt) {
		// The line no longer starts with the prompt (one that changes between
		// draws, say): fall back to following the keystrokes.
		return reconstructInputByKeystrokes(frames)
	}
	out := []string{lines[0][len(prompt):]}
	for _, l := range lines[1:] {
		if ps2 != "" {
			l = strings.TrimPrefix(l, ps2)
		}
		out = append(out, l)
	}
	for i := range out {
		out[i] = strings.TrimRight(out[i], " \t")
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

var reEscape = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|[\\]PX^_][^\x07\x1b]*(?:\x07|\x1b\\\\)|[ -/]*[0-~])")

func visibleText(d string) string {
	d = reEscape.ReplaceAllString(d, "")
	return strings.NewReplacer("\r", "", "\n", "").Replace(d)
}

// continuationPrompt reads what followed an Enter. After Enter the shell either
// runs the command or asks for more of it (a trailing backslash, an open
// quote). When it asks, it draws its continuation prompt (PS2, "> ") and -
// unlike a command that runs - switches bracketed paste straight back on to read
// the next line, with no window title in the prompt. That tells a continuation
// from output that merely begins with "> ". It returns the PS2 text.
func continuationPrompt(afterEnter string) (string, bool) {
	if strings.Contains(afterEnter, "\x1b]0;") {
		return "", false // a real prompt: it ran (and printed nothing)
	}
	// A full-screen program (vim, htop) also switches bracketed paste back on, and
	// then paints a screen: that is not a prompt asking for more of the command.
	if altScreenRe.MatchString(afterEnter) {
		return "", false
	}
	const on = "\x1b[?2004h"
	at := strings.LastIndex(afterEnter, on)
	if at < 0 {
		return "", false
	}
	ps2 := visibleText(afterEnter[at+len(on):])
	return ps2, ps2 != "" && len([]rune(ps2)) <= 24 // a PS2 is a few characters: "> ", "dquote> "
}

// CollapseCommand puts a command on one line, for the places that have room for just
// one (the editor's command list): line continuations (a
// trailing backslash) are removed and the lines joined. It reads the same as
// what was typed; InputText keeps the full text, line breaks included.
func CollapseCommand(cmd string) string {
	var parts []string
	lines := strings.Split(cmd, "\n")
	for i, l := range lines {
		if i < len(lines)-1 {
			l = strings.TrimRight(l, " \t")
			l = strings.TrimSuffix(l, "\\")
		}
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// enterIndex is where the input ends in a frame - the cut, such that
// data[:cut] is the last of it and the rest is what the shell did next - or -1.
// Enter is the CRLF the shell echoes; bash follows it by switching bracketed
// paste off, and that switch alone is reliable even when no CRLF comes with it
// (a recalled multi-line command is redrawn with its CRLF in one write and the
// switch in the next). So: a frame starting with CRLF, or CRLF then the switch,
// or the switch itself, wherever it is.
func enterIndex(data string) int {
	if strings.HasPrefix(data, "\r\n") {
		return 2
	}
	if i := strings.Index(data, "\r\n\x1b[?2004l"); i >= 0 {
		return i + 2
	}
	return strings.Index(data, "\x1b[?2004l")
}

// reconstructInput builds the actual command typed by handling backspaces
// reconstructInput builds the actual command typed by handling backspaces and escape sequences
func reconstructInputByKeystrokes(frames []TerminalFrame) string {
	var result []rune

	// Concatenate all frame data
	var sb strings.Builder
	for _, frame := range frames {
		sb.WriteString(frame.Data)
	}
	data := sb.String()

	// Process character by character, skipping escape sequences
	for i := 0; i < len(data); {
		ch := rune(data[i])

		// Handle escape sequences
		if ch == '\x1b' {
			// Skip entire escape sequence
			i++
			if i < len(data) && data[i] == '[' {
				// CSI sequence: ESC [ ... letter
				i++
				for i < len(data) && (data[i] < 'A' || data[i] > 'Z') && (data[i] < 'a' || data[i] > 'z') {
					i++
				}
				if i < len(data) {
					i++
				}
				continue
			} else if i < len(data) && data[i] == ']' {
				// OSC sequence: ESC ] ... BEL
				i++
				for i < len(data) && data[i] != '\x07' {
					i++
				}
				if i < len(data) {
					i++
				}
				continue
			}
			continue
		}

		// Handle backspace
		if ch == '\b' || ch == 0x7F {
			if len(result) > 0 {
				result = result[:len(result)-1]
			}
			i++
			continue
		}

		// Skip other control characters except newline/return
		if ch < 32 && ch != '\n' && ch != '\r' {
			i++
			continue
		}

		result = append(result, ch)
		i++
	}

	str := string(result)
	str = strings.TrimSuffix(str, "\r\n")
	str = strings.TrimSuffix(str, "\n")
	return strings.TrimSpace(str)
}

// reconstructOutput builds output text from frames
func reconstructOutput(frames []TerminalFrame) string {
	var result strings.Builder
	for _, frame := range frames {
		result.WriteString(frame.Data)
	}
	return result.String()
}

// stripEscapeSequences strips ANSI escape codes for readable display
func stripEscapeSequences(data string) string {
	var result strings.Builder
	for i := 0; i < len(data); {
		if data[i] == 0x1b && i+1 < len(data) {
			i = skipEscape(data, i)
			continue
		}
		// Skip control characters except newline/carriage return
		if data[i] < 32 && data[i] != '\n' && data[i] != '\r' {
			i++
			continue
		}
		result.WriteByte(data[i])
		i++
	}
	return result.String()
}

// skipEscape returns the index just past the escape sequence that starts at
// data[i] (an ESC). Every form is skipped whole, not just the common ones:
//
//	ESC [ ...    CSI: parameter bytes 0x30-0x3F (which include < = > ?), intermediates
//	             0x20-0x2F, and ONE final byte 0x40-0x7E - not only a letter: "~", "@"
//	             and "`" end sequences too (ESC[200~, ESC[3~)
//	ESC ] ...    OSC, and likewise DCS/SOS/PM/APC (ESC P, X, ^, _): up to BEL or ST (ESC \)
//	ESC =  ESC > ESC 7  ESC 8  ESC M  ESC ( B ...   two-byte forms, with any intermediates
//
// A shell like fish sends ESC = and ESC > around every prompt; skipping only
// CSI and OSC left a stray "=" or ">" in the text.
func skipEscape(data string, i int) int {
	switch data[i+1] {
	case '[':
		j := i + 2
		for j < len(data) && data[j] >= 0x30 && data[j] <= 0x3f {
			j++
		}
		for j < len(data) && data[j] >= 0x20 && data[j] <= 0x2f {
			j++
		}
		if j < len(data) && data[j] >= 0x40 && data[j] <= 0x7e {
			j++
		}
		return j
	case ']', 'P', 'X', '^', '_':
		j := i + 2
		for j < len(data) && data[j] != 0x07 && data[j] != 0x1b {
			j++
		}
		switch {
		case j >= len(data):
			return j
		case data[j] == 0x07:
			return j + 1
		case j+1 < len(data) && data[j+1] == '\\':
			return j + 2
		}
		return j // a bare ESC: the next sequence starts there
	}
	j := i + 1
	for j < len(data) && data[j] >= 0x20 && data[j] <= 0x2f {
		j++
	}
	if j < len(data) {
		j++
	}
	return j
}

// syncCommandFrames ensures a command's InputFrames/OutputFrames reflect
// whatever is currently in InputText/OutputText. If the text still matches
// what the original frames reconstruct to, the original frames (with their
// real per-keystroke timing) are left untouched. If the text has been
// edited - i.e. no longer matches - the frames are replaced with a single
// synthetic frame carrying the new text, since the original per-keystroke
// timing no longer corresponds to anything meaningful.
func syncCommandFrames(cmd *Command) {
	if commandInputText(cmd) != cmd.InputText {
		setMarkedCommandLine(cmd.OutputFrames, cmd.InputText)
		ts := cmd.StartTime
		if len(cmd.InputFrames) > 0 {
			ts = cmd.InputFrames[0].Timestamp
		}
		cmd.InputFrames = []TerminalFrame{
			{Timestamp: ts, Data: cmd.InputText + "\r\n"},
		}
	}

	if deriveOutputText(cmd.OutputFrames) != cmd.OutputText {
		ts := cmd.EndTime
		if len(cmd.OutputFrames) > 0 {
			ts = cmd.OutputFrames[0].Timestamp
		}
		cmd.OutputFrames = []TerminalFrame{
			{Timestamp: ts, Data: strings.ReplaceAll(cmd.OutputText, "\n", "\r\n")},
		}
	}
}

// CompressTimestamps returns a new slice containing only the commands not
// present in deleted (keyed by index into the original commands slice, the
// same indexing the editor tracks deletions with), with every frame's
// timestamp - including each surviving command's PromptFrame - shifted so
// that no dead time remains where a deleted command used to be: a
// surviving command now begins exactly where the previous surviving
// command's last frame ended, rather than at its own original timestamp
// with a gap the size of everything deleted in between still sitting in
// front of it. Natural pacing elsewhere (the gap between a command's own
// output ending and its own prompt reappearing, for instance) is
// untouched, since that isn't attributable to anything deleted.
//
// commands is not modified; the returned commands are copies with their
// own, independent frame slices.
func CompressTimestamps(commands []Command, deleted map[int]bool) []Command {
	var result []Command
	var shift int64

	for i, cmd := range commands {
		if deleted[i] {
			start := commandFirstTimestamp(cmd)
			end := commandLastTimestamp(cmd)
			if end > start {
				shift += end - start
			}
			continue
		}

		shifted := cmd
		if shifted.HasPrompt {
			shifted.PromptFrame.Timestamp -= shift
		}
		shifted.InputFrames = shiftFrameTimestamps(cmd.InputFrames, shift)
		shifted.OutputFrames = shiftFrameTimestamps(cmd.OutputFrames, shift)
		shifted.StartTime -= shift
		shifted.EndTime -= shift
		result = append(result, shifted)
	}

	return result
}

// commandFirstTimestamp returns the earliest timestamp belonging to cmd -
// its prompt frame if it has one, otherwise its first input frame.
func commandFirstTimestamp(cmd Command) int64 {
	if cmd.HasPrompt {
		return cmd.PromptFrame.Timestamp
	}
	if len(cmd.InputFrames) > 0 {
		return cmd.InputFrames[0].Timestamp
	}
	return cmd.StartTime
}

// commandLastTimestamp returns the latest timestamp belonging to cmd - its
// last output frame if it produced any, otherwise its last input frame.
func commandLastTimestamp(cmd Command) int64 {
	if len(cmd.OutputFrames) > 0 {
		return cmd.OutputFrames[len(cmd.OutputFrames)-1].Timestamp
	}
	if len(cmd.InputFrames) > 0 {
		return cmd.InputFrames[len(cmd.InputFrames)-1].Timestamp
	}
	return cmd.EndTime
}

// shiftFrameTimestamps returns a copy of frames with shift subtracted from
// every timestamp.
func shiftFrameTimestamps(frames []TerminalFrame, shift int64) []TerminalFrame {
	if len(frames) == 0 {
		return frames
	}
	out := make([]TerminalFrame, len(frames))
	for i, f := range frames {
		f.Timestamp -= shift
		out[i] = f
	}
	return out
}

// RebuildRecording takes edited commands and rewrites the JSON file,
// including each command's annotation, if it has one. A newly-added
// annotation (created in the editor, with no ID yet) is assigned one here,
// along with a timestamp anchored to its command's start and a CreatedAt,
// so it round-trips through future saves like any other.
func RebuildRecording(originalPath string, backupPath string, commands []Command) error {
	input, err := os.ReadFile(originalPath)
	if err != nil {
		return fmt.Errorf("failed to read original: %w", err)
	}
	if err := os.WriteFile(backupPath, input, 0644); err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	var newFrames []TerminalFrame
	var annotations []Annotation

	for i := range commands {
		cmd := &commands[i]
		syncCommandFrames(cmd)

		if cmd.HasPrompt {
			newFrames = append(newFrames, cmd.PromptFrame)
		}
		newFrames = append(newFrames, cmd.InputFrames...)
		newFrames = append(newFrames, cmd.OutputFrames...)

		if cmd.HasAnnotation && strings.TrimSpace(cmd.Annotation.Text) != "" {
			if cmd.Annotation.ID == "" {
				cmd.Annotation.ID = fmt.Sprintf("ann_%x", time.Now().UnixNano())
			}
			if cmd.Annotation.CreatedAt == "" {
				cmd.Annotation.CreatedAt = time.Now().UTC().Format(time.RFC3339)
			}
			cmd.Annotation.Timestamp = commandFirstTimestamp(*cmd)
			annotations = append(annotations, cmd.Annotation)
		}
	}

	file, err := os.Create(originalPath)
	if err != nil {
		return fmt.Errorf("failed to create new recording: %w", err)
	}
	defer func() { _ = file.Close() }()

	encoder := json.NewEncoder(file)
	for _, frame := range newFrames {
		line := recordingLine{Type: "frame", Timestamp: frame.Timestamp, Data: frame.Data, Width: frame.Width, Height: frame.Height}
		if err := encoder.Encode(line); err != nil {
			return fmt.Errorf("failed to encode frame: %w", err)
		}
	}
	for _, ann := range annotations {
		line := recordingLine{Type: "annotation", ID: ann.ID, Timestamp: ann.Timestamp, Text: ann.Text, Author: ann.Author, CreatedAt: ann.CreatedAt, DurationSeconds: ann.DurationSeconds}
		if err := encoder.Encode(line); err != nil {
			return fmt.Errorf("failed to encode annotation: %w", err)
		}
	}

	return nil
}

// GetCommandIndexAtFrame returns the index of the command whose raw frame
// range [FirstRawFrameIndex, LastRawFrameIndex] contains frameIndex.
// frameIndex must be an index into the original, unfiltered frame list (the
// same list the player counts through) - not a count over InputFrames and
// OutputFrames, which omit frames the parser discarded and therefore drift
// out of sync with the player's real position.
func GetCommandIndexAtFrame(frameIndex int, commands []Command) int {
	for i := range commands {
		if frameIndex >= commands[i].FirstRawFrameIndex && frameIndex <= commands[i].LastRawFrameIndex {
			return i
		}
	}
	// frameIndex fell in a gap (a discarded setup frame) between two
	// commands, or past the end of the last one - attribute it to the
	// nearest command that had already started.
	best := -1
	for i := range commands {
		if commands[i].FirstRawFrameIndex <= frameIndex {
			best = i
		}
	}
	return best
}

// GetCommandAtFrameIndex returns the command containing the given raw frame
// index. See GetCommandIndexAtFrame for what frameIndex must be.
func GetCommandAtFrameIndex(frameIndex int, commands []Command) *Command {
	idx := GetCommandIndexAtFrame(frameIndex, commands)
	if idx < 0 {
		return nil
	}
	return &commands[idx]
}

// FilterForDisplay strips all escape sequences except SGR (colour) codes.
// Deprecated: kept for reference/backward compatibility, but no longer used
// by the editor - it discards backspace bytes rather than executing them,
// so corrections made while typing never visually disappear in a
// TextView-based playback pane, unlike DisplayBuffer below. Use
// DisplayBuffer for anything rendering live playback in a widget.
func FilterForDisplay(data string) string {
	var result strings.Builder
	i := 0
	for i < len(data) {
		// CSI sequence: ESC [ ... letter
		if i < len(data)-1 && data[i] == '\x1b' && data[i+1] == '[' {
			start := i
			i += 2
			for i < len(data) && (data[i] < 'A' || data[i] > 'Z') && (data[i] < 'a' || data[i] > 'z') {
				i++
			}
			if i < len(data) {
				final := data[i]
				i++
				if final == 'm' {
					// Keep SGR (colour) sequences as-is
					result.WriteString(data[start:i])
				}
				// Otherwise discard (cursor movement, erase, etc.)
			}
			continue
		}

		// OSC sequence: ESC ] ... BEL or ST — always discard (window title, etc.)
		if i < len(data)-1 && data[i] == '\x1b' && data[i+1] == ']' {
			i += 2
			for i < len(data) && data[i] != '\x07' && data[i] != '\x1b' {
				i++
			}
			if i < len(data) {
				i++
			}
			if i < len(data) && data[i] == '\\' {
				i++
			}
			continue
		}

		// Discard lone ESC and other control chars except newline/carriage return
		if data[i] == '\x1b' {
			i++
			continue
		}
		if data[i] < 32 && data[i] != '\n' && data[i] != '\r' {
			i++
			continue
		}

		result.WriteByte(data[i])
		i++
	}
	return result.String()
}

// cell is one character position on the emulated screen grid: the rune
// displayed there and the colour/attribute tag active when it was written.
type cell struct {
	ch  rune
	tag string
}

// DisplayBuffer is a small terminal emulator: a 2D grid of cells plus a
// real cursor position, driven by the same escape sequences a real
// terminal interprets (cursor positioning, erase-display, erase-line,
// SGR colour), for any display surface that can't run an actual PTY itself
// (a tview.TextView, or a web <div>).
//
// This replaces an earlier, simpler model that only ever appended
// characters and treated backspace as "delete the previous character".
// That worked for ordinary shell line-editing (backspace during typing),
// but full-screen, cursor-addressed programs like htop or onefetch don't
// use backspace at all - they redraw specific screen regions via absolute
// cursor positioning (CUP) and erase sequences, which the old model simply
// discarded, so every redraw just piled up as one long garbled scroll
// instead of overwriting the same cells in place. A real cursor and grid
// handles both cases correctly, including the original backspace-editing
// one: backspace now genuinely moves the cursor left (as it does on a real
// terminal), and a following erase-in-line clears from there to the end of
// the row, which is exactly the `\b`+`ESC[K` pattern shells emit.
//
// The grid has no fixed size: it grows as content addresses new rows or
// columns, rather than assuming a terminal size up front. Recordings don't
// reliably carry accurate width/height per frame, and a program like htop
// only ever needs as many rows as the real terminal had, so growing
// on demand converges on the right size without having to guess it.
//
// Known limitation: line-wrap at a fixed column width isn't emulated
// (there being no fixed width to wrap at), and alternate character sets
// (line-drawing mode) are ignored - like the vim case already noted
// elsewhere, this is a practical terminal emulator, not a complete one.
type DisplayBuffer struct {
	grid [][]cell

	cursorRow int
	cursorCol int
	// cursorVisible tracks DECTCEM (ESC[?25h/l). A real terminal's own
	// blinking cursor is how a person sees where they are in a full-screen
	// program like vim: moving with the arrow keys inside the already-visible
	// part of a file sends nothing but a bare cursor-position sequence - no
	// character is rewritten anywhere - so the block cursor is the entire
	// visual signal that anything happened. Render() paints it in reverse
	// video at (cursorRow, cursorCol); without that, only whatever an
	// application separately redraws (e.g. vim's ruler in the corner) would
	// ever appear to change; everything else the person actually navigated
	// past would look frozen even though the emulated cursor moved correctly.
	cursorVisible bool

	fgColor string
	bgColor string
	bold    bool
}

// NewDisplayBuffer creates an empty display buffer. The cursor starts
// visible, matching a real terminal's default (DECTCEM) state before any
// program explicitly hides it.
func NewDisplayBuffer() *DisplayBuffer {
	return &DisplayBuffer{cursorVisible: true}
}

// IsBlank reports whether nothing visible is on the display: every cell is
// empty or a plain, default-coloured space. A coloured space is visible
// (a background-coloured bar is made of them), so it counts as content.
func (db *DisplayBuffer) IsBlank() bool {
	for _, row := range db.grid {
		for _, c := range row {
			if c.ch != 0 && c.ch != ' ' {
				return false
			}
			if c.tag != "" && c.tag != "-:-:-" {
				return false
			}
		}
	}
	return true
}

// currentTag returns a key identifying the buffer's current colour/attribute
// state, in "fg:bg:attrs" form as tview expects it. Colours are always
// stored as "#rrggbb" hex (tcell's markup parser accepts hex directly), so
// the same representation covers the standard 16 colours, 256-colour
// palette indices, and 24-bit truecolour uniformly - see applySGR.
func (db *DisplayBuffer) currentTag() string {
	fg := db.fgColor
	if fg == "" {
		fg = "-"
	}
	bg := db.bgColor
	if bg == "" {
		bg = "-"
	}
	attrs := "-"
	if db.bold {
		attrs = "b"
	}
	return fg + ":" + bg + ":" + attrs
}

// ansi16Hex is the standard 16-colour ANSI palette (indices 0-7 normal,
// 8-15 bright), in the Tango/GNOME Terminal shades already used
// elsewhere in this codebase, expressed as hex so they compose uniformly
// with 256-colour and truecolour SGR codes, which are hex by nature.
var ansi16Hex = [16]string{
	"#000000", "#cc0000", "#4e9a06", "#c4a000",
	"#3465a4", "#75507b", "#06989a", "#d3d7cf",
	"#555753", "#ef2929", "#8ae234", "#fce94f",
	"#729fcf", "#ad7fa8", "#34e2e2", "#eeeeec",
}

// xterm256Hex converts a 256-colour palette index (as used by the SGR
// "38;5;n" / "48;5;n" extended colour sequences) to a hex colour: indices
// 0-15 are the standard 16-colour palette, 16-231 form a 6x6x6 RGB cube,
// and 232-255 are a 24-step grayscale ramp - the standard xterm mapping.
func xterm256Hex(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 16 {
		return ansi16Hex[n]
	}
	if n <= 231 {
		levels := [6]int{0, 95, 135, 175, 215, 255}
		n -= 16
		r := levels[(n/36)%6]
		g := levels[(n/6)%6]
		b := levels[n%6]
		return rgbHex(r, g, b)
	}
	if n > 255 {
		n = 255
	}
	gray := 8 + (n-232)*10
	return rgbHex(gray, gray, gray)
}

func rgbHex(r, g, b int) string {
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return v
	}
	const hexDigits = "0123456789abcdef"
	r, g, b = clamp(r), clamp(g), clamp(b)
	buf := [7]byte{'#'}
	vals := [3]int{r, g, b}
	for i, v := range vals {
		buf[1+i*2] = hexDigits[v>>4]
		buf[2+i*2] = hexDigits[v&0xF]
	}
	return string(buf[:])
}

// applySGR updates the buffer's current colour/attribute state from the
// numeric, semicolon-separated parameters of an SGR (ESC [ ... m)
// sequence. Handles the standard 16 foreground/background codes, their
// bright variants, and the extended "38;5;n"/"48;5;n" (256-colour) and
// "38;2;r;g;b"/"48;2;r;g;b" (truecolour) forms used by tools like onefetch
// that go well beyond the basic 16-colour palette - those forms consume
// two or four parameters beyond the initial 38/48, so parameters are
// walked with an index rather than handled one at a time independently.
func (db *DisplayBuffer) applySGR(params string) {
	if params == "" {
		db.fgColor = ""
		db.bgColor = ""
		db.bold = false
		return
	}

	fields := strings.Split(params, ";")
	nums := make([]int, len(fields))
	for i, f := range fields {
		n := 0
		for _, c := range f {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
		}
		nums[i] = n
	}

	for i := 0; i < len(nums); i++ {
		code := nums[i]
		switch {
		case code == 0:
			db.fgColor = ""
			db.bgColor = ""
			db.bold = false
		case code == 1:
			db.bold = true
		case code == 22:
			db.bold = false
		case code == 39:
			db.fgColor = ""
		case code == 49:
			db.bgColor = ""
		case code >= 30 && code <= 37:
			db.fgColor = ansi16Hex[code-30]
		case code >= 90 && code <= 97:
			db.fgColor = ansi16Hex[8+code-90]
		case code >= 40 && code <= 47:
			db.bgColor = ansi16Hex[code-40]
		case code >= 100 && code <= 107:
			db.bgColor = ansi16Hex[8+code-100]
		case code == 38 || code == 48:
			if i+1 >= len(nums) {
				break
			}
			mode := nums[i+1]
			if mode == 5 && i+2 < len(nums) {
				hex := xterm256Hex(nums[i+2])
				if code == 38 {
					db.fgColor = hex
				} else {
					db.bgColor = hex
				}
				i += 2
			} else if mode == 2 && i+4 < len(nums) {
				hex := rgbHex(nums[i+2], nums[i+3], nums[i+4])
				if code == 38 {
					db.fgColor = hex
				} else {
					db.bgColor = hex
				}
				i += 4
			} else {
				// Unrecognised subformat - consume just the mode byte so
				// we don't misinterpret whatever follows as unrelated
				// SGR codes.
				i++
			}
		default:
			// Underline, blink, italic, strikethrough, etc. have no
			// representation here and are ignored.
		}
	}
}

// ensureRow grows the grid, if necessary, so row is a valid index.
func (db *DisplayBuffer) ensureRow(row int) {
	for row >= len(db.grid) {
		db.grid = append(db.grid, nil)
	}
}

// ensureCol grows the given row, if necessary, so col is a valid index,
// filling any newly-created cells with blanks in the default tag.
func (db *DisplayBuffer) ensureCol(row, col int) {
	r := db.grid[row]
	for col >= len(r) {
		r = append(r, cell{ch: ' ', tag: "-:-:-"})
	}
	db.grid[row] = r
}

// put writes ch at the given position under the buffer's current colour
// state, growing the grid as needed.
func (db *DisplayBuffer) put(row, col int, ch rune) {
	db.ensureRow(row)
	db.ensureCol(row, col)
	db.grid[row][col] = cell{ch: ch, tag: db.currentTag()}
}

// clearRange blanks cells (r0,c0) through (r1,c1) inclusive, in reading
// order (left-to-right, top-to-bottom) - used by erase-in-display and
// erase-in-line, both of which erase a contiguous reading-order range
// rather than a rectangular block.
func (db *DisplayBuffer) clearRange(r0, c0, r1, c1 int) {
	if r1 < r0 || (r1 == r0 && c1 < c0) {
		return
	}
	db.ensureRow(r1)
	for row := r0; row <= r1; row++ {
		db.ensureRow(row)
		startCol := 0
		if row == r0 {
			startCol = c0
		}
		endCol := len(db.grid[row]) - 1
		if row == r1 {
			endCol = c1
			db.ensureCol(row, c1)
		}
		for col := startCol; col <= endCol && col < len(db.grid[row]); col++ {
			db.grid[row][col] = cell{ch: ' ', tag: "-:-:-"}
		}
	}
}

// parseParams splits a CSI sequence's semicolon-separated numeric
// parameters, defaulting any empty or non-numeric field to 0 (the caller
// substitutes the sequence-appropriate default, usually 0 or 1).
func parseParams(params string) []int {
	if params == "" {
		return nil
	}
	fields := strings.Split(params, ";")
	nums := make([]int, len(fields))
	for i, f := range fields {
		n := 0
		for _, c := range f {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
		}
		nums[i] = n
	}
	return nums
}

func paramOr(nums []int, idx, def int) int {
	if idx >= len(nums) || nums[idx] == 0 {
		return def
	}
	return nums[idx]
}

// applyCSI handles one CSI sequence's effect on cursor position and screen
// content. params is everything between "ESC[" and the final byte
// (including any leading "?" for private-mode sequences); final is that
// last byte, which selects what the sequence does.
func (db *DisplayBuffer) applyCSI(params string, final byte) {
	private := strings.HasPrefix(params, "?")
	if private {
		params = params[1:]
	}
	nums := parseParams(params)

	switch final {
	case 'm':
		db.applySGR(params)

	case 'H', 'f':
		row := paramOr(nums, 0, 1) - 1
		col := paramOr(nums, 1, 1) - 1
		if row < 0 {
			row = 0
		}
		if col < 0 {
			col = 0
		}
		db.cursorRow, db.cursorCol = row, col
		db.ensureRow(row)
		db.ensureCol(row, col)

	case 'A': // cursor up
		db.cursorRow -= paramOr(nums, 0, 1)
		if db.cursorRow < 0 {
			db.cursorRow = 0
		}
	case 'B': // cursor down
		db.cursorRow += paramOr(nums, 0, 1)
		db.ensureRow(db.cursorRow)
	case 'C': // cursor forward
		db.cursorCol += paramOr(nums, 0, 1)
		db.ensureRow(db.cursorRow)
		db.ensureCol(db.cursorRow, db.cursorCol)
	case 'D': // cursor back
		db.cursorCol -= paramOr(nums, 0, 1)
		if db.cursorCol < 0 {
			db.cursorCol = 0
		}
	case 'E': // cursor next line
		db.cursorRow += paramOr(nums, 0, 1)
		db.cursorCol = 0
		db.ensureRow(db.cursorRow)
	case 'F': // cursor previous line
		db.cursorRow -= paramOr(nums, 0, 1)
		if db.cursorRow < 0 {
			db.cursorRow = 0
		}
		db.cursorCol = 0
	case 'G': // cursor horizontal absolute
		db.cursorCol = paramOr(nums, 0, 1) - 1
		if db.cursorCol < 0 {
			db.cursorCol = 0
		}
		db.ensureRow(db.cursorRow)
		db.ensureCol(db.cursorRow, db.cursorCol)
	case 'd': // vertical line position absolute
		db.cursorRow = paramOr(nums, 0, 1) - 1
		if db.cursorRow < 0 {
			db.cursorRow = 0
		}
		db.ensureRow(db.cursorRow)

	case 'J': // erase in display
		n := paramOr(nums, 0, 0)
		db.ensureRow(db.cursorRow)
		switch n {
		case 0:
			lastRow := len(db.grid) - 1
			lastCol := 0
			if lastRow >= 0 {
				lastCol = len(db.grid[lastRow]) - 1
			}
			db.clearRange(db.cursorRow, db.cursorCol, lastRow, lastCol)
		case 1:
			db.clearRange(0, 0, db.cursorRow, db.cursorCol)
		case 2, 3:
			for r := range db.grid {
				for c := range db.grid[r] {
					db.grid[r][c] = cell{ch: ' ', tag: "-:-:-"}
				}
			}
		}

	case 'K': // erase in line
		n := paramOr(nums, 0, 0)
		db.ensureRow(db.cursorRow)
		lastCol := len(db.grid[db.cursorRow]) - 1
		switch n {
		case 0:
			db.clearRange(db.cursorRow, db.cursorCol, db.cursorRow, lastCol)
		case 1:
			db.clearRange(db.cursorRow, 0, db.cursorRow, db.cursorCol)
		case 2:
			db.clearRange(db.cursorRow, 0, db.cursorRow, lastCol)
		}

	default:
		// Scroll-region (r) and window manipulation (t) have no
		// representation in this model and are safely ignored.
		//
		// Of the mode set/reset sequences (h/l), only cursor visibility
		// (DECTCEM, "?25") is tracked, since it's the one that changes what
		// Render() should draw. Everything else this private-mode family
		// covers - the alternate screen buffer (1049), bracketed paste
		// (2004), focus reporting (1004), and so on - has no visible
		// representation in a single flat grid and is ignored the same way.
		if private && (final == 'h' || final == 'l') {
			for _, n := range nums {
				if n == 25 {
					db.cursorVisible = final == 'h'
				}
			}
		}
	}
}

// Feed processes one more raw frame of terminal data, updating the
// emulated screen: printable characters are written at the cursor
// position under the currently active colour/attribute state and advance
// the cursor; CSI sequences move the cursor, erase screen content, or
// update colour state as handled by applyCSI; backspace/DEL move the
// cursor left (real terminal behaviour - erasure happens via a following
// erase-in-line, exactly the `\b`+`ESC[K` pattern shells emit, not via the
// backspace itself); carriage return moves the cursor to column 0; and
// newline moves the cursor down a row, growing the grid if needed.
func (db *DisplayBuffer) Feed(data string) {
	i := 0
	n := len(data)
	for i < n {
		ch := data[i]

		if ch == '\x1b' {
			i++
			if i >= n {
				break
			}
			switch data[i] {
			case '[':
				start := i + 1
				j := start
				for j < n && (data[j] < 'A' || data[j] > 'Z') && (data[j] < 'a' || data[j] > 'z') {
					j++
				}
				if j < n {
					db.applyCSI(data[start:j], data[j])
					i = j + 1
				} else {
					i = j
				}
			case ']':
				j := i + 1
				for j < n && data[j] != '\x07' {
					j++
				}
				i = j
				if i < n {
					i++
				}
			case '(', ')':
				// Character-set designation (e.g. ESC(B for ASCII):
				// ESC + intermediate + one final byte. Alternate
				// character sets (line-drawing mode) aren't emulated.
				i += 2
			case '=', '>':
				// Keypad application/normal mode: ESC + one byte.
				i++
			default:
				// Unrecognised single-byte ESC sequence: consume the one
				// byte so we always make forward progress.
				i++
			}
			continue
		}

		if ch == '\b' || ch == 0x7F {
			db.cursorCol--
			if db.cursorCol < 0 {
				db.cursorCol = 0
			}
			i++
			continue
		}

		if ch == '\r' {
			db.cursorCol = 0
			i++
			continue
		}

		if ch == '\n' {
			db.cursorRow++
			db.cursorCol = 0
			db.ensureRow(db.cursorRow)
			i++
			continue
		}

		if ch < 32 {
			// Other control characters (bell, etc.) have no visual effect.
			i++
			continue
		}

		r, size := utf8.DecodeRuneInString(data[i:])
		db.put(db.cursorRow, db.cursorCol, r)
		db.cursorCol++
		i += size
	}
}

// cellAt returns the cell at (row, col), or a default blank if that
// position hasn't been written to (out of bounds is not an error here: it
// just means nothing has drawn there yet, which is exactly what a blank
// cell represents).
func (db *DisplayBuffer) cellAt(row, col int) cell {
	if row < 0 || row >= len(db.grid) {
		return cell{ch: ' ', tag: "-:-:-"}
	}
	r := db.grid[row]
	if col < 0 || col >= len(r) {
		return cell{ch: ' ', tag: "-:-:-"}
	}
	return r[col]
}

// reverseTag returns tag with reverse video added, for painting the cursor.
// It cannot simply prepend "-" to mean "start from nothing": tview's markup
// parser treats a leading '-' in the attributes field as "reset to initial
// attributes" and stops there, discarding anything after it - "-r" would
// render as plain reset, not reset-then-reverse. Appending "r" (or "br" for
// an already-bold cell) is a request tview parses one flag at a time, only
// ever adding to the existing state, which is what's wanted here: the
// cursor should look like the underlying cell with its colours flipped, not
// like a plain space regardless of what was actually there.
func reverseTag(tag string) string {
	fg, bg, attrs := splitTag(tag)
	if attrs == "-" {
		attrs = "r"
	} else {
		attrs += "r"
	}
	return fg + ":" + bg + ":" + attrs
}

// Render produces the buffer's current content as a tview dynamic-colour
// markup string, ready to pass to TextView.SetText. Each row is trimmed of
// trailing blank cells before being joined with the next, so ordinary
// scrolling shell output doesn't carry a wall of trailing spaces on every
// line.
//
// When the cursor is visible, the cell it's on is painted in reverse video -
// see the cursorVisible field comment for why this matters well beyond
// cosmetics: for a full-screen program moving the cursor within its already-
// drawn viewport (arrow-key navigation in vim, for instance), this is often
// the ONLY visible change in the entire frame.
func (db *DisplayBuffer) Render() string {
	var out strings.Builder
	lastTag := ""
	for rowIdx := 0; rowIdx < len(db.grid); rowIdx++ {
		if rowIdx > 0 {
			out.WriteByte('\n')
		}
		row := db.grid[rowIdx]
		end := len(row)
		for end > 0 && row[end-1].ch == ' ' && row[end-1].tag == "-:-:-" {
			end--
		}

		cursorHere := db.cursorVisible && rowIdx == db.cursorRow
		if cursorHere && db.cursorCol >= end {
			// The cursor rests past this row's real content (a blank
			// tail, or a row that was never written this far at all).
			// Extend up to it so the cursor cell survives the trailing-
			// blank trim above instead of being cut off before it's drawn.
			end = db.cursorCol + 1
		}

		for col := 0; col < end; col++ {
			c := db.cellAt(rowIdx, col)
			tag := c.tag
			if cursorHere && col == db.cursorCol {
				tag = reverseTag(tag)
			}
			if tag != lastTag {
				fg, bg, attrs := splitTag(tag)
				out.WriteString("[" + fg + ":" + bg + ":" + attrs + "]")
				lastTag = tag
			}
			if c.ch == '[' {
				out.WriteString("[[")
			} else {
				out.WriteRune(c.ch)
			}
		}
	}
	return out.String()
}

func splitTag(tag string) (fg, bg, attrs string) {
	parts := strings.SplitN(tag, ":", 3)
	if len(parts) == 3 {
		return parts[0], parts[1], parts[2]
	}
	return "-", "-", "-"
}

// dropSessionEnd removes the last command if it is just the session ending.
func dropSessionEnd(commands []Command) []Command {
	// The last command in a Trecs recording is always "exit" (or Ctrl+D) -
	// that's simply how the recording session was stopped, not meaningful
	// recorded content, so it's dropped rather than shown as an editable
	// command. This has no effect on `recorder -play`, which plays the raw
	// frame stream directly and never groups it into commands at all; it
	// only affects the editor's list, and consequently anything saved
	// through it (rebuilding a recording after an edit naturally excludes
	// whatever isn't in this list).
	//
	// Checked against InputText OR InputText+OutputText together: the
	// shell's "turn off bracketed paste mode" sequence sometimes arrives
	// as its own frame right as the final prompt reappears, gets
	// misclassified as a complete (but empty) input on its own, and pushes
	// the actual "exit\r\n" keystrokes into that command's output instead
	// of its input - checking InputText alone misses that case entirely.
	if len(commands) > 0 {
		last := commands[len(commands)-1]
		trimmedInput := strings.TrimSpace(last.InputText)
		trimmedOutput := strings.TrimSpace(last.OutputText)
		if trimmedInput == "exit" || (trimmedInput == "" && trimmedOutput == "exit") {
			commands = commands[:len(commands)-1]
		}
	}

	return commands
}

// deriveOutputText is the text of what a command printed, as the editor shows
// and edits it: escape sequences gone, tabs expanded, CRLF a newline, and a
// carriage return overwriting the line so far - so a progress bar reads as its
// final state, and the "fresh line" padding a shell like fish prints after every
// command (a marker, a screenful of spaces, a CR) is not there. parse and
// syncCommandFrames both use it: sync tells an edit from no edit by re-deriving.
func deriveOutputText(frames []TerminalFrame) string {
	return CleanOutput(reconstructOutput(frames))
}
