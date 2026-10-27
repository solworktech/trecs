package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	libtrecs "trecs/lib"
)

const usage = `Trecs recording editor - edit terminal recordings

Usage: editor <command> [options]

Commands:
  view   <file> [-audio FILE]
                   View and edit recording. Its sound - audio.opus, .mp3, .m4a,
                   .ogg beside it, or the file given - is played along with it
                   (needs ffmpeg), and follows every pause and jump
  export md  <file>
                   Export the recording's commands and outputs as Markdown
                   (written next to it, as <file>.md; -title sets the heading)
  export gif <file>
                   Render the whole recording as an animated GIF (written next
                   to it, as <file>.gif; -o names it). See "export gif -h" for
                   the size, speed, font and colours
  info   <file>    Output recording's commands
  -idle-trim <duration> <file>
                   Cut the recording's long pauses down to the duration (as 3s
                   or 500ms; 2s is what the GIF shows) and write it back: the
                   recording as it was is kept as <file>.backup, its sound is cut
                   to match (needs ffmpeg), and -o names a different file to write
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
	case "export":
		if err := runExport(os.Args[2:]); err != nil {
			fatalf("%v", err)
		}
		return
	case "view", "info":
	default:
		if name := strings.TrimLeft(cmd, "-"); strings.HasPrefix(cmd, "-") && (name == "idle-trim" || strings.HasPrefix(name, "idle-trim=")) {
			if err := runIdleTrim(os.Args[1:]); err != nil {
				fatalf("%v", err)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n%s", cmd, usage)
		os.Exit(1)
	}

	flags := flag.NewFlagSet(cmd, flag.ExitOnError)
	file := flags.String("file", "", "Terminal recording file (or give it as the first argument)")
	audio := flags.String("audio", "", "view: the recording's sound, played along with it by ffmpeg (default: audio.opus - or .mp3, .m4a, .ogg... - beside the recording)")
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

// runExport is `export md <file>` and `export gif <file>`. (`export <file>`, which was only ever Markdown, still works.)
func runExport(args []string) error {
	format := "md"
	if len(args) > 0 && (args[0] == "md" || args[0] == "gif") {
		format, args = args[0], args[1:]
	} else if len(args) == 0 {
		return fmt.Errorf("export needs a format, md or gif: %s export <md|gif> <file>", os.Args[0])
	}
	flags := flag.NewFlagSet("export "+format, flag.ContinueOnError)
	file := flags.String("file", "", "Terminal recording file (or give it as the first argument)")
	var title, out, fontPath, fg, bg *string
	var fontSize, speed *float64
	var fps, loop, cols, rows *int
	var idle, hold *time.Duration
	var noCursor *bool
	if format == "md" {
		title = flags.String("title", "", "the document's heading (default: \"Terminal recording\")")
	} else {
		out = flags.String("o", "", "the GIF to write (default: the recording's name with .gif added)")
		fontPath = flags.String("font", "", "a monospaced TrueType or OpenType font to draw the text with (default: Go Mono, which is built in)")
		fontSize = flags.Float64("font-size", 16, "the size of the text in pixels: the picture is as large as the terminal, in cells of that size")
		fps = flags.Int("fps", 10, "most pictures a second, 1 to 50: changes closer together than that are shown together")
		speed = flags.Float64("speed", 1, "play the recording this many times faster")
		idle = flags.Duration("idle", 2*time.Second, "longest pause to show, as 2s or 500ms: longer pauses are cut down to it (0: keep them all)")
		hold = flags.Duration("hold", 3*time.Second, "how long the last picture stays before the GIF starts again (0: no wait)")
		loop = flags.Int("loop", 0, "how many times it repeats after the first, 0 being for ever; -1 plays it once")
		cols = flags.Int("cols", 0, "the width of the terminal in characters, if the recording does not say")
		rows = flags.Int("rows", 0, "the height of the terminal in characters, if the recording does not say")
		fg = flags.String("fg", "", "the colour of text the recording does not colour, as #rrggbb (default #c9d1d9)")
		bg = flags.String("bg", "", "the colour of the screen, as #rrggbb (default #0d1117)")
		noCursor = flags.Bool("no-cursor", false, "do not draw the cursor")
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") { // `export gif recording.jsonl`
		*file, args = args[0], args[1:]
	}
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(), "Usage: %s export %s <file> [options]\n\nOptions:\n", os.Args[0], format)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) { // -h: the options have been listed
			return nil
		}
		return fmt.Errorf("failed to parse flags: %w", err)
	}
	if *file == "" && flags.NArg() > 0 {
		*file = flags.Arg(0)
	}
	if *file == "" {
		return fmt.Errorf("a recording file is required: %s export %s <file>", os.Args[0], format)
	}
	if _, err := os.Stat(*file); err != nil {
		return fmt.Errorf("cannot access recording file: %w", err)
	}
	if format == "gif" {
		o := libtrecs.GIFOptions{FontPath: *fontPath, FontSize: *fontSize, FPS: *fps, Speed: *speed, IdleLimit: *idle, Hold: *hold, Loop: *loop,
			Cols: *cols, Rows: *rows, Foreground: *fg, Background: *bg, NoCursor: *noCursor}
		if o.IdleLimit <= 0 { // 0 asks for no limit, which the library calls a negative one: its zero is "the default"
			o.IdleLimit = -1
		}
		if o.Hold <= 0 {
			o.Hold = -1
		}
		path := *out
		if path == "" {
			path = *file + ".gif"
		}
		desc, err := libtrecs.ExportToGIF(*file, path, o)
		if err != nil {
			return err
		}
		fmt.Printf("Successfully exported as %s\n", desc)
		return nil
	}
	commands, err := libtrecs.ParseRecording(*file)
	if err != nil {
		return fmt.Errorf("failed to parse recording: %w", err)
	}
	if len(commands) == 0 {
		return fmt.Errorf("no commands found in recording")
	}
	return libtrecs.ExportToMarkdown(*file, *title, commands)
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

// runIdleTrim is `-idle-trim 3s <file>`: the pauses of the recording longer than that are cut down to it, in the file (the recording as
// it was is kept beside it as <file>.backup), and in its sound. The flags and the file may come in any order.
func runIdleTrim(args []string) error {
	flags := flag.NewFlagSet("idle-trim", flag.ContinueOnError)
	limit := flags.Duration("idle-trim", libtrecs.DefaultIdleTrim, "longest pause to keep, as 2s or 500ms: longer pauses are cut down to it")
	out := flags.String("o", "", "write the trimmed recording to this file, and leave the recording as it is (not for one that has sound)")
	audioFlag := flags.String("audio", "", "the recording's sound, cut to match (default: audio.opus - or .mp3, .m4a, .ogg... - beside the recording)")
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
	if len(files) != 1 {
		return fmt.Errorf("one recording is required: %s -idle-trim 3s <file>", os.Args[0])
	}
	file := files[0]
	if err := libtrecs.CheckIdleTrim(*limit); err != nil {
		return err
	}
	if fi, err := os.Stat(file); err != nil || fi.IsDir() {
		return fmt.Errorf("cannot access recording file: %s", file)
	}
	sound := *audioFlag
	if sound == "" {
		sound = recordingAudio(file)
	} else if fi, err := os.Stat(sound); err != nil || fi.IsDir() {
		return fmt.Errorf("cannot read the audio file: %s", sound)
	}
	target, backup := file, file+".backup"
	if *out != "" {
		if sound != "" {
			return fmt.Errorf("the recording has sound (%s), which goes with it where it is: trim it in place, or move the recording and its sound together first", sound)
		}
		if err := libtrecs.CopyFile(file, *out); err != nil {
			return fmt.Errorf("the copy to %s could not be made: %w", *out, err)
		}
		// the recording as it is stays where it is, so the copy needs no backup of its own
		tmp, err := os.MkdirTemp("", "trecs-idle-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		target, backup = *out, filepath.Join(tmp, "before")
	}
	res, err := libtrecs.TrimIdle(target, backup, sound, *limit)
	if err != nil {
		return err
	}
	if res.Pauses == 0 {
		fmt.Printf("No pause is longer than %s: %s is as it was.\n", *limit, target)
		return nil
	}
	fmt.Printf("Cut %s from %d long pauses: %s is now %s (it was %s).\n",
		res.Removed.Round(time.Millisecond), res.Pauses, target, res.After.Round(time.Millisecond), res.Before.Round(time.Millisecond))
	if *out == "" {
		fmt.Printf("The recording as it was: %s\n", res.BackupPath)
	}
	if res.AudioBackup != "" {
		fmt.Printf("The sound was cut to match; as it was: %s\n", res.AudioBackup)
	}
	return nil
}
