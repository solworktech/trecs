# Recorder transparency tests

The recorder's one promise about the shell is that it **only records**: a session run under `trecs record` must be
indistinguishable, to the shell and to every program in it, from the same session run bare. These tools test that.

Written on Linux; made to run on macOS as well (see *Platforms*), where it has not been run by its author: the first run on a Mac may
need a fix or two. Not covered by anything here: fish 4 (the container had fish 3.7, so `fishNativeIntegration` is untested), Windows.

## Platforms

- **Shells** are found on `PATH` (`bash`, `zsh`, `fish`), or named in `TRECS_FUZZ_BASH`, `TRECS_FUZZ_ZSH`, `TRECS_FUZZ_FISH`. macOS's own
  bash is 3.2, which cannot mark its prompts: `brew install bash` and set `TRECS_FUZZ_BASH=$(brew --prefix)/bin/bash`.
- **Linux** reads `/proc` for the state of the shell (signal masks, limits, descriptors, the session). **macOS** has no `/proc`: the state
  comes from `ps`, `ulimit -a` and `/dev/fd`, and what the system does not tell (signal masks, the session id) is left out of the comparison.
- Commands the sessions type that macOS lacks (`md5sum`, `sha256sum`, `setsid`) are stood in for by small scripts in `shims/`, made on first
  use and put on the sessions' `PATH` (not on Linux, which has them).
- On macOS a bare pty can turn `\n` into `\r\r\n` when its queue is full (the BSD line discipline writes the CR again when it retries the LF). `ptyrun.pty_cr_doubling()` floods a bare shell's pty with a slow reader and counts them; only if it sees any are doubled CRs taken out of the streams before they are compared.
- zsh: Debian and Ubuntu's global `zshrc` runs `compinit`, which asks a question at a prompt if a directory it reads is group-writable (CI runners have some); the harness's `.zshenv` sets `skip_global_compinit`.
- The disk-full scenario mounts a 64 KB tmpfs, which needs Linux and root; elsewhere it falls back to `RLIMIT_FSIZE`.

## Exit status

`recfuzz.py` exits 1 for a harness error or a difference of a kind that changes what the shell or a program in it **does**: a hang, a shell
that does not end, files that differ, corrupted input, text of the integration in the output, leftover processes, a changed terminal or
another exit status, or a bare shell that never gives a prompt under the plain rc that every seed starts from (the shell is then
not being tested at all). Differences in the bytes printed, in the shell's state and in the observation files are in the report but do not fail
the run, because a few are known and cosmetic (see *What remains* in `AUDIT.md`); `--strict` fails on any HIGH or MED finding.
`recfaults.py` exits 1 for a FAIL or an ERROR, except those in `KNOWN_FAILURES` at the top of the file, which are listed as `KNOWN`.

## CI

The `fuzz` job of the `recorder` workflow runs both on Linux and on macOS: `workflow_dispatch` inputs `fuzz_seeds`, `fuzz_shells`
and `fuzz_strict`. Seeds 1-12 in three shells take about ten minutes per OS on two jobs.

The run keeps an artifact per OS (`fuzz-Linux`, `fuzz-macOS`, 14 days): the two logs, the JSON report, `fuzz-work/fail` (the raw bytes of
every fuzz session that differed) and `faultwork/logs` (the first and last 64 kB of every fault-suite session, one `.bin` per test).
Before the fuzzer, a step prints what each shell gives under a bare pty, so a shell that never reaches a prompt shows itself at once.

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
