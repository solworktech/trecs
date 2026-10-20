trecs-edit 1 "Oct 18 2026" trecs-edit "User Manual"
======================================================

## DESCRIPTION

Trecs recording editor - edit terminal recordings

The editor allows you to:
- Watch recording playback
- Pause at any point
- Edit command input and output
- Delete commands
- Show an image over the terminal at a moment of the recording (Ctrl+G)
- Save edited recordings with automatic timestamp compression

Original recordings are backed up before editing.

## Synopsis

```sh
Trecs recording editor - edit terminal recordings

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
```

## Examples

Edit recording:

$ trecs-edit view -file /path/to/terminal.jsonl 

Play the recording with its narration:

$ trecs-edit view /path/to/terminal.jsonl -audio /path/to/narration.mp3

Export commands and output as Markdown:

$ trecs-edit export -file /path/to/terminal.jsonl
