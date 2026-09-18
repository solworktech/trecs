package lib

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ParseRecording reads a terminal.jsonl file and extracts commands
func ParseRecording(filePath string) ([]Command, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open recording file: %w", err)
	}
	defer file.Close()

	var frames []TerminalFrame
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		var frame TerminalFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return nil, fmt.Errorf("failed to parse frame: %w", err)
		}
		frames = append(frames, frame)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames found in recording")
	}

	// Extract prompt from first frame
	prompt := extractPrompt(frames[0].Data)

	// Group frames into commands
	return groupIntoCommands(frames, prompt)
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

// groupIntoCommands organizes frames into command input + output pairs
func groupIntoCommands(frames []TerminalFrame, prompt string) ([]Command, error) {
	var commands []Command
	var currentCommand *Command
	var inputPhase = true

	for i := 1; i < len(frames); i++ {
		frame := frames[i]
		data := frame.Data

		// Skip title/setup frames that aren't actual commands
		if isSetupFrame(data) {
			continue
		}

		// Check if this is actual input (single char or short string without heavy escaping)
		if inputPhase && isUserInput(data) {
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
			currentCommand.InputFrames = append(currentCommand.InputFrames, frame)

			// Check if input ends (newline)
			if containsReturnOrNewline(data) {
				currentCommand.InputText = reconstructInput(currentCommand.InputFrames)
				inputPhase = false
			}
			continue
		}

		// We're in output phase - accumulate until we see next prompt
		if !inputPhase {
			// Check if we've reached the next prompt
			if isRealPrompt(data, prompt) {
				// Save current command
				if currentCommand != nil && len(currentCommand.InputFrames) > 0 {
					currentCommand.OutputText = stripEscapeSequences(reconstructOutput(currentCommand.OutputFrames))
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

	// Save last command
	if currentCommand != nil && len(currentCommand.InputFrames) > 0 {
		currentCommand.InputText = reconstructInput(currentCommand.InputFrames)
		currentCommand.OutputText = stripEscapeSequences(reconstructOutput(currentCommand.OutputFrames))
		currentCommand.OutputTextRaw = reconstructOutput(currentCommand.OutputFrames)
		if len(frames) > 0 {
			currentCommand.EndTime = frames[len(frames)-1].Timestamp
			currentCommand.LastRawFrameIndex = len(frames) - 1
		}
		commands = append(commands, *currentCommand)
	}

	return commands, nil
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

// reconstructInput builds the actual command typed by handling backspaces
// reconstructInput builds the actual command typed by handling backspaces and escape sequences
func reconstructInput(frames []TerminalFrame) string {
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
				for i < len(data) && !((data[i] >= 'A' && data[i] <= 'Z') || (data[i] >= 'a' && data[i] <= 'z')) {
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

// stripEscapeSequences removes ANSI escape codes for readable display
func stripEscapeSequences(data string) string {
	var result strings.Builder
	i := 0
	for i < len(data) {
		// Skip CSI sequences (ESC [ ... letter)
		if i < len(data)-1 && data[i] == '\x1b' && data[i+1] == '[' {
			i += 2
			for i < len(data) && !((data[i] >= 'A' && data[i] <= 'Z') || (data[i] >= 'a' && data[i] <= 'z')) {
				i++
			}
			if i < len(data) {
				i++
			}
			continue
		}

		// Skip OSC sequences (ESC ] ... BEL or ST)
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

// syncCommandFrames ensures a command's InputFrames/OutputFrames reflect
// whatever is currently in InputText/OutputText. If the text still matches
// what the original frames reconstruct to, the original frames (with their
// real per-keystroke timing) are left untouched. If the text has been
// edited - i.e. no longer matches - the frames are replaced with a single
// synthetic frame carrying the new text, since the original per-keystroke
// timing no longer corresponds to anything meaningful.
func syncCommandFrames(cmd *Command) {
	if reconstructInput(cmd.InputFrames) != cmd.InputText {
		ts := cmd.StartTime
		if len(cmd.InputFrames) > 0 {
			ts = cmd.InputFrames[0].Timestamp
		}
		cmd.InputFrames = []TerminalFrame{
			{Timestamp: ts, Data: cmd.InputText + "\r\n"},
		}
	}

	if stripEscapeSequences(reconstructOutput(cmd.OutputFrames)) != cmd.OutputText {
		ts := cmd.EndTime
		if len(cmd.OutputFrames) > 0 {
			ts = cmd.OutputFrames[0].Timestamp
		}
		cmd.OutputFrames = []TerminalFrame{
			{Timestamp: ts, Data: strings.ReplaceAll(cmd.OutputText, "\n", "\r\n")},
		}
	}
}

// RebuildRecording takes edited commands and rewrites the JSON file
func RebuildRecording(originalPath string, backupPath string, commands []Command) error {
	input, err := os.ReadFile(originalPath)
	if err != nil {
		return fmt.Errorf("failed to read original: %w", err)
	}
	if err := os.WriteFile(backupPath, input, 0644); err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	var newFrames []TerminalFrame

	for i := range commands {
		cmd := &commands[i]
		syncCommandFrames(cmd)

		if cmd.HasPrompt {
			newFrames = append(newFrames, cmd.PromptFrame)
		}
		newFrames = append(newFrames, cmd.InputFrames...)
		newFrames = append(newFrames, cmd.OutputFrames...)
	}

	file, err := os.Create(originalPath)
	if err != nil {
		return fmt.Errorf("failed to create new recording: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for _, frame := range newFrames {
		if err := encoder.Encode(frame); err != nil {
			return fmt.Errorf("failed to encode frame: %w", err)
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

// FilterForDisplay strips all escape sequences except SGR (colour) codes,
// suitable for feeding into tview's ANSIWriter.
func FilterForDisplay(data string) string {
	var result strings.Builder
	i := 0
	for i < len(data) {
		// CSI sequence: ESC [ ... letter
		if i < len(data)-1 && data[i] == '\x1b' && data[i+1] == '[' {
			start := i
			i += 2
			for i < len(data) && !((data[i] >= 'A' && data[i] <= 'Z') || (data[i] >= 'a' && data[i] <= 'z')) {
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
