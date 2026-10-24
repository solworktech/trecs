# Trecs

A screen video of a terminal is a blurry, unsearchable blob. A Trecs recording is the session itself: sharp at any size, a few kilobytes instead of hundreds of megabytes, and every command is a jump point — so viewers go straight to the step they need instead of scrubbing a timeline.

- **Copy, don't retype.** Select and copy commands straight out of the player.
- **Annotate the moments that matter.** Notes attach to commands and pause playback where you want attention.
- **Narrate if you like.** Optional audio stays in sync as you jump between commands.
- **Better than docs alone.** Show the real output, the real errors, the real fix — embedded right next to your written instructions.

This repository holds the two command-line tools:

| Tool | What it does |
|------|--------------|
| `trecs` | The recorder. Records a terminal session, plays it back, and uploads it to a Trecs platform. |
| `trecs-edit` | The editor. Trims and fixes a recording, adds annotations, merges recordings, exports Markdown and animated GIFs. |

Recordings are played in the browser by the Trecs web player (see [The player](#the-player)).


[![Trecs](https://trecs.run/v1/recordings/5bb6689c-164b-4ca6-9057-087560147134/social.png)](https://trecs.run/watch/5bb6689c-164b-4ca6-9057-087560147134)

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [The recorder: `trecs`](#the-recorder-trecs)
- [The editor: `trecs-edit`](#the-editor-trecs-edit)
- [The player](#the-player)
- [Free software: the AGPLv3](#free-software-the-agplv3)
- [Enterprise licences](#enterprise-licences)
- [Contributing](#contributing)

## Install

Download a package or archive for your system from the
[releases page](https://github.com/solworktech/trecs/releases): `.deb`, `.rpm` and Arch packages, or a `.tar.gz` holding both binaries. Linux and macOS are the supported platforms; FreeBSD and NetBSD builds are published too.

To build from source you need Go 1.24 or later:

```sh
go build -o trecs ./recorder
go build -o trecs-edit ./editor
```

Both binaries print their version with `--version`. A release reports its tag; a build from a clone reports the commit it was built from (with `-dirty` if there were uncommitted changes).

Two options are set when building, with `-ldflags`:

```sh
# the version to report, instead of the commit
go build -ldflags "-X trecs/lib.Version=v1.2.3" -o trecs ./recorder
# the server `trecs login` uses when there is no -server, no saved login and no $TRECS_SERVER
go build -ldflags "-X main.defaultServer=https://trecs.example.com" -o trecs ./recorder
```

Recording audio (narration) needs [`ffmpeg`](https://ffmpeg.org/) on your `PATH`. Nothing else is needed.

## Quick start

```sh
trecs record                       # work as usual; type `exit` or press Ctrl+D to finish
trecs-edit view   <file> [-audio FILE] # View and edit a recording, with its sound
trecs upload <recording>           # get a link to share (asks you to log in first)
```

## The recorder: `trecs`

```
trecs record [options]       Record a terminal session
trecs play [options]         Play a recording back in the terminal
trecs login [options]        Log in to a Trecs server (saves the session)
trecs logout                 Log out and forget the saved session
trecs upload [options] PATH  Upload a recording
trecs update [options] ID [PATH]
                             Change a recording you uploaded
trecs version                Show the version (also --version)
trecs help                   Show the help
```

### Recording

`trecs record` starts your shell inside a recorder and captures everything that happens in it, with millisecond timing: output, colours, cursor movement, and full-screen programs such as `vim` or `htop`. Work as you normally would. Ctrl+C goes to the program you are running, not the recorder; to end the recording, type `exit` or press Ctrl+D.

```sh
trecs record                        # name is the date and time
trecs record -name nginx-setup      # a name of your own
trecs record -cmd zsh               # record a particular shell
trecs record -output ~/demos        # choose where recordings are kept
```

When the recording ends you get its session ID, duration and the path of `terminal.jsonl`, and a Markdown transcript of the commands and their output is written beside it.

By default, recordings are kept in `trecs/recordings` under `$XDG_CONFIG_HOME` (or under `$HOME` when that is not set). Each recording gets its own session directory holding:

- `terminal.jsonl` — the recording: plain text, one JSON object per line, easy to inspect, `grep` and diff;
- `metadata.json` — session ID, start and end times, the shell used, and the paths of any audio file.

### What the recorder does to your shell

The recorder is meant to be invisible: it records what is run, and changes nothing about how it runs. What it does add is small, and listed here.

- Your shell's startup files run first, as they always do. For bash, zsh and fish, a few lines are added after them to write the prompt marks (see below). They keep `$?` (and, in fish, `$status`) as the last command left it; `$ZDOTDIR` is your own while your zsh files run. The marks do add the `__trecs_*` functions and wrap `PS1`/`PS2`, and set `PS0` and `PROMPT_COMMAND` in bash.
- `TRECS_RECORDING=1` is in the environment, so that a profile or script can tell. `TERM` is passed on as it is; only where there is none (CI) or it is `dumb`, `xterm-256color` is given.
- The terminal is put in raw mode so that every key, Ctrl+C and Tab included, reaches the shell; it is given back when the recording ends, whether you end it, the terminal closes, or the recorder is sent `SIGTERM`, `SIGHUP`, `SIGINT` or `SIGQUIT`. The terminal's size follows your window.
- The recording is text: bytes that are not valid UTF-8 (the contents of a binary file printed to the terminal) cannot be kept as they were. Device Control Strings that are only questions to the terminal are left out, and a sixel graphic or inline image bigger than the limit is replaced by a mark.

Everything the recorder writes is private to you (directories `0700`, files `0600`), because a recording holds whatever was on the screen. A recording of the same name as one that is already there is never written over without a word: at a terminal you are asked to type `YES` to replace it, and anything else asks for another name or path (Tab completes paths). Where there is nobody to ask it is an error, unless `-overwrite` is given.

### Every command is a jump point

For bash (4.4 and later), zsh and fish the recorder starts the shell with *prompt marks* (the OSC 133 escape sequences that kitty, iTerm2, WezTerm and VS Code use). They tell the recording, exactly, where each prompt, each command and each command's output begin and end, whatever your prompt looks like. Your own startup files run first, so your prompt, aliases and plugins are all there.

Those marks are what let the player and the editor jump from command to command, and let `trecs` extract a command list. Other shells (sh, dash, ksh and so on) are recorded too, but without marks the command boundaries have to be inferred from the prompt, which is less reliable. If the shell integration gets in the way of a prompt framework of yours, `-no-marks` turns it off.

### Scripted recordings

`-commands-file` makes the recorder type the commands itself instead of reading the keyboard. It needs no terminal at all, so it works in CI, and it is how you keep a tutorial reproducible: change the script, record again.

```sh
trecs record -commands-file setup.txt -name nginx-setup
trecs record -commands-file setup.txt -human-like -cols 100 -rows 30
```

- **The file is checked before anything runs.** It is read as a script (bash, sh and ksh are parsed; fish and the rest are read more roughly) and what is in it that does damage which cannot be undone, or hides what it does, **stops the recording**: a download that is run (`curl … | sh`, `bash -c "$(curl …)"`), text that is decoded and run, `rm -rf` of `/`, the home directory, `*` or a variable that may be empty, a write to a disk, to `/etc`, to `~/.ssh` or to a startup file (`~/.bashrc`...), `chmod u+s`, a shutdown, `kill -1`, a reverse shell (`/dev/tcp`, `nc -e`), a fork bomb, a secret (a key, the environment) sent to another machine, and characters that make a line read differently from what it does. The lines are listed with the reason; nothing has been run. Give `-allow-risky` if you have read them and they are meant. Other things (`sudo`, `&`, `read`, `eval`, `git push --force`...) are only listed. This is a seat belt and not a sandbox: a script written to hide what it does (with `eval` and a variable) will get past it, so it still has to be read. If [ShellCheck](https://www.shellcheck.net) is installed, what it says about the file is shown too (it never blocks).
- **Use a shell with prompt marks.** A script is typed into bash, zsh or fish, which the recorder makes mark their prompts: that is how it knows the shell is ready for the next line and not waiting for the answer to a `read` of the script. A shell that cannot do this (`dash`, plain `sh`, `ksh`) or `-no-marks` is **refused** for `-commands-file`, and the message lists the shells on your machine that will do (`-cmd bash` runs a POSIX `sh` script too). If you must, `-allow-unmarked` types the lines when the shell is quiet, which goes wrong when the script asks for input. Recording **by hand** in such a shell works, but the recording cannot tell where one command ends and the next begins; `trecs record` says so and lists the shells on your machine that can do it, numbered from 0, with the shell you asked for as the last number: press a number to record in that shell, or any other key to stop (`-allow-unmarked` goes on in the shell you asked for without asking).
- **Read the file before you run it.** The commands are typed into a real shell with your permissions, exactly as if they were a shell script: nothing reviews them. The recorder says so in a warning and asks you to type `YES` before it starts (anything else exits). What the audit (and ShellCheck) had to say is shown in yellow, and if there was any, a red line just before the question counts it: `3 warnings were triggered when auditing setup.txt. Review carefully before typing 'YES'.` When standard input is not a terminal (CI, a pipe) there is nobody to ask: the warning is printed and the recording goes ahead.
- One command per line. Blank lines and lines starting with `#` are skipped. A line ending in a backslash, or leaving a quote open, is continued by the next one.
- The recorder waits for the shell's next prompt after each command, so output and prompts come out in the order a person would have seen them. A line is only typed when the shell is back at its prompt, so nothing is ever typed into a command that is running: one that waits for input (or a password) waits, as it would at a keyboard nobody touches, and a long one is never interrupted. `-command-timeout 10m` sets a limit for a job that must end; the shell is then hung up on, as when a terminal is closed, the rest of the file is not typed, the recording is kept as far as it got and `trecs` exits with status 1. So it does for a shell that never shows a prompt (after a minute) and for a recording that cannot be written (a full disk). (With `-allow-unmarked`, a shell without prompt marks can only be told from a *program* that is running, not from the shell's own `read` that is waiting.)
- The lines are a script's, not a person's keystrokes: history expansion (`!word`) is off in bash and zsh, a tab is typed as a tab and not as a request to complete, and a file with a control character in it (Ctrl+C, Escape...: keys, which cannot be seen when the file is read) is refused.
- When the script goes through, `trecs` exits with the status of the shell, which is that of the last command, as a script would: a failing last command fails the job in CI. (A recording made by hand always exits 0.)
- By default each command is entered at once. `-human-like` types with the pauses a person makes, between characters and between commands.
- `-cols` and `-rows` set the terminal size (default: the size of the current terminal, or 172×38 when there is none). They only apply with `-commands-file`; a person at the keyboard is recorded at the size of their own terminal.

### Narration

```sh
trecs record -audio
```

Records your voice alongside the terminal, started at the same moment so the two stay in sync, and stays in sync as viewers jump between commands. The default is mono Opus at 32 kbit/s, about a quarter of the size of the same voice as a 128 kbit/s MP3.

| Option | Default | |
|--------|---------|-|
| `-audio-device` | `default` | The input device |
| `-audio-codec` | `libopus` | `libopus` (Ogg), `libmp3lame` or `aac` |
| `-audio-bitrate` | 32k for Opus | The bitrate |

### Playing back in the terminal

```sh
trecs play -file path/to/terminal.jsonl
trecs play -file path/to/terminal.jsonl -speed 2.0
```

`-speed` is a multiplier (default `1.0`). The recording is written to your terminal, and a recording from someone else can ask a terminal for more than drawing: to put something in the clipboard, to answer a question by typing the answer into your shell. So `play` leaves those out (clipboard writes, device and window reports, version and mode queries) and passes everything that draws.

### Secrets

A recording keeps everything that was on the screen: the token that was exported, the key that was printed, the password in a URL. So it is checked before anything leaves your machine, with the rules of [gitleaks](https://github.com/gitleaks/gitleaks) (about 220 of them: cloud and SaaS tokens, private keys, passwords in URLs, high-entropy values next to words like `secret` or `token`...). Commands, their output and annotations are all looked at.

```sh
trecs scan path/to/session          # list what was found; exits 1 if anything was
```

When a recording ends, anything found is listed (each secret masked, with the command it is in and its line), whether you upload or not: the file holds it either way. Before an upload (`record -upload` or the question that ends a recording, `upload`, and `update` with a `PATH`) a recording with secrets in it is **not sent** unless you type `YES` when asked, or give `-allow-secrets`. Where there is nobody to ask (CI) it is not sent. The way to get rid of a secret is to delete its command in `trecs-edit`, then `trecs update` the recording. A check like this finds what it has a rule for; it is a safety net, not a guarantee.

### Logging in, uploading and sharing

Recordings can be uploaded to a Trecs platform server, which gives you a link to share and a player you can embed.

```sh
trecs login -server https://trecs.example.com    # or set $TRECS_SERVER
trecs upload -name "Setting up nginx" -visibility unlisted path/to/session
trecs record -upload                             # upload as soon as the recording ends, without asking
trecs logout
```

When a recording made at a terminal ends, the recorder asks `Upload to the Trecs Platform? [Y/n]`. `Y`, `y` or Enter uploads it (as `private`, unless `-visibility` says otherwise; you are asked to log in first if needed); any other key does not. `-upload` uploads without asking, and `-no-upload` neither asks nor uploads. When standard input is not a terminal there is nobody to ask, so nothing is uploaded unless `-upload` was given.

A server address must be `https://` (plain `http://` is accepted only for a server on this machine, such as `http://localhost:8080`, or when `login` is given `-allow-insecure-endpoint`, which you should do only on a network you trust), so that your password and session never cross the network in the clear. `login` asks for your email and password; use `-email` to give the email and `-password-stdin` to read the password from standard input (for scripts). The server is remembered, so you only need `-server` the first time. `upload` logs you in if you are not already.

| `upload` option (before the path) | |
|----------------------------------|-|
| `-name` | The recording's title |
| `-description` | Your own description, in Markdown |
| `-commands-as-description`, `-cad` | Describe it with the commands typed (the default) |
| `-annotations-as-description`, `-aad` | Describe it with its "show in nav menu" annotations, each linking to its moment |
| `-visibility` | `private` (the default), `unlisted` or `public` |
| `-audio` | The audio file (default: `audio.opus`, or `.mp3`, `.m4a`, `.ogg`, beside the recording) |
| `-allow-secrets` | Upload even if the recording has secrets in it (see [Secrets](#secrets)) |
| `-markdown` | A Markdown file the server keeps as the recording's Markdown export (and the player's "export as Markdown" gives), instead of one made from the recording's commands. Nothing is looked for beside the recording: it is only sent when named. At most 2 MB, UTF-8 text. |

`PATH` is a session directory or a `terminal.jsonl`. `record -upload` takes `-visibility` too.

### Updating an uploaded recording

Edited the recording, re-recorded the narration, or want a better title? `update` takes the options of `upload` and applies them to a recording that is already there, which you name by the id in its link (or by the link itself). The link, views, likes, comments, channels and tags are kept.

```sh
trecs update -name "Setting up nginx, 2nd take" 0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
trecs update -audio narration.mp3 https://trecs.example.com/watch/0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
trecs update -aad -markdown notes.md 0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d path/to/session   # send the session again too
```

Only what you give is changed. With a `PATH`, the recording's terminal file (and the pictures it shows) is sent again and replaces the one on the server, with its duration, size and searchable commands; the audio beside it goes too, unless `-audio` names another. Without one, `-name`, `-description`, `-visibility`, `-audio` and `-markdown` change just those. `-commands-as-description` and `-annotations-as-description` describe the recording from the session, so they need the `PATH`; without any of the three description options the description stays as it is. A thumbnail moment past the end of a shorter new recording is handed back to the player. Only the author can update a recording.

### Recorder options

| Option | Default | |
|--------|---------|-|
| `-name` | date and time | Session name: a plain name (no `/` or `..`; use `-output` to say where) |
| `-output` | see [Recording](#recording) | Where recordings are kept |
| `-cmd` | your current shell | The shell to run |
| `-terminal` | `true` | Record the terminal |
| `-audio` | `false` | Record audio too |
| `-commands-file` | | Type the commands in this file instead of reading the keyboard |
| `-human-like` | `false` | With `-commands-file`, type like a person |
| `-allow-risky` | `false` | With `-commands-file`: run it although the check of the script blocked it |
| `-allow-unmarked` | `false` | Record in a shell without prompt marks without asking (with `-commands-file`: type the script into it) |
| `-command-timeout` | none | With `-commands-file`, give up if one command runs longer than this (`10m`) |
| `-cols`, `-rows` | the terminal's | With `-commands-file`, the terminal size |
| `-no-marks` | `false` | Don't start the shell with prompt marks |
| `-upload` | `false` | Upload when the recording ends, without asking |
| `-no-upload` | `false` | Don't ask whether to upload the recording when it ends |
| `-allow-secrets` | `false` | With `-upload`: upload even if secrets were found |
| `-overwrite` | `false` | Replace a recording of the same name without asking |
| `-visibility` | `private` | The upload's visibility: `private`, `unlisted` or `public` |

## The editor: `trecs-edit`

```
trecs-edit view   <file>      View and edit a recording
trecs-edit export md  <file>  Export the commands and their output as Markdown
trecs-edit export gif <file>  Render the whole recording as an animated GIF
trecs-edit info   <file>      List the recording's commands
trecs-edit merge  -o <out> <file> <file> [<file>...]
                              Join recordings into one
trecs-edit help
```

`<file>` is a `terminal.jsonl`. The editor never works on your only copy: before a recording is first rewritten, the original is kept beside it as `<file>.backup`, and stays so however many times you save (it is not replaced by a version that already has your changes in it). A save is written beside the recording and moved into place when whole, so a crash or a full disk leaves the recording as it was.

### Viewing and editing

`trecs-edit view` plays the recording in a full-screen terminal interface. Pause anywhere, and step from command to command or pick one from the list.
 
If the recording has a sound (`audio.opus`, or `.mp3`, `.m4a`, `.ogg`, beside it) it is played along with it, or give the file: `trecs-edit view terminal.jsonl -audio narration.mp3`. It is played by `ffmpeg`, which must be on your `PATH`, and follows the recording as it does in the web player: it stops when you pause, and starts again from the right place when you resume, jump to a command, or step to the next or previous one. If `ffmpeg` is missing or has no audio output, the status line says so and the recording plays without sound. (The output is PulseAudio/PipeWire or ALSA on Linux, AudioToolbox on macOS, sndio or OSS on the BSDs; `TRECS_AUDIO_DEVICE=alsa` picks one.) Deleting a command closes the gap it leaves, which moves everything after it earlier; the sound is not cut to match, so after such a save it is out of step with the recording.
 
| Key | Action |
|-----|--------|
| Space or `P` | Pause / resume |
| Ctrl+P / Ctrl+N | Previous / next command |
| Ctrl+L | Browse the commands, with their start times, and jump to any of them |
| Ctrl+E | Edit the current command |
| Ctrl+A | Timed annotations: list, edit or add |
| Ctrl+G | Add an image at this moment (or edit the one on show) |
| `/` | Find in the commands and what they printed (see below) |
| `?` | Help: every key of the page (F1 on the edit screen) |
| Ctrl+T | Show or hide the annotation of the current command |
| Ctrl+X | Dismiss what is on screen (an annotation, a cue card, a preview) |
| Ctrl+F | Full screen |
| `Q` | Quit |

In the edit screen you can change a command's **input** and **output** (a mistyped command, a leaked secret, a noisy stack trace), set its annotation, or delete the command altogether. Ctrl+E inside a field opens it in `$EDITOR`.

| Key | Action |
|-----|--------|
| Ctrl+S | Stage the change and carry on editing |
| Ctrl+W | Write the recording and resume playback |
| Ctrl+D | Delete the command |
| Ctrl+P / Ctrl+N | Previous / next command |
| Tab | Move between fields |
| Ctrl+X | Discard the staged change and resume playback |

When the recording is saved, the timestamps are compressed automatically, so a deleted command leaves no gap behind it. The sound of a recording that has one loses the same time, in one pass of `ffmpeg` (the audio file is kept beside as `<file>.backup`), so what is said stays with what is shown. Only the commands' own time goes: an image or a timed note put after a deleted command's last frame stays, at the same moment of what is left; without `ffmpeg` the recording is still saved, the editor says the sound was not changed, and it is out of step after the first deleted command. Changing or deleting an image waits until deleted commands are saved.

### Annotations

There are three kinds, all written in Markdown (bold, italics, `code` and links), and all stored in the recording itself:

- **Command notes.** A note attached to a command. When playback reaches the command it pauses and the note is shown in a panel for the number of seconds you set (set the duration to 0 to switch the note off), then playback carries on. A note moves with its command when you delete others around it.
- **Timed annotations** (Ctrl+A on the playback screen). Shown at a moment of your choosing, for a duration, either as a card centred over the terminal or in a box at the bottom left. For each one you choose whether to pause playback while it is shown, and whether to show it in the player's command list ("show in nav menu"), where its first line appears under its command.
- **Cue cards.** A card shown centred over the terminal *before* a command (an intro) or *after* it (a post): a title card at the start, a "what we just did" at the end. Add them from the command list with Ctrl+A.

### Images

Ctrl+G on the playback screen (**Add Image**) shows a picture over the terminal at a moment of the recording, for a time you choose, as in a slide between two commands. A dialog asks for:

- the **time** (`MM:SS` or seconds) the image comes up at, which starts as the position of playback;
- the **file**, typed or picked with Ctrl+O (a picker lists the folders and the PNG, JPEG, GIF and WebP files in them);
- the **duration** to show it, from 1 to 600 seconds (5 is proposed).

The recording makes room for it: everything from that moment on is later by the duration, so nothing is covered up - the commands, annotations and the sound after it all move together. The picture is copied into an `images/` folder beside the recording, named after its content (`img-<12 hex>.png`), so the recording does not depend on where the file was; it can be at most 2 MB, and the same picture used twice is stored once. In the list of commands (Ctrl+L) the image is a row of its own under the command it is in, `display image: <file name>` (the name only, not the path), and it can be treated as a command is: Ctrl+E edits it (time, file, duration), Ctrl+D deletes it, Ctrl+P plays from it. Ctrl+E while one is on show edits it as well. Changing or deleting an image closes up the time it made, in the picture and in the sound.

**Find.** `/` opens a box over the commands and what they printed (the web player's search, with the same options). Type at least three characters and press Enter: the matches are listed under their command, each with the line before and after it and the term highlighted. **Regex** (on to begin with) reads the term as a regular expression - Go's, not PCRE: no lookaround or back-references, which are refused with a message rather than quietly matching something else; **Match case** (off) makes case count - without it, case counts only if the term has a capital. Alt+R and Alt+C switch them. Enter on a match goes there, paused, with the line on the screen; Esc closes the box and leaves playback paused.

**Copying.** While a recording plays, the terminal's own selection works as it does anywhere. While playback is paused, has ended, or a box is open, the editor takes the mouse instead: drag over the playback screen, the results of a search or the help to select, and letting go copies. What is copied is what is on the screen, a line per row, without the spaces at the end of a row. (Shift+drag is the terminal's own selection, as ever.) The copy goes to the system clipboard through `wl-copy`, `xclip`, `xsel`, `pbcopy` or `clip.exe`, whichever the machine has, and also to the terminal as an OSC 52 sequence, which is what reaches your clipboard over ssh; GNOME Terminal and other VTE terminals ignore OSC 52, so they need one of the programs. In the text fields of the edit page, Ctrl+Q copies the selection and Ctrl+X cuts it, to the system clipboard as well.

The sound of a recording that has one is changed to match, with `ffmpeg`: silence is put in for the image's duration, and taken out again when an image is shortened or deleted. The audio file is kept beside the original as `<file>.backup`. Without `ffmpeg` the recording is still changed and the editor says that the sound is not, so it will be out of step after the image.

The editor draws the picture where the terminal supports sixel (see below); elsewhere, and for WebP, it shows a card with the file name and size. `trecs upload` sends the images with the recording, and the web player shows them over the terminal for the same moments (the list of commands there has the same `display image:` rows). The Markdown export puts `![display image: name](images/<file>)` at the image's moment.

On a narrow terminal the key line at the top of a page shows only the keys that fit, most useful first, and always ends with `/ find` (on the playback page) and `? help`.

### Pictures (sixel and inline images)

A program that draws pictures in the terminal is recorded with its pictures, and the pictures are shown by both players. Two ways of drawing them are understood: sixel graphics (`img2sixel`, `chafa -f sixel`, `lsix`, `gnuplot`'s `sixel` terminal...) and inline images, the iTerm2 protocol (`imgcat`, `viu`, `tdf` and others, in a terminal such as WezTerm that speaks it; PNG, JPEG and GIF are shown in the editor, and WebP too in the web player). The kitty graphics protocol is not.

- **Recording.** The graphic is kept, as the program wrote it, in the recording's frames. An image of more than 2 MB is not kept: it is left out and a marker takes its place, so the recording stays a size that can be shared. The terminal's own answers (what the program asks the terminal about, and what comes back) are not kept. Your terminal has to support sixel for the program to send any: `trecs` passes the terminal's capabilities through unchanged. The size of a character cell, in pixels, is written to the recording's first line (`cell_width`, `cell_height`), because that is what tells how many cells a picture covered; a terminal that does not report it is taken to be 8x16, and so are recordings made before this existed.
- **In the editor** the picture is drawn over the cells it covered, if the terminal the editor runs in supports sixel (foot, WezTerm, Windows Terminal and others; the editor asks the terminal). It is scaled to the editor's cell size and cut to what is on screen. Elsewhere, and while a dialog is open, the cells show a shaded block (`▒`). `TRECS_SIXEL=0` turns the pictures off, `TRECS_SIXEL=1` on, whatever the terminal says; `TRECS_CELL_SIZE=WxH` (for example `10x20`) gives the cell size in pixels if the terminal does not report it.
- **In the web player** the picture is drawn in the page, over the cells it covered; where it cannot be (the view is narrower than the recording and wraps its lines) a shaded block is shown. Text selected over a picture is copied as blanks.

Limits: a picture's box is as wide as the screen at most (what is wider is cut, as a terminal cuts it) and has at most 16384 cells; the pictures on the screen share about 65000 cells between them, and the oldest is replaced when there is no room for a new one. The picture is taken to end where a terminal leaves the cursor: on the line below it, in the column it began at. A recording line may be 16 MiB.

### Merging

```sh
trecs-edit merge -o tutorial.jsonl part1.jsonl part2.jsonl part3.jsonl
trecs-edit merge -gap 2000 -o tutorial.jsonl part1.jsonl part2.jsonl
```

Joins recordings in the order given: each starts where the one before it ended, after a pause of `-gap` milliseconds (default `1000`), and the screen is cleared. Annotations travel with their frames. The input files are not changed. Record a long tutorial in parts, re-record the part that went wrong, and merge.

### Exporting

```sh
trecs-edit export md  session.jsonl -title "Setting up nginx"
trecs-edit export gif session.jsonl
```

`export md` writes the commands and their output as Markdown, to `<file>.md` beside the recording, with `-title` as its heading. This is the same transcript `trecs record` writes when a recording ends, and makes a good starting point for written documentation. `trecs-edit info <file>` just prints the commands. (`trecs-edit export <file>`, without `md`, still does this.)

`export gif` renders the whole recording as an animated GIF, to `<file>.gif` (or the file named by `-o`): the terminal as `trecs-edit view` shows it, at the size it was recorded, from the first frame to the last. It is a picture of text, so it is larger than the recording by a long way, and has no sound. A GIF of several minutes is usually a few megabytes. Pictures (sixel, inline images) and timed annotations are not in it.

| Flag | Default | |
|---|---|---|
| `-o` | `<file>.gif` | The GIF to write |
| `-idle` | `2s` | The longest pause shown: a pause of a minute at the prompt is two seconds in the GIF. `0` keeps every pause |
| `-speed` | `1` | Play the recording this many times faster |
| `-fps` | `10` | The most pictures a second (1 to 50). Changes closer together than that are shown together |
| `-hold` | `3s` | How long the last picture stays before the GIF starts again. `0` for no wait |
| `-loop` | `0` | Repeats after the first play: `0` for ever, `-1` once |
| `-font-size` | `16` | The size of the text in pixels. The picture is as large as the terminal, in cells of that size |
| `-font` | built in | A monospaced TrueType or OpenType font. The built-in one is Go Mono. A font with no bold of its own is smeared for bold text |
| `-fg`, `-bg` | `#c9d1d9`, `#0d1117` | The colours of text the recording does not colour, and of the screen, as `#rrggbb` |
| `-cols`, `-rows` | as recorded | The terminal's size, for a recording that does not say |
| `-no-cursor` | | Leave the cursor out |

A GIF has 256 colours. All that a recording uses are kept when they fit (a terminal rarely uses more than a few dozen, and the edges of letters take three more each); otherwise the 255 that stand for them best are chosen. Block elements, box-drawing lines and braille are drawn by the exporter rather than taken from the font, so they meet their neighbours without gaps. A character the font has no glyph for is left blank: `-font` takes a font that has it. To make it smaller, lower `-fps`, raise `-speed`, lower `-idle` or `-font-size`, or trim the recording first.

We recommend using [JetBrainsMonoNerdFontMono-Regular.ttf](./fonts/JetBrainsMonoNerdFontMono-Regular.ttf), as it includes nerd icons, needed for TUI tools like [gh-dash](https://github.com/dlvhdr/gh-dash). You can download additional fonts from the [nerd-fonts repo](https://github.com/ryanoasis/nerd-fonts).

## The player

Recordings are played in the browser by the Trecs web player, as the watch page of a Trecs platform or embedded in your own pages. Because it renders text rather than playing pixels, it is sharp on a phone and on a 4K display alike, and a recording loads at once even on a poor connection.

- **Command navigation.** Previous and next buttons and a command list; click any command to jump straight to it. Annotations marked "show in nav menu" appear under their commands.
- **Copy, don't retype.** Pause, then select and copy any text on the screen, or use **Copy screen** to copy all of it.
- **Annotations and cue cards** appear as you authored them; a viewer can hide or show them.
- **Audio** is kept in sync, however the viewer jumps around, with mute and volume controls.
- **Share a moment.** The right-click menu copies the recording's URL, the URL *at the current time*, the embed code, or an image link for a README. Links accept a start time (`?t=21`, `7:21` or `1:02:03`).
- **Full screen**, and a layout that adapts from phone to desktop.

### Embedding

The platform's share menu gives you an `<iframe>` embed code. To host the player yourself, include the player bundle and use the `<trecs-player>` element:

```html
<trecs-player src="session.jsonl" audio="session.opus"></trecs-player>
<script src="trecs-player.js"></script>
```

| Attribute | |
|-----------|-|
| `src` | URL of a `.jsonl` recording |
| `audio` | URL of an audio track, used with `src` |
| `recording-id` and `api-base` | Load a recording from a Trecs platform instead of `src` |
| `autoplay` | Start playing when loaded (muted, if there is audio) |
| `start-at` | Where to start: seconds, `7:21` or `1:02:03` |
| `preview` | A still with a play button, for cards and thumbnails |

A recording is plain text, so it can also be packed with the player into a single HTML file that opens with a double-click, with no server.

## Free software: the AGPLv3

Trecs is free software, released under the [GNU Affero General Public License, version 3](LICENSE) (AGPLv3). You are free to use it, change it and share it, on one condition that matters more than all the others:

> **If you distribute Trecs, you must distribute its source.**
>
> Whenever you give anyone a copy of Trecs, or of anything that contains it — a binary, a package, a container image, an installer, a copy bundled into your product or appliance — the people who receive it must also receive the **complete source code**, under the AGPLv3. That includes every change you made. If Trecs is built into a larger program, the whole program is covered, not just the Trecs part. (Shipping Trecs *beside* separate, unrelated programs is fine, provided its source comes with it.)
>
> In short: you can redistribute Trecs, but you cannot make it closed, rebrand it as your own proprietary product, or bundle it into one. If that is what you need, you need an [enterprise licence](#enterprise-licences).

The same idea applies to running it as a service. If you run a *modified* Trecs so that other people use it over a network (a hosted recorder or platform, say), you must offer those users the source of your modified version. That is the "A" in AGPL: hosting it on a server instead of shipping it does not get you out of the rule above.

In plain terms (a summary, not legal advice; the licence text is what counts):

**You are free to**

- use Trecs for any purpose, including commercial work, and make as many recordings as you like. What you record is yours; the licence does not reach it;
- read, study and modify the source;
- run it, modified or not, inside your own organisation;
- share copies, modified or not, and charge for doing so, as long as the source goes with them.

**You must**

- provide the complete source, under the AGPLv3, to everyone you distribute a copy to (above), and to everyone who uses your modified version over a network;
- keep the copyright and licence notices, and mark your changes as yours.

**You may not**

- distribute Trecs, or something built from it, under a proprietary licence or without its source;
- add restrictions of your own on top of the AGPLv3 for the people who receive your version;
- remove the licence and copyright notices.

## Enterprise licences

Solwork also offers enterprise licences. They replace the AGPLv3's conditions with commercial terms, so you don't have to publish your modifications or ship your source, and they cover:

- **A private, white-labelled instance of the Trecs platform**, running on your own servers or fully managed by Solwork, under your own name and branding.
- **Integration with your ticketing, support and bug-tracking system**, so a recording can travel with the ticket it belongs to instead of a paragraph of "steps to reproduce".
- **White-labelling and distribution of the recorder**, so you can rebrand `trecs` and ship it to your customers, support staff or users as part of your own tooling.

To talk about an enterprise licence, write to jesse@solwork.io.

## Contributing

Contributions are welcome (see [CONTRIBUTING.md](CONTRIBUTING.md) for the details): bug reports, fixes, documentation, new ideas, and recordings that show a problem. For anything bigger than a small fix, please open an issue first, so we can agree on the direction before you spend time on it.

- Fork the repository, work on a branch, and open a pull request.
- Keep a change focused, add tests where you can, and make sure `go test ./...` passes.
- Contributions are made under the contributor licence agreement in [CONTRIBUTING.md](CONTRIBUTING.md): you keep your copyright, the project stays AGPLv3, and Solwork may also offer your contribution under its enterprise licences.

A note on the AGPLv3 and your changes: nothing obliges you to send them to us, but if you distribute a modified Trecs, or run one as a service for others, you have to publish its source under the AGPLv3 anyway. Sending your change upstream means you don't have to carry a fork — and everyone benefits from it.

## Licence

Trecs is released under the [GNU Affero General Public License v3.0](LICENSE). Enterprise licences are available separately; see [above](#enterprise-licences).
