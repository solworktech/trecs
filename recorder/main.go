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
	"time"
	"trecs/cloud"

	libtrecs "trecs/lib"
)

// recordExtras are the record options for scripted sessions and shell integration.
type recordExtras struct {
	commandsFile  *string
	humanLike     *bool
	cols, rows    *int
	noMarks       *bool
	noUpload      *bool
	cmdTimeout    *time.Duration
	allowSecrets  *bool
	overwrite     *bool
	allowRisky    *bool
	allowUnmarked *bool
}

// defineExtras adds the record options for scripted sessions and shell integration to a set of flags (`record` and `livestream` both have them).
func defineExtras(fs *flag.FlagSet) *recordExtras {
	return &recordExtras{
		commandsFile:  fs.String("commands-file", "", "Run the commands in this file (one per line; blank lines and lines starting with # are skipped) instead of reading the keyboard. Needs no terminal, so it works in CI"),
		humanLike:     fs.Bool("human-like", false, "With -commands-file: type with the pauses a person makes, between characters and between commands (default: each command is entered at once)"),
		cols:          fs.Int("cols", 0, "Terminal width for -commands-file (default: this terminal's, or 172 when there is none)"),
		rows:          fs.Int("rows", 0, "Terminal height for -commands-file (default: this terminal's, or 38 when there is none)"),
		cmdTimeout:    fs.Duration("command-timeout", 0, "With -commands-file: give up if one command runs longer than this (default: wait for ever, as at a keyboard). The shell is then hung up on, as when a terminal is closed"),
		noMarks:       fs.Bool("no-marks", false, "Don't start the shell with prompt marks (OSC 133); bash, zsh and fish get them by default"),
		noUpload:      fs.Bool("no-upload", false, "Don't ask whether to upload the recording when it ends"),
		overwrite:     fs.Bool("overwrite", false, "Replace a recording of the same name without asking (by default you are asked, or it is an error where there is nobody to ask)"),
		allowRisky:    fs.Bool("allow-risky", false, "With -commands-file: run the script even if the check of it found something that blocks (a download that is run, a recursive remove of the home directory, a write to a startup file...)"),
		allowUnmarked: fs.Bool("allow-unmarked", false, "Record in a shell that has no prompt marks (not bash, zsh or fish) without asking. With -commands-file, a line can then be typed as the answer to a `read` of the script; interactively, the recording cannot tell the commands apart"),
		allowSecrets:  fs.Bool("allow-secrets", false, "With -upload: upload even if secrets were found in the recording (it is checked first; by default you are asked)"),
	}
}

// defaultOutputDir is where recordings are kept unless -output says otherwise.
func defaultOutputDir() string {
	return filepath.Join(cmp.Or(os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME")), "trecs", "recordings")
}

func main() {
	recordCmd := flag.NewFlagSet("record", flag.ExitOnError)
	playCmd := flag.NewFlagSet("play", flag.ExitOnError)

	// Record flags
	recordTerminal := recordCmd.Bool("terminal", true, "Enable terminal recording")
	recordAudio := recordCmd.Bool("audio", false, "Enable audio recording")
	recordCamera := recordCmd.Bool("camera", false, "Enable camera recording")
	recordScreen := recordCmd.Bool("screen", false, "Enable screen recording")
	outputDir := recordCmd.String("output", defaultOutputDir(), "Output directory")
	sessionName := recordCmd.String("name", "", "Session name, a plain one: no / or .. (default: timestamp YYYY_MM_DD_HH_MM_SS)")
	terminalCmd := recordCmd.String("cmd", "", "Shell to run (default: the shell this was run from)")
	audioDevice := recordCmd.String("audio-device", "default", "Audio device")
	audioCodec := recordCmd.String("audio-codec", "libopus", "Audio codec")
	audioBitrate := recordCmd.String("audio-bitrate", "", "Audio bitrate (default: the codec's, for a voice: 32k for opus)")
	cameraDevice := recordCmd.String("camera-device", "/dev/video0", "Camera device")
	cameraSize := recordCmd.String("camera-size", "640x480", "Camera resolution")
	screenDisplay := recordCmd.String("screen-display", ":0", "Screen display (X11 or macOS)")
	screenSize := recordCmd.String("screen-size", "1920x1080", "Screen resolution")
	uploadAfter := recordCmd.Bool("upload", false, "Upload the recording when it ends (log in first with `"+os.Args[0]+" login`)")
	uploadVisibility := recordCmd.String("visibility", "", "Visibility for -upload: private (default), unlisted or public")
	extras := defineExtras(recordCmd)

	// Play flags
	playFile := playCmd.String("file", "", "Terminal recording file to play")
	playSpeed := playCmd.Float64("speed", 1.0, "Playback speed multiplier")

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version", "-version", "--version":
		printVersion()

	case "record":
		if err := recordCmd.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse record flags: %v\n", err)
			os.Exit(1)
		}
		runRecord(recordTerminal, recordAudio, recordCamera, recordScreen,
			outputDir, sessionName, terminalCmd, audioDevice, audioCodec, audioBitrate,
			cameraDevice, cameraSize, screenDisplay, screenSize, uploadAfter, uploadVisibility, extras, nil)

	case "livestream":
		_, err := cloud.LoadSession()
		if errors.Is(err, cloud.ErrNotLoggedIn) {
			runLogin(make([]string, 0))
		}
		runLivestream(os.Args[2:])

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

	case "update":
		_, err := cloud.LoadSession()
		if errors.Is(err, cloud.ErrNotLoggedIn) {
			runLogin(make([]string, 0))
		}
		runUpdate(os.Args[2:])

	case "scan":
		runScan(os.Args[2:])

	case "shell-init":
		os.Exit(runShellInit(os.Args[2:], os.Stdout, os.Stderr))

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
	outputDir, sessionName, terminalCmd, audioDevice, audioCodec, audioBitrate,
	cameraDevice, cameraSize, screenDisplay, screenSize *string,
	uploadAfter *bool, uploadVisibility *string, extras *recordExtras, live *liveRun) {

	// The echo to standard output is for the person's screen and must never end the session: with nobody asking for SIGPIPE, a
	// write to a pipe whose reader has gone (`trecs record | tee log`, the tee closed) kills the recorder - and the shell with
	// it, and leaves the terminal raw. Asked for, the signal is dropped and the write just fails, which the echo ignores. (A
	// handler, not SIG_IGN: the shell must not start with SIGPIPE ignored.)
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)

	if *extras.humanLike && *extras.commandsFile == "" {
		fmt.Fprintln(os.Stderr, "Error: -human-like only applies with -commands-file")
		os.Exit(1)
	}
	if err := checkSessionName(*sessionName); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if *uploadAfter && *extras.noUpload {
		fmt.Fprintln(os.Stderr, "Error: -upload and -no-upload contradict each other")
		os.Exit(1)
	}
	if *extras.commandsFile == "" && (*extras.cols != 0 || *extras.rows != 0) {
		// A person at the keyboard is typing in a real terminal of its own size;
		// recording a different one would not match what they see.
		fmt.Fprintln(os.Stderr, "recorder: -cols and -rows only apply with -commands-file; recording at this terminal's size")
	}

	warnings := 0
	if *extras.commandsFile != "" {
		// a file that cannot be typed as it is (a keystroke hidden in it) is refused before anything is asked
		if _, err := readScript(*extras.commandsFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error: commands file: %v\n", err)
			os.Exit(1)
		}
		shell := cmp.Or(*terminalCmd, detectShell())
		if !*extras.allowUnmarked && !supportsMarks(shell, *extras.noMarks) {
			fmt.Fprintln(os.Stderr, "Error: "+unmarkedRefusal(shell, *extras.noMarks, markShellsAvailable()))
			os.Exit(1)
		}
		ok, n := auditScript(*extras.commandsFile, shell, *extras.allowRisky, os.Stderr)
		if !ok {
			os.Exit(1)
		}
		warnings = n
	} else if *terminal && !*extras.allowUnmarked && !*extras.noMarks && stdinIsTerminal() {
		// A person at the keyboard, in a shell whose prompts cannot be marked: the recording will not know its commands. Say so first.
		if shell := cmp.Or(*terminalCmd, detectShell()); !supportsMarks(shell, false) {
			use, ok := confirmUnmarked(shell, markShellsAvailable(), os.Stdin, os.Stderr)
			if !ok {
				fmt.Fprintln(os.Stderr, "Aborted; nothing was recorded.")
				os.Exit(1)
			}
			if use != "" { // one of the shells that can be recorded with its commands told apart was chosen
				*terminalCmd = use
			}
		}
	}
	if live != nil {
		if !live.confirm(*extras.commandsFile, warnings) {
			fmt.Fprintln(os.Stderr, "Aborted; nothing was streamed.")
			os.Exit(1)
		}
	} else if *extras.commandsFile != "" && !confirmScript(*extras.commandsFile, os.Stdin, os.Stderr, stdinIsTerminal(), warnings) {
		fmt.Fprintln(os.Stderr, "Aborted; nothing was run.")
		os.Exit(1)
	}

	config := &libtrecs.RecordingConfig{
		TerminalEnabled: *terminal,
		TerminalCommand: *terminalCmd,
		SessionName:     *sessionName,
		AudioEnabled:    *audio,
		AudioDevice:     *audioDevice,
		AudioCodec:      *audioCodec,
		AudioBitrate:    *audioBitrate,
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
		AllowUnmarked:   *extras.allowUnmarked,
		CommandTimeout:  *extras.cmdTimeout,
	}

	// A recording that is there is not written over without a word
	if err := chooseSessionTarget(config, os.Stdin, os.Stderr, stdinIsTerminal(), *extras.overwrite); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if config.SessionName != *sessionName { // another name was chosen
		*sessionName = config.SessionName
	}

	// A live stream is taken now, so that the first record written has somewhere to go (and a stream that is taken by someone else
	// is found out before anything runs).
	if live != nil {
		if err := live.start(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		config.OnRecord = live.sender.Add
	}

	// Create and start session recorder
	recorder, err := NewSessionRecorder(config)
	if err != nil {
		if live != nil {
			live.release()
		}
		fmt.Fprintf(os.Stderr, "Failed to create recorder: %v\n", err)
		os.Exit(1)
	}

	// Print the recording message, then reset cursor to column 0 before entering raw mode
	// This prevents terminal cursor position confusion when the shell starts
	if live != nil {
		fmt.Fprintf(os.Stderr, "Streaming to %s in progress. Type 'exit' or Ctrl+D to end the recording.\r\n", live.info.URL)
	} else {
		fmt.Fprint(os.Stderr, "Recording in progress. Type 'exit' or Ctrl+D to end the recording.\r\n")
	}

	// Every signal that ends a process and can be caught ends the recording properly: the terminal is given back, the files are
	// finished and ffmpeg is told to stop. A recorder that dies of one leaves ffmpeg recording the microphone, the camera or the
	// screen. (A person's Ctrl+C goes to the shell, not here: the terminal is raw.)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)

	if err := recorder.Start(); err != nil {
		if live != nil {
			live.release()
		}
		fmt.Fprintf(os.Stderr, "Failed to start recording: %v\n", err)
		os.Exit(1)
	}

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

	// How it ended: a scripted session that could not go on, or a file that could not be written, fails; a scripted session that
	// went through ends with the status of the shell, as a script would - the last command's.
	exitCode := 0
	if failure := recorder.Failure(); failure != "" {
		exitCode = 1
	} else if *extras.commandsFile != "" {
		if code, ok := recorder.ShellExitCode(); ok {
			exitCode = code
		}
	}

	metadata := recorder.GetMetadata()
	if exitCode != 0 && recorder.Failure() != "" {
		fmt.Printf("\nRecording ended early: %s\n", recorder.Failure())
	} else {
		fmt.Printf("\nRecording completed successfully!\n")
	}
	fmt.Printf("Session ID: %s\n", metadata.SessionID)
	fmt.Printf("Duration: %v\n", metadata.EndTime.Sub(metadata.StartTime))
	fmt.Printf("Terminal: %s\n", metadata.TerminalFile)
	commands, err := libtrecs.ParseRecording(metadata.TerminalFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse recording: %v\n", err)
	}
	if len(commands) == 0 {
		fmt.Fprintln(os.Stderr, "no commands found in recording")
	}
	if err := libtrecs.ExportToMarkdown(metadata.TerminalFile, *sessionName, commands); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		exitCode = cmp.Or(exitCode, 1) // a file of the recording could not be written
	}
	if metadata.AudioFile != "" {
		fmt.Printf("Audio: %s\n", metadata.AudioFile)
	}
	if metadata.CameraFile != "" {
		fmt.Printf("Camera: %s\n", metadata.CameraFile)
	}
	if metadata.ScreenFile != "" {
		fmt.Printf("Screen: %s\n", metadata.ScreenFile)
	}
	fmt.Print(viewHint(metadata.TerminalFile))

	// What was on the screen is checked for secrets, whether it is to be uploaded or not: the file holds them either way.
	var secretHits []secretHit
	var scanErr error
	if metadata.TerminalFile != "" {
		if secretHits, scanErr = scanRecording(metadata.TerminalFile); scanErr != nil {
			fmt.Fprintf(os.Stderr, "Could not check the recording for secrets: %v\n", scanErr)
		} else if len(secretHits) > 0 {
			fmt.Println()
			reportSecrets(os.Stdout, secretHits)
			if live != nil {
				fmt.Println("This is the recording on this machine, which is as it was. What viewers of the stream saw has these masked where the live check recognised them, which is not always.")
			}
		}
	}

	if live != nil {
		// the server has the recording (or, if the stream stopped early, a part of it); nothing is offered to be uploaded
		live.finish(metadata.TerminalFile)
		os.Exit(exitCode)
	}

	if metadata.TerminalFile != "" && !*extras.noUpload {
		// -upload uploads without asking. Otherwise a person at the keyboard is asked; with nobody to ask (CI, a pipe) nothing is uploaded.
		if *uploadAfter || (stdinIsTerminal() && askYes("Upload to the Trecs Platform?", os.Stdin, os.Stdout)) {
			fmt.Println()
			// the hits were shown above: only the decision is left. A recording that could not be checked is not sent.
			if !letSecretsGo(secretHits, *extras.allowSecrets, os.Stdin, os.Stderr, stdinIsTerminal(), true) || scanErr != nil {
				if *uploadAfter {
					exitCode = cmp.Or(exitCode, 1)
				}
				os.Exit(exitCode)
			}
			if _, err := cloud.LoadSession(); errors.Is(err, cloud.ErrNotLoggedIn) && stdinIsTerminal() {
				runLogin(nil)
			}
			uploadFiles(cloud.Upload{
				Name: *sessionName, Visibility: *uploadVisibility,
				TerminalPath: metadata.TerminalFile, AudioPath: metadata.AudioFile,
			}, false)
		} else {
			fmt.Printf("You can always upload it later with:\ntrecs upload -name %s %s\n", *sessionName, metadata.TerminalFile)
		}
	}
	os.Exit(exitCode)
}

func printVersion() {
	fmt.Printf("%s %s\n", filepath.Base(os.Args[0]), libtrecs.VersionString())
	if defaultServer != "" {
		fmt.Printf("default server: %s\n", defaultServer)
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
	fmt.Printf(`
Usage:
  %[1]s record [options]       Record terminal and media streams
  %[1]s play [options]         Play back terminal recording
  %[1]s livestream create [options]
                               Make a live stream: a page to watch it at, and an id to stream to
  %[1]s livestream ID [options]
                               Record a shell as record does, and stream it live to that page (see Livestream Options)
  %[1]s login [options]        Log in to a %[1]s server (saves the session)
  %[1]s logout                 Log out and forget the saved session
  %[1]s upload [options] PATH  Upload a recording (a session directory or terminal.jsonl)
  %[1]s update [options] ID [PATH]
                               Change an uploaded recording: its terminal file, audio, Markdown, name,
                               description or visibility (ID is the one in its link, or the link)
  %[1]s scan PATH              Check a recording for secrets (API keys, tokens, private keys, passwords in URLs...)
  %[1]s shell-init [SHELL]     Print a file that marks the prompts of a shell you did not start here (one on a
                               server after ssh); see its comments, and "shell-init -h"
  %[1]s version                Show the version (also -version, --version)
  %[1]s help                   Show this help message

Record Options:
  -terminal                       Enable terminal recording (default: true)
  -audio                          Enable audio recording (default: false)
  -camera                         Enable camera recording (default: false)
  -screen                         Enable screen recording (default: false)
  -output string                  Output directory (default: "$HOME/.config/trecs/recordings")
  -name string                    Session name, a plain one: no / or .. (default: timestamp YYYY_MM_DD_HH_MM_SS)
  -cmd string                     Terminal command to execute (default: current shell, or sh if unable to detect)
  -audio-device string            Audio device (default: "default")
  -audio-codec string             Audio codec: libopus (default, Ogg), libmp3lame, aac
  -audio-bitrate string           Audio bitrate (default: 32k for libopus - a voice, in 1/4 of the size of 128k MP3)
  -camera-device string           Camera device (default: "/dev/video0")
  -camera-size string             Camera resolution (default: "640x480")
  -screen-display string          Screen display (default: ":0")
  -screen-size string             Screen resolution (default: "1920x1080")

  -commands-file string           Type the commands in this file instead of reading the keyboard (CI-friendly)
  -human-like                     With -commands-file: type with human pauses (default: instant)
  -cols, -rows int                Terminal size for -commands-file (default: this terminal's, else 172x38)
  -command-timeout duration       With -commands-file: give up if a command runs longer (default: never)
  -no-marks                       Don't set up shell integration (prompt marks)
  -upload                         Upload the recording when it ends, without asking (needs a login)
  -no-upload                      Don't ask whether to upload the recording when it ends
  -allow-risky                    With -commands-file: run it although the check of the script blocked it
  -allow-unmarked                 Record in a shell without prompt marks (default: -commands-file refuses it; interactive asks first)
  -allow-secrets                  With -upload: go on even if secrets were found (also for upload and update)
  -overwrite                      Replace a recording of the same name without asking (otherwise you are asked)
  -visibility string              Visibility for the upload: private (default), unlisted, public

  When it ends, a recording made at a terminal asks "Upload to the Trecs Platform? [Y/n]" (Y, y or
  Enter uploads it, any other key doesn't). -upload skips the question and uploads; -no-upload skips
  it and doesn't. With -commands-file, a warning is shown and you are asked to type YES to continue
  (when standard input is a terminal).

Livestream Options:
  livestream create:
    -name string                  The stream's title (default: a timestamp)
    -description string           Its description (Markdown)
    -visibility string            private (default), unlisted or public; the recordings it makes have it too
  livestream ID (the id, or the address of the stream's page, comes first):
    -cmd, -commands-file, -human-like, -cols, -rows, -command-timeout, -no-marks, -allow-risky,
    -allow-unmarked, -output, -name (the recording kept on this machine), -overwrite: as for record
    -audio                        Not available for live streams yet
  You are told how much storage you have and asked to type YES before a stream starts. Secrets are
  masked in what viewers get, as far as they are recognised; the recording on this machine is not
  changed. If the storage runs out or the connection is lost for good, streaming stops and the
  recording goes on: upload it later. Each time a stream ends it becomes a recording of its own.

Login Options:
  -server string                  Server address (default: the last login's, else $TRECS_SERVER, else the one built in, if any)
  -email string                   Account email (asked for if omitted)
  -password-stdin                 Read the password from standard input
  -allow-insecure-endpoint        Accept a plain http:// server that is not on this machine (unencrypted!)

Upload Options (before the path):
  -name string                    Recording title
  -description string             Your own description (Markdown)
  -annotations-as-description     Describe it with its "show in nav menu" annotations, each
                                  linking to its moment (-aad for short)
  -commands-as-description        Describe it with the commands typed (-cad for short; the
                                  default)
  -visibility string              private (default), unlisted or public
  -audio string                   Audio file (default: audio.opus, or .mp3 .m4a .ogg, beside the recording)
  -markdown string                Markdown file the server keeps as the recording's Markdown export,
                                  instead of making one from the recording (default: made from it)

Play Options:
  -file string                    Terminal recording file to play (required)
  -speed float                    Playback speed multiplier (default: 1.0)

Examples:
  # Record terminal only (uses timestamp as session name)
  %[1]s record

  # Record with custom session name
  %[1]s record -name my-session

  # Record terminal and audio
  %[1]s record -audio

  # Record all streams
  %[1]s record -audio -camera -screen

  # Play back a recording at 2x speed
  %[1]s play -file recordings/2024_01_15_10_30_45/terminal.jsonl -speed 2.0

During Recording:
  - Use Ctrl+C to interrupt commands in the shell (works normally)
  - Type 'exit' or press Ctrl+D to end the recording
  - The recording will stop and you'll return to your original shell %s`, os.Args[0], "\n")
}
