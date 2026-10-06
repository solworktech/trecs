package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	libtrecs "trecs/lib"
)

const usage = `recorder editor - edit terminal recordings

Usage: editor <command> [options]

Commands:
  view   <file>    View and edit recording
  export <file>    Export the recording's commands and outputs as Markdown
                   (written next to it, as <file>.md; -title sets the heading)
  help             Show this help

The editor allows you to:
- Watch recording playback
- Pause at any point
- Edit command input and output
- Delete commands
- Save edited recordings with automatic timestamp compression

Original recordings are backed up before editing.
`

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	cmd := os.Args[1]
	switch cmd {
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
		return
	case "view", "export":
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n%s", cmd, usage)
		os.Exit(1)
	}

	flags := flag.NewFlagSet(cmd, flag.ExitOnError)
	file := flags.String("file", "", "Terminal recording file (or give it as the first argument)")
	title := flags.String("title", "", "export: the document's heading (default: \"Terminal recording\")")
	args := os.Args[2:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") { // `editor view recording.jsonl`, as the usage says
		*file, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		fatalf("failed to parse flags: %v", err)
	}
	if *file == "" {
		fatalf("a recording file is required: editor %s <file>", cmd)
	}
	if _, err := os.Stat(*file); err != nil {
		fatalf("cannot access recording file: %v", err)
	}

	commands, err := libtrecs.ParseRecording(*file)
	if err != nil {
		fatalf("failed to parse recording: %v", err)
	}
	if len(commands) == 0 {
		fatalf("no commands found in recording")
	}

	switch cmd {
	case "view":
		if err := runEditor(*file, commands); err != nil {
			fatalf("%v", err)
		}
	case "export":
		if err := exportToMarkdown(*file, *title, commands); err != nil {
			fatalf("%v", err)
		}
	}
}

// exportToMarkdown writes the commands and their outputs next to the recording,
// as <file>.md. See libtrecs.MarkdownFromCommands for the format.
func exportToMarkdown(filePath, title string, commands []libtrecs.Command) error {
	mdPath := filePath + ".md"
	if err := os.WriteFile(mdPath, []byte(libtrecs.MarkdownFromCommands(title, commands)), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", mdPath, err)
	}
	fmt.Printf("Successfully saved as %s\n", mdPath)
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
