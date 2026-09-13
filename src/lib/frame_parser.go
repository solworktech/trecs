package lib

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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
	// Look for common shell prompts
	lines := strings.Split(firstFrameData, "\n")
	if len(lines) > 0 {
		lastLine := lines[len(lines)-1]
		// Find the last occurrence of common prompt endings
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

	for i, frame := range frames {
		if currentCommand == nil {
			currentCommand = &Command{
				InputFrames:  []TerminalFrame{},
				OutputFrames: []TerminalFrame{},
				StartTime:    frame.Timestamp,
			}
		}

		// Check if this frame ends input (contains newline/return)
		if inputPhase && containsReturnOrNewline(frame.Data) {
			inputPhase = false
			currentCommand.InputFrames = append(currentCommand.InputFrames, frame)
			currentCommand.InputText = reconstructInput(currentCommand.InputFrames)
			continue
		}

		// Check if we've reached the next prompt (start of new command)
		if !inputPhase && isPromptLine(frame.Data, prompt) {
			// Save current command and start new one
			if currentCommand != nil && len(currentCommand.InputFrames) > 0 {
				currentCommand.OutputText = stripEscapeSequences(reconstructOutput(currentCommand.OutputFrames))
				currentCommand.OutputTextRaw = reconstructOutput(currentCommand.OutputFrames)
				if i > 0 {
					currentCommand.EndTime = frames[i-1].Timestamp
				}
				commands = append(commands, *currentCommand)
			}
			currentCommand = &Command{
				InputFrames:  []TerminalFrame{},
				OutputFrames: []TerminalFrame{},
				StartTime:    frame.Timestamp,
			}
			inputPhase = true
			continue
		}

		// Accumulate frames
		if inputPhase {
			currentCommand.InputFrames = append(currentCommand.InputFrames, frame)
		} else {
			currentCommand.OutputFrames = append(currentCommand.OutputFrames, frame)
		}
	}

	// Don't forget the last command
	if currentCommand != nil && len(currentCommand.InputFrames) > 0 {
		currentCommand.InputText = reconstructInput(currentCommand.InputFrames)
		currentCommand.OutputText = stripEscapeSequences(reconstructOutput(currentCommand.OutputFrames))
		currentCommand.OutputTextRaw = reconstructOutput(currentCommand.OutputFrames)
		if len(frames) > 0 {
			currentCommand.EndTime = frames[len(frames)-1].Timestamp
		}
		commands = append(commands, *currentCommand)
	}

	return commands, nil
}

func containsReturnOrNewline(data string) bool {
	return strings.Contains(data, "\r") || strings.Contains(data, "\n")
}

func isPromptLine(data string, prompt string) bool {
	if prompt == "" {
		return false
	}
	return strings.Contains(data, prompt)
}

// reconstructInput builds the actual command typed by handling backspaces
func reconstructInput(frames []TerminalFrame) string {
	var result []rune

	for _, frame := range frames {
		for _, ch := range frame.Data {
			// Handle backspace
			if ch == '\b' || ch == 0x7F { // backspace or DEL
				if len(result) > 0 {
					result = result[:len(result)-1]
				}
				continue
			}

			// Skip control characters except newline/return
			if ch < 32 && ch != '\n' && ch != '\r' {
				continue
			}

			// Skip escape sequences
			if ch == '\x1b' { // ESC
				continue
			}

			result = append(result, rune(ch))
		}
	}

	// Clean up result: remove trailing newline/return
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
	// Remove ANSI escape sequences
	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\[[0-9;]*m`)
	data = re.ReplaceAllString(data, "")

	// Remove other control sequences
	re = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	data = re.ReplaceAllString(data, "")

	// Clean up excessive whitespace while preserving structure
	lines := strings.Split(data, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line != "" || len(cleaned) > 0 {
			cleaned = append(cleaned, line)
		}
	}

	return strings.Join(cleaned, "\n")
}

// RebuildRecording takes edited commands and rewrites the JSON file
func RebuildRecording(originalPath string, backupPath string, commands []Command) error {
	// Backup original
	input, err := os.ReadFile(originalPath)
	if err != nil {
		return fmt.Errorf("failed to read original: %w", err)
	}
	if err := os.WriteFile(backupPath, input, 0644); err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	// Rebuild frames from commands
	var newFrames []TerminalFrame
	var timeOffset int64

	for _, cmd := range commands {
		// Add input frames
		for _, frame := range cmd.InputFrames {
			frame.Timestamp = frame.Timestamp - timeOffset
			newFrames = append(newFrames, frame)
		}

		// Add output frames
		for _, frame := range cmd.OutputFrames {
			frame.Timestamp = frame.Timestamp - timeOffset
			newFrames = append(newFrames, frame)
		}
	}

	// Write new recording file
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
