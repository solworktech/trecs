trecs-edit 1 "Oct 9 2026" trecs-edit "User Manual"
======================================================

## DESCRIPTION

Trecs recording editor - edit terminal recordings

The editor allows you to:
- Watch recording playback
- Pause at any point
- Edit command input and output
- Delete commands
- Save edited recordings with automatic timestamp compression

Original recordings are backed up before editing.

## Synopsis

```sh
Usage: editor <command> [options]

Commands:
  view   <file>    View and edit recording
  export <file>    Export the recording's commands and outputs as Markdown
                   (written next to it, as <file>.md; -title sets the heading)
  help             Show this help
```

## Examples

Edit recording:

$ trecs-edit view -file /path/to/terminal.jsonl 

Export commands and output as Markdown:

$ trecs-edit export -file /path/to/terminal.jsonl
