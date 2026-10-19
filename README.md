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
| `trecs-edit` | The editor. Trims and fixes a recording, adds annotations, merges recordings, exports Markdown. |

Recordings are played in the browser by the Trecs web player (see [The player](#the-player)).

Sample recording, showcasing the excellent [gh-dash util](https://github.com/dlvhdr/gh-dash)

[![gh-dash & prs - tracks PRs from the comfort of your terminal](https://jesse.packman.io/v1/recordings/d628fafa-a1ef-430b-b04f-0ac78976db87/preview.gif)](https://jesse.packman.io/watch/d628fafa-a1ef-430b-b04f-0ac78976db87)

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
trecs-edit view <recording>        # play it back, fix it, annotate it
trecs upload <recording>           # get a link to share (asks you to log in first)
```

## The recorder: `trecs`

```
trecs record [options]       Record a terminal session
trecs play [options]         Play a recording back in the terminal
trecs login [options]        Log in to a Trecs server (saves the session)
trecs logout                 Log out and forget the saved session
trecs upload [options] PATH  Upload a recording
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

### Every command is a jump point

For bash (4.4 and later), zsh and fish the recorder starts the shell with *prompt marks* (the OSC 133 escape sequences that kitty, iTerm2, WezTerm and VS Code use). They tell the recording, exactly, where each prompt, each command and each command's output begin and end, whatever your prompt looks like. Your own startup files run first, so your prompt, aliases and plugins are all there.

Those marks are what let the player and the editor jump from command to command, and let `trecs` extract a command list. Other shells (sh, dash, ksh and so on) are recorded too, but without marks the command boundaries have to be inferred from the prompt, which is less reliable. If the shell integration gets in the way of a prompt framework of yours, `-no-marks` turns it off.

### Scripted recordings

`-commands-file` makes the recorder type the commands itself instead of reading the keyboard. It needs no terminal at all, so it works in CI, and it is how you keep a tutorial reproducible: change the script, record again.

```sh
trecs record -commands-file setup.txt -name nginx-setup
trecs record -commands-file setup.txt -human-like -cols 100 -rows 30
```

- **Read the file before you run it.** The commands are typed into a real shell with your permissions, exactly as if they were a shell script: nothing reviews them. The recorder says so in a warning and asks you to type `YES` before it starts (anything else exits). When standard input is not a terminal (CI, a pipe) there is nobody to ask: the warning is printed and the recording goes ahead.
- One command per line. Blank lines and lines starting with `#` are skipped. A line ending in a backslash, or leaving a quote open, is continued by the next one.
- The recorder waits for the shell's next prompt after each command, so output and prompts come out in the order a person would have seen them.
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

`-speed` is a multiplier (default `1.0`).

### Logging in, uploading and sharing

Recordings can be uploaded to a Trecs platform server, which gives you a link to share and a player you can embed.

```sh
trecs login -server https://trecs.example.com    # or set $TRECS_SERVER
trecs upload -name "Setting up nginx" -visibility unlisted path/to/session
trecs record -upload                             # upload as soon as the recording ends, without asking
trecs logout
```

When a recording made at a terminal ends, the recorder asks `Upload to the Trecs Platform? [Y/n]`. `Y`, `y` or Enter uploads it (as `private`, unless `-visibility` says otherwise; you are asked to log in first if needed); any other key does not. `-upload` uploads without asking, and `-no-upload` neither asks nor uploads. When standard input is not a terminal there is nobody to ask, so nothing is uploaded unless `-upload` was given.

`login` asks for your email and password; use `-email` to give the email and `-password-stdin` to read the password from standard input (for scripts). The server is remembered, so you only need `-server` the first time. `upload` logs you in if you are not already.

| `upload` option (before the path) | |
|----------------------------------|-|
| `-name` | The recording's title |
| `-description` | Your own description, in Markdown |
| `-commands-as-description`, `-cad` | Describe it with the commands typed (the default) |
| `-annotations-as-description`, `-aad` | Describe it with its "show in nav menu" annotations, each linking to its moment |
| `-visibility` | `private` (the default), `unlisted` or `public` |
| `-audio` | The audio file (default: `audio.opus`, or `.mp3`, `.m4a`, `.ogg`, beside the recording) |

`PATH` is a session directory or a `terminal.jsonl`. `record -upload` takes `-visibility` too.

### Recorder options

| Option | Default | |
|--------|---------|-|
| `-name` | date and time | Session name |
| `-output` | see [Recording](#recording) | Where recordings are kept |
| `-cmd` | your current shell | The shell to run |
| `-terminal` | `true` | Record the terminal |
| `-audio` | `false` | Record audio too |
| `-commands-file` | | Type the commands in this file instead of reading the keyboard |
| `-human-like` | `false` | With `-commands-file`, type like a person |
| `-cols`, `-rows` | the terminal's | With `-commands-file`, the terminal size |
| `-no-marks` | `false` | Don't start the shell with prompt marks |
| `-upload` | `false` | Upload when the recording ends, without asking |
| `-no-upload` | `false` | Don't ask whether to upload the recording when it ends |
| `-visibility` | `private` | The upload's visibility: `private`, `unlisted` or `public` |

## The editor: `trecs-edit`

```
trecs-edit view   <file>      View and edit a recording
trecs-edit export <file>      Export the commands and their output as Markdown
trecs-edit info   <file>      List the recording's commands
trecs-edit merge  -o <out> <file> <file> [<file>...]
                              Join recordings into one
trecs-edit help
```

`<file>` is a `terminal.jsonl`. The editor never works on your only copy: before a recording is rewritten, the original is kept beside it as `<file>.backup`.

### Viewing and editing

`trecs-edit view` plays the recording in a full-screen terminal interface. Pause anywhere, and step from command to command or pick one from the list.

| Key | Action |
|-----|--------|
| Space or `P` | Pause / resume |
| Ctrl+P / Ctrl+N | Previous / next command |
| Ctrl+L | Browse the commands, with their start times, and jump to any of them |
| Ctrl+E | Edit the current command |
| Ctrl+A | Timed annotations: list, edit or add |
| Ctrl+G | Add an image at this moment (or edit the one on show) |
| `?` | The full help menu of the page (F1 on the edit screen) |
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

When the recording is saved, the timestamps are compressed automatically, so a deleted command leaves no gap behind it.

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

The sound of a recording that has one is changed to match, with `ffmpeg`: silence is put in for the image's duration, and taken out again when an image is shortened or deleted. The audio file is kept beside the original as `<file>.backup`. Without `ffmpeg` the recording is still changed and the editor says that the sound is not, so it will be out of step after the image.

The editor draws the picture where the terminal supports sixel (see below); elsewhere, and for WebP, it shows a card with the file name and size. `trecs upload` sends the images with the recording, and the web player shows them over the terminal for the same moments (the list of commands there has the same `display image:` rows). The Markdown export puts `![display image: name](images/<file>)` at the image's moment.

On a narrow terminal the key line at the top of a page shows only the keys that fit, most useful first, and always ends with `? full help menu`.

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
trecs-edit export session.jsonl -title "Setting up nginx"
```

Writes the commands and their output as Markdown, to `<file>.md` beside the recording, with `-title` as its heading. This is the same transcript `trecs record` writes when a recording ends, and makes a good starting point for written documentation. `trecs-edit info <file>` just prints the commands.

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
