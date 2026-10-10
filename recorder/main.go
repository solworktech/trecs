package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"trecs/cloud"

	libtrecs "trecs/lib"
)

// recordExtras are the record options for scripted sessions and shell integration.
type recordExtras struct {
	commandsFile *string
	humanLike    *bool
	cols, rows   *int
	noMarks      *bool
}

func main() {
	recordCmd := flag.NewFlagSet("record", flag.ExitOnError)
	playCmd := flag.NewFlagSet("play", flag.ExitOnError)

	// Record flags
	recordTerminal := recordCmd.Bool("terminal", true, "Enable terminal recording")
	recordAudio := recordCmd.Bool("audio", false, "Enable audio recording")
	recordCamera := recordCmd.Bool("camera", false, "Enable camera recording")
	recordScreen := recordCmd.Bool("screen", false, "Enable screen recording")
	outputDir := recordCmd.String("output", filepath.Join(cmp.Or(os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME")), "trecs", "recordings"), "Output directory")
	sessionName := recordCmd.String("name", "", "Session name (default: timestamp YYYY_MM_DD_HH_MM_SS)")
	terminalCmd := recordCmd.String("cmd", "", "Shell to run (default: the shell this was run from)")
	audioDevice := recordCmd.String("audio-device", "default", "Audio device")
	audioCodec := recordCmd.String("audio-codec", "libmp3lame", "Audio codec")
	cameraDevice := recordCmd.String("camera-device", "/dev/video0", "Camera device")
	cameraSize := recordCmd.String("camera-size", "640x480", "Camera resolution")
	screenDisplay := recordCmd.String("screen-display", ":0", "Screen display (X11 or macOS)")
	screenSize := recordCmd.String("screen-size", "1920x1080", "Screen resolution")
	uploadAfter := recordCmd.Bool("upload", false, "Upload the recording when it ends (log in first with `trecs login`)")
	uploadVisibility := recordCmd.String("visibility", "", "Visibility for -upload: private (default), unlisted or public")
	extras := &recordExtras{
		commandsFile: recordCmd.String("commands-file", "", "Run the commands in this file (one per line; blank lines and lines starting with # are skipped) instead of reading the keyboard. Needs no terminal, so it works in CI"),
		humanLike:    recordCmd.Bool("human-like", false, "With -commands-file: type with the pauses a person makes, between characters and between commands (default: each command is entered at once)"),
		cols:         recordCmd.Int("cols", 0, "Terminal width for -commands-file (default: this terminal's, or 172 when there is none)"),
		rows:         recordCmd.Int("rows", 0, "Terminal height for -commands-file (default: this terminal's, or 38 when there is none)"),
		noMarks:      recordCmd.Bool("no-marks", false, "Don't start the shell with prompt marks (OSC 133); bash, zsh and fish get them by default"),
	}

	// Play flags
	playFile := playCmd.String("file", "", "Terminal recording file to play")
	playSpeed := playCmd.Float64("speed", 1.0, "Playback speed multiplier")

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "record":
		if err := recordCmd.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse record flags: %v\n", err)
			os.Exit(1)
		}
		runRecord(recordTerminal, recordAudio, recordCamera, recordScreen,
			outputDir, sessionName, terminalCmd, audioDevice, audioCodec,
			cameraDevice, cameraSize, screenDisplay, screenSize, uploadAfter, uploadVisibility, extras)

	case "login":
		runLogin(os.Args[2:])

	case "logout":
		runLogout()

	case "upload":
		_, err := cloud.LoadSession()
		if errors.Is(err, cloud.ErrNotLoggedIn) {
			runLogin(make([]string, 0))
		}
		runUpload(os.Args[2:])

	case "play":
		if err := playCmd.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse play flags: %v\n", err)
			os.Exit(1)
		}
		runPlay(playFile, playSpeed)

	case "help":
		printUsage()

	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func runRecord(terminal, audio, camera, screen *bool,
	outputDir, sessionName, terminalCmd, audioDevice, audioCodec,
	cameraDevice, cameraSize, screenDisplay, screenSize *string,
	uploadAfter *bool, uploadVisibility *string, extras *recordExtras) {

	if *extras.humanLike && *extras.commandsFile == "" {
		fmt.Fprintln(os.Stderr, "Error: -human-like only applies with -commands-file")
		os.Exit(1)
	}
	if *extras.commandsFile == "" && (*extras.cols != 0 || *extras.rows != 0) {
		// A person at the keyboard is typing in a real terminal of its own size;
		// recording a different one would not match what they see.
		fmt.Fprintln(os.Stderr, "recorder: -cols and -rows only apply with -commands-file; recording at this terminal's size")
	}

	config := &libtrecs.RecordingConfig{
		TerminalEnabled: *terminal,
		TerminalCommand: *terminalCmd,
		SessionName:     *sessionName,
		AudioEnabled:    *audio,
		AudioDevice:     *audioDevice,
		AudioCodec:      *audioCodec,
		AudioBitrate:    "128k",
		CameraEnabled:   *camera,
		CameraDevice:    *cameraDevice,
		CameraSize:      *cameraSize,
		CameraFramerate: "30",
		ScreenEnabled:   *screen,
		ScreenDisplay:   *screenDisplay,
		ScreenSize:      *screenSize,
		ScreenFramerate: "30",
		OutputDir:       *outputDir,
		ScriptFile:      *extras.commandsFile,
		HumanLike:       *extras.humanLike,
		Cols:            *extras.cols,
		Rows:            *extras.rows,
		NoMarks:         *extras.noMarks,
	}

	// Create and start session recorder
	recorder, err := NewSessionRecorder(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create recorder: %v\n", err)
		os.Exit(1)
	}

	// Print the recording message, then reset cursor to column 0 before entering raw mode
	// This prevents terminal cursor position confusion when the shell starts
	fmt.Fprint(os.Stderr, "Recording in progress. Type 'exit' or Ctrl+D to end the recording.\r\n")

	if err := recorder.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start recording: %v\n", err)
		os.Exit(1)
	}

	// Handle SIGTERM for graceful shutdown (Ctrl+C should go to the shell, not the recorder)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM)

	// Wait for either shell to exit or SIGTERM
	go func() {
		_ = recorder.Wait()        // Ignore error
		sigChan <- syscall.SIGTERM // Signal that recording is done
	}()

	<-sigChan

	// If we got here and recorder is still running, stop it
	if err := recorder.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "Error stopping recorder: %v\n", err)
	}
	if err := recorder.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "Error waiting for recorder: %v\n", err)
	}

	metadata := recorder.GetMetadata()
	fmt.Printf("\nRecording completed successfully!\n")
	fmt.Printf("Session ID: %s\n", metadata.SessionID)
	fmt.Printf("Duration: %v\n", metadata.EndTime.Sub(metadata.StartTime))
	fmt.Printf("Terminal: %s\n", metadata.TerminalFile)
	if metadata.AudioFile != "" {
		fmt.Printf("Audio: %s\n", metadata.AudioFile)
	}
	if metadata.CameraFile != "" {
		fmt.Printf("Camera: %s\n", metadata.CameraFile)
	}
	if metadata.ScreenFile != "" {
		fmt.Printf("Screen: %s\n", metadata.ScreenFile)
	}

	if *uploadAfter && metadata.TerminalFile != "" {
		fmt.Println()
		uploadFiles(cloud.Upload{
			Name: *sessionName, Visibility: *uploadVisibility,
			TerminalPath: metadata.TerminalFile, AudioPath: metadata.AudioFile,
		})
	}
}

func runPlay(playFile *string, playSpeed *float64) {
	if *playFile == "" {
		fmt.Fprintf(os.Stderr, "Error: --file is required for playback\n")
		os.Exit(1)
	}

	player := libtrecs.NewTerminalPlayer()

	if err := player.Play(*playFile); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to play recording: %v\n", err)
		os.Exit(1)
	}

	player.SetSpeed(*playSpeed)

	// Wait for playback to complete
	player.Wait()
	fmt.Println("\nPlayback completed.")
}

func printUsage() {
	fmt.Println(`
Usage:
  trecs record [options]       Record terminal and media streams
  trecs play [options]         Play back terminal recording
  trecs login [options]        Log in to a trecs server (saves the session)
  trecs logout                 Log out and forget the saved session
  trecs upload [options] PATH  Upload a recording (a session directory or terminal.jsonl)
  trecs help                   Show this help message

Record Options:
  -terminal                       Enable terminal recording (default: true)
  -audio                          Enable audio recording (default: false)
  -camera                         Enable camera recording (default: false)
  -screen                         Enable screen recording (default: false)
  -output string                  Output directory (default: "./recordings")
  -name string                    Session name (default: timestamp YYYY_MM_DD_HH_MM_SS)
  -cmd string                     Terminal command to execute (default: current shell, or sh if unable to detect)
  -audio-device string            Audio device (default: "default")
  -audio-codec string             Audio codec (default: "libmp3lame")
  -camera-device string           Camera device (default: "/dev/video0")
  -camera-size string             Camera resolution (default: "640x480")
  -screen-display string          Screen display (default: ":0")
  -screen-size string             Screen resolution (default: "1920x1080")

  -commands-file string           Type the commands in this file instead of reading the keyboard (CI-friendly)
  -human-like                     With -commands-file: type with human pauses (default: instant)
  -cols, -rows int                Terminal size for -commands-file (default: this terminal's, else 172x38)
  -no-marks                       Don't set up shell integration (prompt marks)
  -upload                         Upload the recording when it ends (needs a login)
  -visibility string              Visibility for -upload: private (default), unlisted, public

Login Options:
  -server string                  Server address (default: $TRECS_SERVER, else the last login's)
  -email string                   Account email (asked for if omitted)
  -password-stdin                 Read the password from standard input

Upload Options (before the path):
  -name string                    Recording title
  -description string             Description (default: the commands typed)
  -visibility string              private (default), unlisted or public
  -audio string                   Audio file (default: audio.mp3 beside the recording)

Play Options:
  -file string                    Terminal recording file to play (required)
  -speed float                    Playback speed multiplier (default: 1.0)

Examples:
  # Record terminal only (uses timestamp as session name)
  trecs record

  # Record with custom session name
  trecs record -name my-session

  # Record terminal and audio
  trecs record -audio

  # Record all streams
  trecs record -audio -camera -screen

  # Play back a recording at 2x speed
  trecs play -file recordings/2024_01_15_10_30_45/terminal.jsonl -speed 2.0

During Recording:
  - Use Ctrl+C to interrupt commands in the shell (works normally)
  - Type 'exit' or press Ctrl+D to end the recording
  - The recording will stop and you'll return to your original shell`)
}
