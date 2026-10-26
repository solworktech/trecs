# Recorder transparency tests

The recorder's one promise about the shell is that it **only records**: a session run under `trecs record` must be
indistinguishable, to the shell and to every program in it, from the same session run bare. These tools test that.

Linux only as written (they read `/proc`, and use the `pty`, `termios` and `fcntl` modules). Not covered by anything here:
macOS (bash 3.2 and zsh as the login shell, `TIOCGETA`), fish 4 (the container had fish 3.7, so `fishNativeIntegration` is
untested), Windows.

## recfuzz.py: a differential fuzzer

```
go build -o recorder/fuzz/trecs ./recorder        # or: TRECS_BIN=/path/to/trecs
cd recorder/fuzz
python3 recfuzz.py --seeds 1-60 --shells bash,zsh,fish --steps 18 --jobs 8 --json out.json
```

For each seed it builds a random user profile (an rc file with 0-3 of 79 settings people really have: `set -e`, `set -x`,
`set -u`, exported `PROMPT_COMMAND`, `PS1` with `$?`, DEBUG and ERR traps, aliases and functions that shadow `printf`/`print`/`string`,
`unsetopt PROMPT_PERCENT`, precmd/preexec hooks, vi mode, `IGNOREEOF`...) and a random session of 18 steps drawn from 70 kinds
(typed lines, status chains, `$_`, `PIPESTATUS`, multi-line input, heredocs, editing keys, history, completion, `^C ^Z ^\ ^D`,
job control, signals, raw binary input of all 256 byte values, UTF-8 split across writes, 400 KB pastes, floods of output,
terminal queries answered by the harness (CPR, DA1, OSC 11), alternate screen, sixel, resizes, nested shells, vim and less,
`exec`, and the many ways to end the shell).

It then runs the session **three times** on a pseudo-terminal: bare, bare again (a control for noise), and under the recorder.
A difference counts only where the two bare runs agree. Compared: the bytes the terminal gets, step by step (and the shell's
start-up output); the files the session wrote; a probe of the shell's state taken in the middle and at the end (options,
variables, environment, functions, aliases, traps, fds, signal masks, limits, tty settings, process groups, history); the
terminal's settings after the session; processes and temporary files left behind; the shell's exit status; and absolute
checks (bytes typed in raw mode arrive intact, no integration text leaks into the output).

What the recorder changes **on purpose** is not hidden: it is listed at the end of every run as the inventory of known
differences (the `TRECS_RECORDING` variable, the prompt variables and their marks, the `__trecs_*` functions, the hook arrays).

A failing seed is kept in `work/fail/<shell>-<seed>/` with the raw bytes of all three runs, and re-run with
`python3 recfuzz.py --seeds N-N --shells zsh --keep`.

## recfaults.py: fault injection

```
python3 recfaults.py                   # all
python3 recfaults.py signals_to_recorder disk_full
```

Hand-built situations, each compared with the bare shell where there is a reference: signals sent to the recorder
(16 kinds), `kill -9`, the terminal closed under foreground / background / nohup jobs, a recording that cannot be written
(tmpfs of 64 KB), unusable output directories, `TMPDIR` missing / a file / unset (all three shells), the recorder's stdout
a pipe whose reader has gone, `RLIMIT_NOFILE` of 6-32, 16 odd environments (`TERM`, `HOME`, `LANG`, 1.5 MB of environment,
`BASH_ENV`, `POSIXLY_CORRECT`...), a burst of 300 resizes, 40 MB of output (throughput) and 300 keystrokes (latency), a consumer
that stops reading for 2 s mid-flood, `^C` while the output is blocked, typeahead and pastes up to 1 MB, a recorder inside
a recorded shell, the many ways a shell ends, the terminal's settings after the end, `-cmd` variants.

`ptyrun.py` is the pseudo-terminal runner both use (it plays the terminal emulator: it answers the queries programs send).
`adhoc.py` runs one experiment, bare against recorded: `python3 adhoc.py bash 'set -e' 'false && true' 'echo $?'`.
