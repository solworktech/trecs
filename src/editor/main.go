package main

import (
	"flag"
	"fmt"
	"os"

	libtrecs "trecs/lib"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, `Usage: editor <command> [options]

Commands:
  view   <file>    View and edit recording
  help             Show this help
`)
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "view":
		viewCmd := flag.NewFlagSet("view", flag.ExitOnError)
		file := viewCmd.String("file", "", "Terminal recording file to view/edit (required)")
		if err := viewCmd.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse flags: %v\n", err)
			os.Exit(1)
		}

		if *file == "" {
			fmt.Fprintf(os.Stderr, "Error: -file is required\n")
			os.Exit(1)
		}

		if err := runEditor(*file); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "help":
		fmt.Fprintf(os.Stderr, `recorder editor - edit terminal recordings

Usage: editor <command> [options]

Commands:
  view   <file>    View and edit recording commands
  help             Show this help

The editor allows you to:
- Watch recording playback
- Pause at any point
- Edit command input and output
- Delete commands
- Save edited recordings with automatic timestamp compression

Original recordings are backed up before editing.
`)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		os.Exit(1)
	}
}

// runEditor opens the recording in editor mode
func runEditor(filePath string) error {
	// Verify file exists
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("cannot access recording file: %w", err)
	}

	// Parse recording
	commands, err := libtrecs.ParseRecording(filePath)
	if err != nil {
		return fmt.Errorf("failed to parse recording: %w", err)
	}

	if len(commands) == 0 {
		return fmt.Errorf("no commands found in recording")
	}

	// Create player (don't start playback here - editor.Run() does that)
	player := libtrecs.NewTerminalPlayer()

	// Launch editor
	editor := NewEditorMode(player, commands, filePath)
	if err := editor.Run(); err != nil {
		return fmt.Errorf("editor error: %w", err)
	}

	return nil
}
