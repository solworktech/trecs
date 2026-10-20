package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	libtrecs "trecs/lib"
)

const usage = `Trecs recording editor - edit terminal recordings

Usage: editor <command> [options]

Commands:
  view   <file> [-audio FILE]
                   View and edit recording. Its sound - audio.opus, .mp3, .m4a,
                   .ogg beside it, or the file given - is played along with it
                   (needs ffmpeg), and follows every pause and jump
  export <file>    Export the recording's commands and outputs as Markdown
                   (written next to it, as <file>.md; -title sets the heading)
  info   <file>    Output recording's commands
  merge  -o <out> <file> <file> [<file>...]
                   Join recordings into one, in the order given: each starts
                   where the one before it ended (-gap ms of pause, default 1000,
                   then the screen is cleared). Annotations go with their frames.
  version          Show the version (also -version, --version)
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
	case "version", "-version", "--version":
		fmt.Printf("%s %s\n", filepath.Base(os.Args[0]), libtrecs.VersionString())
		return
	case "merge":
		if err := runMerge(os.Args[2:]); err != nil {
			fatalf("%v", err)
		}
		return
	case "view", "export", "info":
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n%s", cmd, usage)
		os.Exit(1)
	}

	flags := flag.NewFlagSet(cmd, flag.ExitOnError)
	file := flags.String("file", "", "Terminal recording file (or give it as the first argument)")
	audio := flags.String("audio", "", "view: the recording's sound, played along with it by ffmpeg (default: audio.opus - or .mp3, .m4a, .ogg... - beside the recording)")
	title := flags.String("title", "", "export: the document's heading (default: \"Terminal recording\")")
	args := os.Args[2:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") { // `editor view recording.jsonl`, as the usage says
		*file, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		fatalf("failed to parse flags: %v", err)
	}
	if *file == "" {
		fatalf("a recording file is required: %s %s <file>", os.Args[0], cmd)
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
		sound := *audio
		if sound == "" {
			sound = recordingAudio(*file)
		} else if fi, err := os.Stat(sound); err != nil || fi.IsDir() {
			fatalf("cannot read the audio file: %s", sound)
		}
		if err := runEditor(*file, commands, sound); err != nil {
			fatalf("%v", err)
		}
	case "export":
		if err := libtrecs.ExportToMarkdown(*file, *title, commands); err != nil {
			fatalf("%v", err)
		}
	case "info":
		if err := showInfo(commands); err != nil {
			fatalf("%v", err)
		}
	}
}

func showInfo(commands []libtrecs.Command) error {

	for _, c := range commands {
		if strings.TrimSpace(c.InputText) == "" {
			continue
		}
		fmt.Println(libtrecs.CollapseCommand(c.InputText))
	}
	return nil
}

// runEditor opens the recording in editor mode
func runEditor(filePath string, commands []libtrecs.Command, audio string) error {
	// Create player (don't start playback here - editor.Run() does that)
	player := libtrecs.NewTerminalPlayer()
	// Launch editor
	editor := NewEditorMode(player, commands, filePath)
	editor.SetAudio(audio)
	if err := editor.Run(); err != nil {
		return fmt.Errorf("editor error: %w", err)
	}

	return nil
}

// runMerge is `merge -o out.jsonl a.jsonl b.jsonl ...`: flags and files may come in any order.
func runMerge(args []string) error {
	flags := flag.NewFlagSet("merge", flag.ContinueOnError)
	out := flags.String("o", "", "the merged recording to write (required)")
	gap := flags.Int64("gap", libtrecs.DefaultMergeGapMs, "milliseconds between the end of one recording and the start of the next")
	var files []string
	for len(args) > 0 {
		if err := flags.Parse(args); err != nil {
			return fmt.Errorf("failed to parse flags: %w", err)
		}
		args = flags.Args()
		if len(args) > 0 {
			files, args = append(files, args[0]), args[1:]
		}
	}
	if *out == "" {
		return fmt.Errorf("where to write the merged recording is required: %s merge -o <out> <list of input files>", os.Args[0])
	}
	if len(files) < 2 {
		return fmt.Errorf("two recordings or more are needed: %s merge -o <out> <list of input files>", os.Args[0])
	}
	warn := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "Warning: "+format+"\n", a...) }
	if err := libtrecs.MergeRecordings(files, *out, *gap, warn); err != nil {
		return err
	}
	fmt.Printf("Merged %d recordings into %s\n", len(files), *out)
	return nil
}
