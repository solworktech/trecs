package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	libtrecs "trecs/lib"
)

func main() {
	recordCmd := flag.NewFlagSet("record", flag.ExitOnError)
	playCmd := flag.NewFlagSet("play", flag.ExitOnError)

	// Record flags
	recordTerminal := recordCmd.Bool("terminal", true, "Enable terminal recording")
	recordAudio := recordCmd.Bool("audio", false, "Enable audio recording")
	recordCamera := recordCmd.Bool("camera", false, "Enable camera recording")
	recordScreen := recordCmd.Bool("screen", false, "Enable screen recording")
	outputDir := recordCmd.String("output", "./recordings", "Output directory")
	sessionName := recordCmd.String("name", "", "Session name (default: timestamp YYYY_MM_DD_HH_MM_SS)")
	terminalCmd := recordCmd.String("cmd", "bash", "Terminal command to execute")
	audioDevice := recordCmd.String("audio-device", "default", "Audio device")
	audioCodec := recordCmd.String("audio-codec", "libmp3lame", "Audio codec")
	cameraDevice := recordCmd.String("camera-device", "/dev/video0", "Camera device")
	cameraSize := recordCmd.String("camera-size", "640x480", "Camera resolution")
	screenDisplay := recordCmd.String("screen-display", ":0", "Screen display (X11 or macOS)")
	screenSize := recordCmd.String("screen-size", "1920x1080", "Screen resolution")

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
			cameraDevice, cameraSize, screenDisplay, screenSize)

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
	cameraDevice, cameraSize, screenDisplay, screenSize *string) {

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
  recorder record [options]       Record terminal and media streams
  recorder play [options]         Play back terminal recording
  recorder help                   Show this help message

Record Options:
  -terminal                       Enable terminal recording (default: true)
  -audio                          Enable audio recording (default: false)
  -camera                         Enable camera recording (default: false)
  -screen                         Enable screen recording (default: false)
  -output string                  Output directory (default: "./recordings")
  -name string                    Session name (default: timestamp YYYY_MM_DD_HH_MM_SS)
  -cmd string                     Terminal command to execute (default: "bash")
  -audio-device string            Audio device (default: "default")
  -audio-codec string             Audio codec (default: "libmp3lame")
  -camera-device string           Camera device (default: "/dev/video0")
  -camera-size string             Camera resolution (default: "640x480")
  -screen-display string          Screen display (default: ":0")
  -screen-size string             Screen resolution (default: "1920x1080")

Play Options:
  -file string                    Terminal recording file to play (required)
  -speed float                    Playback speed multiplier (default: 1.0)

Examples:
  # Record terminal only (uses timestamp as session name)
  recorder record

  # Record with custom session name
  recorder record -name my-session

  # Record terminal and audio
  recorder record -audio

  # Record all streams
  recorder record -audio -camera -screen

  # Play back a recording at 2x speed
  recorder play -file recordings/2024_01_15_10_30_45/terminal.jsonl -speed 2.0

During Recording:
  - Use Ctrl+C to interrupt commands in the shell (works normally)
  - Type 'exit' or press Ctrl+D to end the recording
  - The recording will stop and you'll return to your original shell
`)
}
