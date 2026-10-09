trecs 1 "Oct 9 2026" trecs "User Manual"
========================================

## DESCRIPTION

A tool for recording terminal sessions with precise timestamp control.

## Features

- Sharp at any size: 
A video is a grid of pixels: soft on a phone, blurry at full screen, 
unreadable once someone zooms in. A recording is text, drawn fresh at
whatever size the viewer has.
- Kilobytes, not megabytes: 
The same session as a screen video runs to hundreds of megabytes. 
A recording loads at once, plays on a poor connection and costs
next to nothing to host.
- The real thing, not a polished excerpt: 
Docs show the snippet someone chose to paste, and drift out of date. 
A recording shows the actual output, the actual error and the actual fix,
right next to your written steps.
- Upload to the Trecs platform playback in web context (via browser)

## Synopsis

```sh
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
  -output string                  Output directory (default: "./recordings")
  -name string                    Session name (default: timestamp YYYY_MM_DD_HH_MM_SS)
  -cmd string                     Terminal command to execute (default: current shell, or sh if unable to detect)
  -audio-device string            Audio device (default: "default")
  -audio-codec string             Audio codec (default: "libmp3lame")

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

During Recording:
  - Use Ctrl+C to interrupt commands in the shell (works normally)
  - Type 'exit' or press Ctrl+D to end the recording
  - The recording will stop and you'll return to your original shell
```

## Examples

Record terminal only (uses timestamp as session name):

$ trecs record

Record with custom session name:

$ trecs record -name my-session

Record terminal and audio:

$ trecs record -audio

Play back a recording at 2x speed:

$ trecs play -file /path/to/terminal.jsonl -speed 2.0
