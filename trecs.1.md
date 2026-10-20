trecs 1 "Oct 18 2026" trecs "User Manual"
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
  trecs update [options] ID [PATH]
                               Change an uploaded recording: its terminal file, audio, Markdown, name,
                               description or visibility (ID is the one in its link, or the link)
  trecs version                Show the version (also -version, --version)
  trecs help                   Show this help message

Record Options:
  -terminal                       Enable terminal recording (default: true)
  -audio                          Enable audio recording (default: false)
  -camera                         Enable camera recording (default: false)
  -screen                         Enable screen recording (default: false)
  -output string                  Output directory (default: "$HOME/.config/trecs/recordings")
  -name string                    Session name (default: timestamp YYYY_MM_DD_HH_MM_SS)
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
  -no-marks                       Don't set up shell integration (prompt marks)
  -upload                         Upload the recording when it ends, without asking (needs a login)
  -no-upload                      Don't ask whether to upload the recording when it ends
  -visibility string              Visibility for the upload: private (default), unlisted, public

Login Options:
  -server string                  Server address (default: the last login's, else $TRECS_SERVER,
                                  else the one built in, if any)
  -email string                   Account email (asked for if omitted)
  -password-stdin                 Read the password from standard input

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

Scripted Recordings (-commands-file):
  - The commands are typed into a real shell, with your permissions, as if they were a shell
    script: nothing reviews them. A warning is shown (in yellow) and you must type YES to
    continue; any other input exits without running anything.
  - When standard input is not a terminal (CI, a pipe) the warning is shown and the recording
    goes ahead.

After Recording:
  - At a terminal you are asked "Upload to the Trecs Platform? [Y/n]". Y, y or Enter uploads the
    recording (private, unless -visibility is given); any other key does not.
  - -upload uploads without asking; -no-upload neither asks nor uploads.
  - When standard input is not a terminal nothing is uploaded unless -upload is given.

Build Options (go build -ldflags "-X ..."):
  trecs/lib.Version=TAG           The version shown by --version (default: the commit built from)
  main.defaultServer=URL          The server for login when there is no -server, no saved login and no $TRECS_SERVER

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

Replace the narration of an uploaded recording, and rename it (the id is the one in its link):

$ trecs update -name "Better title" -audio narration.mp3 0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d

Play back a recording at 2x speed:

$ trecs play -file /path/to/terminal.jsonl -speed 2.0
