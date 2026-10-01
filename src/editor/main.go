package main

import (
	"flag"
	"fmt"
	"os"

	libtrecs "trecs/lib"
	//"github.com/mohae/struct2csv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, `Usage: editor <command> [options]

Commands:
  view     <file>    View and edit recording
  export   <file>    Export recordings's commands and outputs as MarkDown
  help               Show this help
`)
		os.Exit(1)
	}

	cmd := os.Args[1]
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

	// Verify file exists
	if _, err := os.Stat(*file); err != nil {
		_ = fmt.Errorf("cannot access recording file: %w", err)
		return
	}

	// Parse recording
	commands, err := libtrecs.ParseRecording(*file)
	if err != nil {
		_ = fmt.Errorf("failed to parse recording: %w", err)
		return
	}

	if len(commands) == 0 {
		_ = fmt.Errorf("no commands found in recording")
		return
	}

	switch cmd {
	case "view":

		if err := runEditor(*file, commands); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "export":
		if err := exportToMarkdown(*file, commands); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "help":
		fmt.Fprintf(os.Stderr, `recorder editor - edit terminal recordings

Usage: editor <command> [options]

Commands:
  view     <file>    View and edit recording commands
  export   <file>    Export recordings's commands and outputs as MarkDown
  help               Show this help

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

func exportToMarkdown(filePath string, commands []libtrecs.Command) error {
	md_path := filePath + ".md"
	md_file, e := os.Create(md_path)
	if e != nil {
		panic(e)
	}
	defer func() { _ = md_file.Close() }()
	for _, cmd := range commands {
		_, _ = fmt.Fprintf(md_file, "### `%s`\n", cmd.InputText)
		_, _ = fmt.Fprintf(md_file, "\n%s\n\n", cmd.Annotation.Text)
		_, _ = fmt.Fprintf(md_file, "```sh\n%s```\n", cmd.OutputText)
	}
	fmt.Printf("Successfully saved as %s\n", md_path)
	return nil

}

// runEditor opens the recording in editor mode
func runEditor(filePath string, commands []libtrecs.Command) error {

	// Create player (don't start playback here - editor.Run() does that)
	player := libtrecs.NewTerminalPlayer()

	// Launch editor
	editor := NewEditorMode(player, commands, filePath)
	if err := editor.Run(); err != nil {
		return fmt.Errorf("editor error: %w", err)
	}

	return nil
}
