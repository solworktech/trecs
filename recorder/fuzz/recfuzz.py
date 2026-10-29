#!/usr/bin/env python3
"""Differential fuzzer for the trecs recorder.

The claim under test: the recorder only records. A shell session run under `trecs record` must be
indistinguishable, to the shell and to the programs it runs, from the same session run bare.

For each seed it builds a random user profile (rc file) and a random session (typed lines, editing
keys, control characters, job control, signals, binary input and output, terminal queries, resizes,
nested shells, big floods), then runs it three times on a pseudo-terminal:

    A1  bare shell          A2  bare shell again (noise control)        R   shell under the recorder

A difference counts only where A1 == A2 (the session is deterministic) and R differs. Compared are the
bytes the terminal gets (per step), the files the session wrote, a snapshot of the shell's state taken
by probes (options, variables, environment, fds, signal masks, limits, tty settings, traps...), the
terminal's settings after the end, processes and temporary files left behind, and a few absolute
checks (bytes typed in raw mode arrive intact, `$?` is the real status, no integration text leaks).
"""
import argparse
import concurrent.futures as cf
import multiprocessing as mp
import hashlib
import json
import os
import random
import re
import shutil
import sys
import time
import traceback

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ptyrun import IS_LINUX, LOCALE, SHELLS, Run, preflight, processes_in, pty_cr_doubling, tool_path, undouble_cr  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
TRECS = os.environ.get("TRECS_BIN", os.path.join(HERE, "trecs"))

# ---------------------------------------------------------------------------------------------- profiles

BASH_RC = [
    ("export_pc_append", 'export PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND; }history -a"'),
    ("pc_string", "PROMPT_COMMAND='PCN=$((PCN+1))'"),
    ("pc_array", "PROMPT_COMMAND=(':' 'PCA=$((PCA+1))')"),
    ("pc_status", "PROMPT_COMMAND='echo \"pc:$?\" >> $OBS/pc.log'"),
    ("ps1_status", "PS1='[$?] P> '"),
    ("ps1_brackets", "PS1='\\[\\e[1;32m\\]P\\[\\e[0m\\]> '"),
    ("ps0", "PS0='<run>'"),
    ("ps2", "PS2='.. '"),
    ("set_u", "set -u"),
    ("set_x", "set -x"),
    ("set_e", "set -e"),
    ("posix", "set -o posix"),
    ("vi_mode", "set -o vi"),
    ("debug_trap", "trap 'LASTCMD=$BASH_COMMAND' DEBUG"),
    ("extdebug", "shopt -s extdebug; trap ':' DEBUG"),
    ("err_trap", "trap 'echo ERR:$?' ERR"),
    ("alias_printf", "alias printf='echo ALIASED-PRINTF'"),
    ("func_printf", "printf() { echo \"my printf: $*\"; }"),
    ("func_local", "local() { echo 'my local'; }"),
    ("alias_declare", "alias declare='echo ALIASED-DECLARE'"),
    ("alias_case", "alias case='echo ALIASED-CASE'"),
    ("histcontrol", "HISTCONTROL=ignoreboth; HISTSIZE=5"),
    ("histverify", "shopt -s histverify"),
    ("nocaseglob", "shopt -s nocaseglob extglob nullglob failglob"),
    ("cnf_handle", "command_not_found_handle() { echo \"nope: $1\" >&2; return 127; }"),
    ("ignoreeof", "IGNOREEOF=2"),
    ("noclobber", "set -o noclobber"),
    ("no_bracketed", "bind 'set enable-bracketed-paste off'"),
    ("emacs_bind", "bind '\"\\C-o\": \"echo bound\\n\"'"),
    ("export_ps1", "export PS1='E> '"),
    ("readonly_pc", "PROMPT_COMMAND=:; readonly PROMPT_COMMAND"),
    ("unset_ps1", "PS1=; unset PS2"),
    ("checkwinsize_off", "shopt -u checkwinsize"),
    ("monitor_off", "set +m"),
]
ZSH_RC = [
    ("prompt_subst", "setopt PROMPT_SUBST; PROMPT='${PWD:t} P> '"),
    ("prompt_status", "PROMPT='%? P> '"),
    ("no_prompt_percent", "unsetopt PROMPT_PERCENT; PROMPT='P> '"),
    ("precmd_fn", "precmd() { PCN=$((PCN+1)); echo \"precmd:$?\" >> $OBS/pc.log }"),
    ("preexec_fn", "preexec() { echo \"preexec:$1\" >> $OBS/pc.log }"),
    ("precmd_array", "precmd_functions=(); precmd_functions+=(myhook); myhook() { :; }"),
    ("nounset", "setopt NO_UNSET"),
    ("errexit", "setopt ERR_EXIT"),
    ("errreturn", "setopt ERR_RETURN"),
    ("xtrace", "setopt XTRACE"),
    ("ksharrays", "setopt KSH_ARRAYS SH_WORD_SPLIT"),
    ("posix_builtins", "setopt POSIX_BUILTINS POSIX_IDENTIFIERS"),
    ("print_exit", "setopt PRINT_EXIT_VALUE"),
    ("share_hist", "setopt SHARE_HISTORY INC_APPEND_HISTORY; HISTFILE=$HOME/.zsh_history"),
    ("emulate_sh_opts", "setopt SH_GLOB KSH_GLOB NO_BARE_GLOB_QUAL RC_QUOTES"),
    ("nomatch_off", "unsetopt NOMATCH"),
    ("alias_print", "alias print='echo ALIASED-PRINT'"),
    ("func_print", "print() { echo \"my print: $*\"; }"),
    ("alias_autoload", "alias autoload='echo ALIASED-AUTOLOAD'"),
    ("vi_mode", "bindkey -v"),
    ("ps2", "PS2='.. '"),
    ("right_prompt", "RPROMPT='[r]'"),
    ("transient", "setopt TRANSIENT_RPROMPT PROMPT_CR PROMPT_SP"),
    ("ignoreeof", "setopt IGNORE_EOF"),
    ("noclobber", "setopt NO_CLOBBER"),
    ("add_hook", "autoload -Uz add-zsh-hook; add-zsh-hook precmd _u1; _u1() { :; }"),
    ("export_prompt", "export PROMPT='E> '"),
    ("unset_prompt", "unset PS2"),
    ("compinit", "autoload -Uz compinit && compinit -u -d $HOME/.zcompdump"),
]
FISH_RC = [
    ("prompt_status", "function fish_prompt; echo -n \"[$status] P> \"; end"),
    ("prompt_color", "function fish_prompt; set_color red; echo -n 'P'; set_color normal; echo -n '> '; end"),
    ("prompt_multi", "function fish_prompt; echo top; echo -n 'P> '; end"),
    ("right_prompt", "function fish_right_prompt; echo -n '[r]'; end"),
    ("postexec", "function _pe --on-event fish_postexec; echo \"post:$status\" >> $OBS/pc.log; end"),
    ("preexec", "function _pre --on-event fish_preexec; echo \"pre:$argv\" >> $OBS/pc.log; end"),
    ("cnf", "function fish_command_not_found; echo \"nope: $argv[1]\" >&2; end"),
    ("func_printf", "function printf; echo \"my printf\"; end"),
    ("func_string", "function string; echo \"my string\"; end"),
    ("func_functions", "function functions; echo \"my functions\"; end"),
    ("abbr", "abbr -a gst 'echo status'"),
    ("vi_mode", "fish_vi_key_bindings"),
    ("export_var", "set -gx FOO bar"),
    ("prompt_cmdline", "function fish_prompt; echo -n (commandline | string length) '> '; end"),
    ("prompt_jobs", "function fish_prompt; jobs -c >/dev/null; echo -n 'P> '; end"),
    ("no_fish_prompt_default", "functions -e fish_prompt"),
]


def make_profile(rng, shell):
    pool = {"bash": BASH_RC, "zsh": ZSH_RC, "fish": FISH_RC}[shell]
    k = rng.choice([0, 0, 1, 1, 2, 3])
    picked = rng.sample(pool, k)
    return [name for name, _ in picked], [line for _, line in picked]


def write_profile(shell, home, lines):
    os.makedirs(home, exist_ok=True)
    if shell == "bash":
        base = "PS1='P> '\nHISTFILE=$HOME/.bash_history\ntrap 'echo $? > \"$OBS/exitstatus\"' EXIT\n"
        open(os.path.join(home, ".bashrc"), "w").write(base + "\n".join(lines) + "\n")
        open(os.path.join(home, ".bash_profile"), "w").write(". ~/.bashrc\n")
    elif shell == "zsh":
        base = "PROMPT='P> '\nHISTFILE=$HOME/.zsh_history\nSAVEHIST=100\nHISTSIZE=100\nunsetopt BEEP\nzshexit() { echo $? > \"$OBS/exitstatus\"; }\n"
        open(os.path.join(home, ".zshrc"), "w").write(base + "\n".join(lines) + "\n")
        # Debian and Ubuntu's /etc/zsh/zshrc runs compinit, which stops to ask about "insecure directories" if one of the directories
        # it reads from (on a CI runner, several) can be written by the group: the shell then waits for an answer at a prompt that is
        # not ours. The person's own rc is read after it, so it cannot be turned off from there; .zshenv is read before it.
        open(os.path.join(home, ".zshenv"), "w").write("skip_global_compinit=1\n")
    else:
        d = os.path.join(home, ".config", "fish")
        os.makedirs(d, exist_ok=True)
        base = ("set -g fish_greeting\nfunction fish_prompt; echo -n 'P> '; end\n"
                "function _x --on-event fish_exit; echo $status > $OBS/exitstatus; end\n")
        open(os.path.join(d, "config.fish"), "w").write(base + "\n".join(lines) + "\n")


# -------------------------------------------------------------------------------------------- the probes

PROBE_BASH = r'''
{
echo "--- flags: $-"
echo "--- set -o"; set -o
echo "--- shopt"; shopt -p
echo "--- variable names"; compgen -v | grep -v -E '^(BASH_|RANDOM|SECONDS|EPOCH|LINENO|PIPESTATUS|PPID|BASHPID|OLDPWD|COLUMNS|LINES|SRANDOM|HISTCMD|FUNCNAME|DIRSTACK|GROUPS|SHLVL|_)' | sort
echo "--- prompts"; declare -p PS0 PS1 PS2 PS4 PROMPT_COMMAND PROMPT_DIRTRIM IFS 2>&1
echo "--- env"; env -0 | tr '\0' '\n' | sort
echo "--- aliases"; alias
echo "--- functions"; declare -F
echo "--- traps"; trap -p
echo "--- umask"; umask
echo "--- fds"; for f in /proc/$$/fd/*; do case "$(readlink "$f")" in /dev/pts/*) echo "$(basename $f) -> pts";; *) echo "$(basename $f) -> $(readlink "$f")";; esac; done | grep -v '\.zwc$' | sed 's/^[0-9]* -> //' | sort
echo "--- proc status"; grep -E '^(SigBlk|SigIgn|SigCgt|NoNewPrivs|Seccomp|CapEff|CapBnd|Uid|Gid|Groups)' /proc/$$/status
echo "--- limits"; cat /proc/$$/limits
echo "--- nice/oom"; ps -o ni= -p $$; cat /proc/$$/oom_score_adj
echo "--- stty"; stty -a
echo "--- tty"; [ "$(tty)" != "not a tty" ] && echo tty-ok; ps -o pgid=,tpgid=,sid=,stat= -p $$ | awk -v me=$$ '{print "leader=" ($1==me) " fg=" ($1==$2) " sess=" ($3==me) " stat=" $4}'
echo "--- hist"; history | tail -4 | cut -c8-
echo "--- cwd/jobs"; pwd; jobs
echo "--- size"; stty size; echo "$COLUMNS $LINES"
} > "$OBS/probe.$1" 2>&1
'''

PROBE_ZSH = r'''
{
echo "--- flags: $-"
echo "--- options"; setopt
echo "--- parameters"; print -l ${(ko)parameters} | grep -v -E '^(RANDOM|SECONDS|EPOCHSECONDS|EPOCHREALTIME|LINENO|OLDPWD|COLUMNS|LINES|SHLVL|_|ZSH_|funcstack|functrace|funcfiletrace|funcsourcetrace|PPID|TTYIDLE|HISTCMD|SRANDOM|status|pipestatus|argv)$'
echo "--- prompts"; typeset -p PS1 PS2 PS3 PS4 PROMPT PROMPT2 RPROMPT precmd_functions preexec_functions chpwd_functions zshexit_functions 2>&1
echo "--- env"; env -0 | tr '\0' '\n' | sort
echo "--- aliases"; alias
echo "--- functions"; print -l ${(ko)functions} | grep -v -E '^(_|\+)' | sort
echo "--- hooks"; add-zsh-hook -L 2>&1 | sort
echo "--- traps"; trap
echo "--- umask"; umask
echo "--- fds"; for f in /proc/$$/fd/*; do case "$(readlink "$f")" in /dev/pts/*) echo "$(basename $f) -> pts";; *) echo "$(basename $f) -> $(readlink "$f")";; esac; done | grep -v '\.zwc$' | sed 's/^[0-9]* -> //' | sort
echo "--- proc status"; grep -E '^(SigBlk|SigIgn|SigCgt|NoNewPrivs|Seccomp|CapEff|CapBnd|Uid|Gid|Groups)' /proc/$$/status
echo "--- limits"; cat /proc/$$/limits
echo "--- stty"; stty -a
echo "--- tty"; ps -o pgid=,tpgid=,sid=,stat= -p $$ | awk -v me=$$ '{print "leader=" ($1==me) " fg=" ($1==$2) " sess=" ($3==me) " stat=" $4}'
echo "--- hist"; fc -l -4 | cut -c8-
echo "--- cwd/jobs"; pwd; jobs
echo "--- size"; stty size; echo "$COLUMNS $LINES"
} > "$OBS/probe.$1" 2>&1
'''

PROBE_FISH = r'''
begin
echo "--- status"; status is-interactive; and echo interactive; status is-login; and echo login
echo "--- variables"; set -n | grep -v -E '^(_|history|fish_pid|last_pid|status|SHLVL|PWD|OLDPWD|COLUMNS|LINES|pipestatus|CMD_DURATION|fish_kill_signal)$'
echo "--- env"; env | sort
echo "--- functions"; functions -n | string split ', ' | sort
echo "--- abbr"; abbr
echo "--- fds"; for f in /proc/$fish_pid/fd/*; set -l t (readlink $f); if string match -q '/dev/pts/*' -- $t; echo (basename $f) "-> pts"; else; echo (basename $f) "->" $t; end; end | grep -v '\.zwc$' | sed 's/^[0-9]* -> //' | sort
echo "--- proc status"; grep -E '^(SigBlk|SigIgn|SigCgt|NoNewPrivs|Seccomp|CapEff|CapBnd|Uid|Gid|Groups)' /proc/$fish_pid/status
echo "--- limits"; cat /proc/$fish_pid/limits
echo "--- stty"; stty -a
echo "--- tty"; ps -o pgid=,tpgid=,sid=,stat= -p $fish_pid | awk -v me=$fish_pid '{print "leader=" ($1==me) " fg=" ($1==$2) " sess=" ($3==me) " stat=" $4}'
echo "--- hist"; history | head -4
echo "--- cwd/jobs"; pwd; jobs
echo "--- size"; stty size
end > $OBS/probe.$argv[1] 2>&1
'''



def portable(probe, fish=False):
    """The probes read /proc, which only Linux has. Elsewhere the same facts come from ps, ulimit and /dev/fd, and what a system does
    not tell (the signal masks, the session id) is left out: the comparison is still bare against recorded, line by line."""
    if IS_LINUX:
        return probe
    out = []
    pid = "$fish_pid" if fish else "$$"
    for line in probe.splitlines():
        if 'echo "--- fds"' in line:
            line = 'echo "--- fds"; ls /dev/fd | sort -n | sed \'s/^[0-9]*$/fd/\''
        elif "/proc/" in line and "--- limits" in line:
            line = 'echo "--- limits"; ulimit -a'
        elif "/proc/" in line:
            continue
        elif "env -0" in line:
            line = line.replace("env -0 | tr '\\0' '\\n' | sort", "env | sort")
        elif "ps -o pgid=,tpgid=,sid=,stat=" in line:
            line = line.replace("ps -o pgid=,tpgid=,sid=,stat=", "ps -o pgid=,tpgid=,stat=").replace(
                '" sess=" ($3==me) " stat=" $4', '" stat=" $3')
        out.append(line)
    return "\n".join(out) + "\n"


PROBE_BASH, PROBE_ZSH, PROBE_FISH = portable(PROBE_BASH), portable(PROBE_ZSH), portable(PROBE_FISH, fish=True)

# ------------------------------------------------------------------------------------------------ steps
# A step: (label, [(delay_before, bytes), ...]); when the parts are done the harness types a sync line
# that prints a unique marker, and waits for it. `nosync` steps hold their own end.

CTRL = {"C": b"\x03", "D": b"\x04", "Z": b"\x1a", "BS": b"\x1c", "A": b"\x01", "E": b"\x05", "K": b"\x0b",
        "U": b"\x15", "W": b"\x17", "Y": b"\x19", "L": b"\x0c", "R": b"\x12", "V": b"\x16", "T": b"\x14"}
UP, DOWN, RIGHT, LEFT = b"\x1b[A", b"\x1b[B", b"\x1b[C", b"\x1b[D"

WORDS = ["alpha", "beta", "gamma", "délta", "日本語", "x y", "it's", 'say "hi"', "$HOME", "a;b", "a&b", "a|b", "`id`", "$(echo sub)",
         "*", "?", "~", "{1,2}", "\\", "tab\there", "-n", "--", "!!", "!$", "#c", "%", "=", "\u00e9\u0301", "🙂", "a\\ b"]
FMT = ["%s\\n", "%5d|%-5d|\\n", "%x\\n", "\\e[31mred\\e[0m\\n", "%b\\n", "\\u00e9\\n", "\\0\\n", "%q\\n", "%.3s\\n"]


def q(s):
    return "'" + s.replace("'", "'\\''") + "'"


def line(*cmds):
    return ("\n".join(cmds) + "\n").encode()


def gen_steps(rng, shell, n):
    """The session: a list of steps."""
    fish = shell == "fish"
    zsh = shell == "zsh"
    S = []

    def add(label, *parts, nosync=False):
        S.append((label, list(parts), nosync))

    def typed(label, text, delay=0.0):
        add(label, (delay, text if isinstance(text, bytes) else text.encode()))

    def w():
        return rng.choice(WORDS)

    gens = []

    def g(weight):
        def deco(f):
            gens.append((weight, f))
            return f
        return deco

    @g(8)
    def echo():
        typed("echo", f"echo {q(w())} {q(w())}\n")

    @g(4)
    def echo_dq():
        typed("echo-dq", 'echo "' + rng.choice(["$HOME", "$PWD", "$?", "$$x", "a b", "\\$", "$(echo in)"]) + '" done\n')

    @g(4)
    def printf():
        typed("printf", f"printf {q(rng.choice(FMT))} {q(w())} 66\n")

    @g(8)
    def status_chain():
        n_ = rng.randint(0, 255)
        first = rng.choice(["false", "true", f"(exit {n_})" if not fish else f"sh -c 'exit {n_}'", "ls /nonexistent_dir_x 2>/dev/null", "test -d /"])
        if fish:
            tail = rng.choice(["echo $status", "echo $status $pipestatus", "echo $status; echo $status"])
        elif zsh:
            tail = rng.choice(["echo $?", "echo $? $pipestatus", "echo $?; echo $?", "echo $? $_"])
        else:
            tail = rng.choice(["echo $?", "echo $? ${PIPESTATUS[@]}", "echo $?; echo $?", "echo $? $_", "echo $? $BASH_COMMAND"])
        add("status:" + first, (0, f"{first}\n".encode()), (0.05, f"{tail}\n".encode()))

    @g(5)
    def pipestatus():
        if fish:
            typed("pipestatus", "false | true | sh -c 'exit 3'; echo $pipestatus\n")
        elif zsh:
            typed("pipestatus", "false | true | (exit 3); echo $pipestatus\n")
        else:
            typed("pipestatus", "false | true | (exit 3); echo ${PIPESTATUS[@]}\n")

    @g(4)
    def underscore():
        if fish:
            return typed("noop", "echo ok\n")
        typed("underscore", "echo one two three; echo $_\n")
        add("underscore2", (0, b"echo x y\n"), (0, b"echo $_\n"))

    @g(3)
    def env_export():
        v = rng.choice(["FOO", "BAR_1", "TRECS_TEST", "LC_X"])
        if fish:
            typed("export", f"set -gx {v} {q(w())}; echo ${v}; env | grep ^{v}=\n")
        else:
            typed("export", f"export {v}={q(w())}; echo ${v}; env | grep ^{v}=\n")

    @g(3)
    def cd():
        typed("cd", "mkdir -p d1/d2 && cd d1/d2 && pwd && cd .. && pwd && cd - ; pwd\n")

    @g(2)
    def pushd():
        if fish:
            return typed("pushd", "pushd d1 >/dev/null; pwd; popd >/dev/null; pwd\n")
        typed("pushd", "mkdir -p d1; pushd d1 >/dev/null; pwd; popd >/dev/null; pwd\n")

    @g(3)
    def files():
        typed("files", "printf 'one\\ntwo\\n' > f1.txt; cat f1.txt; wc -l f1.txt; ls\n")

    @g(4)
    def multiline_if():
        if fish:
            add("if-multi", (0, b"if true\n"), (0.03, b"echo yes\n"), (0.03, b"else\n"), (0.03, b"echo no\n"), (0.03, b"end\n"))
        else:
            add("if-multi", (0, b"if true; then\n"), (0.03, b"echo yes\n"), (0.03, b"else\n"), (0.03, b"echo no\n"), (0.03, b"fi\n"))

    @g(4)
    def multiline_for():
        if fish:
            add("for-multi", (0, b"for i in 1 2 3\n"), (0.03, b"echo i=$i\n"), (0.03, b"end\n"))
        else:
            add("for-multi", (0, b"for i in 1 2 3; do\n"), (0.03, b"echo i=$i\n"), (0.03, b"done\n"))

    @g(3)
    def heredoc():
        if fish:
            return typed("noop", "echo heredoc-not-in-fish\n")
        add("heredoc", (0, b"cat <<EOF\n"), (0.03, b"line $HOME\n"), (0.03, b"line two\n"), (0.03, b"EOF\n"))

    @g(3)
    def quote_continue():
        add("open-quote", (0, b"echo 'first\n"), (0.03, b"second'\n"))

    @g(3)
    def backslash_continue():
        add("bs-continue", (0, b"echo a \\\n"), (0.03, b"b\n"))

    @g(3)
    def function_def():
        if fish:
            add("func", (0, b"function ff\n"), (0.03, b"echo in-ff $argv\n"), (0.03, b"end\n"), (0.03, b"ff 1 2\n"))
        else:
            add("func", (0, b"ff() {\n"), (0.03, b"echo in-ff $1 $?\n"), (0.03, b"}\n"), (0.03, b"false; ff 7\n"))

    @g(3)
    def alias_():
        if fish:
            typed("alias", "alias ll 'ls -l'; ll f1.txt 2>&1 | wc -l\n")
        else:
            typed("alias", "alias ll='ls -l'; ll f1.txt 2>&1 | wc -l\n")

    @g(4)
    def job_sleep_int():
        add("sleep-^C", (0, b"sleep 5\n"), (0.3, CTRL["C"]))

    @g(3)
    def job_stop_resume():
        if fish:
            add("^Z-fg", (0, b"sleep 5\n"), (0.3, CTRL["Z"]), (0.2, b"jobs -c\n"), (0.1, b"fg\n"), (0.3, CTRL["C"]))
        else:
            add("^Z-fg", (0, b"sleep 5\n"), (0.3, CTRL["Z"]), (0.2, b"jobs\n"), (0.1, b"fg\n"), (0.3, CTRL["C"]))

    @g(3)
    def job_bg():
        if fish:
            typed("bg", "sleep 0.3 &\njobs -c; wait; echo waited\n")
        else:
            typed("bg", "sleep 0.3 &\njobs; wait; echo waited\n")

    @g(2)
    def job_stop_bg():
        if fish:
            add("^Z-bg", (0, b"sleep 0.6\n"), (0.2, CTRL["Z"]), (0.2, b"bg\n"), (0.9, b"jobs -c\n"))
        else:
            add("^Z-bg", (0, b"sleep 0.6\n"), (0.2, CTRL["Z"]), (0.2, b"bg\n"), (0.9, b"jobs\n"))

    @g(2)
    def kill_job():
        if fish:
            typed("kill-job", "sleep 5 &\nkill %1; sleep 0.2; jobs -c\n")
        else:
            typed("kill-job", "sleep 5 &\nkill %1; sleep 0.2; jobs\n")

    @g(3)
    def quit_key():
        add("sleep-^\\", (0, b"sleep 5\n"), (0.3, CTRL["BS"]))

    @g(3)
    def cat_eof():
        add("cat-EOF", (0, b"cat\n"), (0.1, b"typed text\n"), (0.1, CTRL["D"]))

    @g(2)
    def read_int():
        if fish:
            return typed("noop", "echo x\n")
        add("read-^C", (0, b"read -r x\n"), (0.2, b"abc"), (0.1, CTRL["C"]))

    @g(2)
    def read_val():
        if fish:
            add("read", (0, b"read -l x\n"), (0.2, b"value\n"), (0.1, b"echo got:$x\n"))
        else:
            add("read", (0, b"read -r x\n"), (0.2, b"value\n"), (0.1, b"echo got:$x\n"))

    @g(2)
    def read_timeout():
        if fish:
            return typed("noop", "echo y\n")
        t = "read -t 0.1 x; echo rc=$?\n"
        typed("read-t", t)

    @g(3)
    def edit_keys():
        keys = rng.choice([
            [b"echo abc", CTRL["A"], b"X", CTRL["E"], b"Y\n"],
            [b"echo one two", CTRL["W"], b"three\n"],
            [b"echo discard", CTRL["U"], b"echo kept\n"],
            [b"echo a", LEFT, LEFT, b"Z", RIGHT, b"\n"],
            [b"echo hist\n", UP, b"\n"],
            [b"echo foo bar baz", CTRL["A"], CTRL["K"], b"echo cut\n"],
            [b"echo y", CTRL["A"], CTRL["K"], CTRL["Y"], CTRL["Y"], b"\n"],
            [b"echo one\n", b"echo two\n", UP, UP, b"\n"],
            [b"echo search-me\n", CTRL["R"], b"search", b"\n"],
            [b"echo alt\n", b"echo ", b"\x1b.", b"\n"],
            [b"echo ", CTRL["V"], CTRL["C"], b"\n"],
            [b"echo ", CTRL["V"], b"\t", b"x\n"],
            [b"echo clear", CTRL["L"], b"\n"],
            [b"echo bye", CTRL["C"]],
            [b"echo del", b"\x7f\x7f", b"\n"],
            [b"echo home", b"\x1b[H", b"A", b"\x1b[F", b"B\n"],
            [b"echo bp", b"\x1b[200~pasted\ttext\x1b[201~", b"\n"],
            [b"echo ", CTRL["T"], b"abc", CTRL["T"], b"\n"],
        ])
        add("edit", *[(0.02, k) for k in keys])

    @g(3)
    def tab_complete():
        typed("setup-files", "touch comp_aa comp_ab comp_b; mkdir -p compdir\n")
        add("tab1", (0, b"ls comp_a"), (0.2, b"\t"), (0.2, b"\t"), (0.3, CTRL["U"]))
        add("tab2", (0, b"ls comp"), (0.2, b"\t"), (0.3, CTRL["U"]))
        add("tab-cmd", (0, b"ech"), (0.2, b"\t"), (0.2, b"x\n"))

    @g(3)
    def ctrl_d_nonempty():
        add("^D-nonempty", (0, b"echo abc"), (0.1, CTRL["D"]), (0.1, b"\n"))

    @g(2)
    def ctrl_z_prompt():
        add("^Z-at-prompt", (0, CTRL["Z"]), (0.1, b"echo after\n"))

    @g(2)
    def ctrl_c_prompt():
        add("^C-at-prompt", (0, CTRL["C"]), (0.1, b"echo after\n"))

    @g(4)
    def raw_roundtrip():
        n_ = rng.choice([1, 17, 255, 1024, 4000, 20000])
        data = bytes(rng.getrandbits(8) for _ in range(n_))
        h = hashlib.sha256(data).hexdigest()
        add("raw-bytes:" + h[:8] + ":" + str(n_),
            (0, f"stty raw -echo; head -c {n_} > raw.bin; stty sane\n".encode()),
            (0.3, data), (0.3, b""), (0, f"sha256sum raw.bin | cut -c1-64; echo {h}\n".encode()))

    @g(2)
    def all_bytes_in():
        data = bytes(range(256)) * rng.choice([1, 4])
        h = hashlib.sha256(data).hexdigest()
        add("allbytes:" + h[:8],
            (0, f"stty raw -echo; head -c {len(data)} > all.bin; stty sane\n".encode()),
            (0.3, data), (0.3, b""), (0, f"sha256sum all.bin | cut -c1-64; echo {h}\n".encode()))

    @g(2)
    def all_bytes_out():
        typed("allbytes-out", "python3 -c 'import sys; sys.stdout.buffer.write(bytes(range(256))*3)' | od -An -tx1 | md5sum\n")
        typed("allbytes-out-raw", "stty -opost; python3 -c 'import sys; sys.stdout.buffer.write(bytes(range(32,127))*2+b\"\\n\")'; stty sane\n")

    @g(3)
    def utf8_split_in():
        text = "héllo wörld 日本語 🙂 ─│┌┐".encode()
        h = hashlib.sha256(text + b"\n").hexdigest()
        cut = rng.randint(1, len(text) - 1)
        add("utf8-split-in", (0, b"cat > u8.txt\n"), (0.1, text[:cut]), (0.15, text[cut:] + b"\n"), (0.1, CTRL["D"]),
            (0.1, f"sha256sum u8.txt | cut -c1-64; echo {h}\n".encode()))

    @g(3)
    def utf8_out():
        typed("utf8-out", "python3 -c 'print(\"─│日本語🙂é\"*3000)' | md5sum; python3 -c 'print(\"─│日本語🙂é\"*3000)' | wc -c\n")
        add("utf8-out-direct", (0, b"python3 -c 'print(\"-\"*30, \"\\u2500\\u2502\\U0001f642\"*300)' | tail -c 200\n"))

    @g(3)
    def big_out():
        n_ = rng.choice([1000, 20000, 100000])
        typed("seq", f"seq 1 {n_} | md5sum; seq 1 {n_} | tail -n 2\n")
        add("seq-direct", (0, f"seq 1 {rng.choice([300, 3000])}\n".encode()))

    @g(2)
    def huge_out():
        add("flood-direct", (0, b"head -c 1500000 /dev/zero | tr '\\0' 'a' | fold -w 100 | head -n 20000\n"))

    @g(2)
    def binary_out():
        add("binary-out", (0, b"head -c 200000 /dev/urandom | md5sum\n"))
        add("binary-out-direct", (0, b"python3 -c 'import random,sys; r=random.Random(5); sys.stdout.buffer.write(bytes(r.getrandbits(8) for _ in range(60000)))'\n"))

    @g(2)
    def big_in():
        size = rng.choice([50000, 400000])
        data = (b"x" * 99 + b"\n") * (size // 100)
        h = hashlib.sha256(data).hexdigest()
        add("big-paste", (0, b"cat > big.txt\n"), (0.2, data), (0.2, CTRL["D"]),
            (0.2, f"sha256sum big.txt | cut -c1-64; echo {h}\n".encode()))

    @g(3)
    def terminal_query():
        add("cpr", (0, b"stty raw -echo; printf '\\033[6n'; head -c 7 | od -An -c; stty sane\n"))
        add("da1", (0, b"stty raw -echo; printf '\\033[c'; head -c 7 | od -An -c; stty sane\n"))
        add("osc11", (0, b"stty raw -echo; printf '\\033]11;?\\033\\\\'; head -c 25 | od -An -c; stty sane\n"))

    @g(3)
    def escapes_out():
        esc = rng.choice([
            "printf '\\033[?1049h\\033[2J\\033[Halt screen\\033[?1049l'",
            "printf '\\033]0;a title\\007'",
            "printf '\\033]52;c;aGVsbG8=\\007'",
            "printf '\\033]8;;http://x.example\\033\\\\link\\033]8;;\\033\\\\'",
            "printf '\\033P1;1;q\"1;1;1;1#0;2;0;0;0#0!1~\\033\\\\'",
            "printf '\\033P+q544e\\033\\\\'",
            "printf '\\033[?2004h\\033[?2004l'",
            "printf '\\033[2J\\033[H\\033[31mred\\033[0m\\033[1;1H'",
            "printf '\\033[?25l\\033[?25h'",
            "printf '\\033]133;A\\007marked\\033]133;D;0\\007'",
            "printf '\\033c'",
            "printf '\\033[?1000h\\033[?1000l'",
            "printf '\\033]7;file:///tmp\\033\\\\'",
        ])
        typed("escapes", esc + "\n")

    @g(3)
    def resize():
        r, c = rng.choice([(24, 80), (50, 200), (10, 40), (40, 132)])
        add(f"resize-{r}x{c}", (0, f"RESIZE:{r}:{c}".encode()), (0.4, b"stty size; tput cols; tput lines\n"))
        add("resize-echo", (0, b"echo $COLUMNS x $LINES\n"))

    @g(2)
    def vim_edit():
        if rng.random() < 0.5:
            add("vim", (0, b"vim -u NONE -N -n vimfile.txt\n"), (0.8, b"ihello vim\x1b:wq\n"), (0.6, b"cat vimfile.txt\n"))
        else:
            add("less", (0, b"seq 1 200 > l.txt; less -R l.txt\n"), (0.5, b"G"), (0.2, b"q"))

    @g(2)
    def nested_shell():
        sh = rng.choice(["bash", "bash --norc", "sh", "zsh -f", "fish --no-config"] if not fish else ["bash", "fish --no-config", "sh"])
        add("nested:" + sh, (0, (sh + "\n").encode()), (0.8, b"echo in-nested $0\n"), (0.2, b"echo $?\n"), (0.2, b"exit\n"))

    @g(2)
    def nested_bash_rc():
        add("nested-bash-rc", (0, b"bash -i\n"), (0.8, b"echo nested-prompt-1\n"), (0.2, b"false\n"), (0.2, b"echo nested-prompt-2\n"), (0.2, b"exit\n"))

    @g(2)
    def sub_shell_cmd():
        typed("bash-c", "bash -c 'echo $PS1 | wc -c; echo ${PROMPT_COMMAND-unset}; env | grep -vc ^TRECS_RECORDING='; sh -c 'echo $0'\n")

    @g(2)
    def trap_signals():
        if fish:
            return typed("trap", "function onusr --on-signal USR1; echo got-usr1; end; kill -USR1 $fish_pid; sleep 0.1; echo after\n")
        typed("trap", "trap 'echo got-usr1' USR1; kill -USR1 $$; sleep 0.1; echo after; trap - USR1\n")

    @g(2)
    def self_int():
        typed("self-int", "kill -INT $$; echo survived\n" if not fish else "echo skip\n")

    @g(2)
    def history_():
        typed("history", "echo h1; echo h2; history | tail -n 3 | cut -c8-\n" if not fish else "echo h1; echo h2; history | head -n 3\n")

    @g(1)
    def history_bang():
        if fish:
            return typed("noop", "echo bang-skip\n")
        typed("hist-bang", "echo bang1\n")
        typed("hist-bang2", "echo !!\n")
        typed("hist-bang3", "echo !$ !bang\n")

    @g(2)
    def command_not_found():
        typed("cnf", "nonexistent_cmd_xyz; echo rc=$?\n" if not fish else "nonexistent_cmd_xyz; echo rc=$status\n")

    @g(2)
    def globbing():
        typed("glob", "touch g1 g2 g3; echo g*; echo g[12]; echo nomatch*x\n")

    @g(2)
    def substitution():
        typed("subst", "echo $(echo a; echo b) `echo c`\n" if not fish else "echo (echo a; echo b)\n")

    @g(2)
    def arith():
        typed("arith", "echo $((2+3*4)) $((7%3))\n" if not fish else "math 2+3*4\n")

    @g(2)
    def process_sub():
        typed("procsub", "diff <(echo a) <(echo a) && echo same\n" if not fish else "diff (echo a | psub) (echo a | psub) && echo same\n")

    @g(2)
    def stderr_mix():
        typed("stderr", "echo out; echo err >&2; echo out2; ls /nonexistent_zz\n")

    @g(2)
    def ulimit_():
        typed("ulimit", "ulimit -n; ulimit -c; umask\n")

    @g(2)
    def many_short():
        for i in range(rng.randint(3, 8)):
            typed("burst", f"echo burst{i}\n")

    @g(2)
    def typeahead():
        add("typeahead", (0, b"sleep 0.5\n"), (0.1, b"echo typed-during-sleep\n"))

    @g(2)
    def stty_changes():
        typed("stty", "stty -a | head -n 1 | cut -c1-30; stty -echo; echo hidden; stty echo; stty -isig; stty isig; stty sane\n")

    @g(1)
    def tty_name():
        ps_ids = ("ps -o pgid=,tpgid=,sid= -p $$ | awk -v me=$$ '{print ($1==me), ($1==$2), ($3==me)}'" if IS_LINUX  # macOS ps has no session id
                  else "ps -o pgid=,tpgid= -p $$ | awk -v me=$$ '{print ($1==me), ($1==$2)}'")
        typed("ttyname", f"tty | sed 's/[0-9]*$/N/'; {ps_ids}\n" if not fish else "tty | sed 's/[0-9]*$/N/'\n")

    @g(2)
    def procs_tree():
        typed("ancestors", "ps -o comm= -p $PPID | sed 's/.*/PARENT-OK/'; echo $TERM $LANG; echo ${SHLVL-}\n" if not fish else "echo $TERM $LANG\n")

    @g(1)
    def exec_self():
        sh = shell
        typed("exec-shell", f"exec {sh}\n")
        typed("after-exec", "echo after-exec\n")

    @g(1)
    def subshell_exit():
        typed("subshell-exit", "(exit 5); echo $?\n" if not fish else "sh -c 'exit 5'; echo $status\n")

    @g(1)
    def disown_():
        if fish:
            return typed("noop", "echo d\n")
        typed("disown", "sleep 0.3 & disown; jobs; sleep 0.4; echo done-disown\n")

    @g(1)
    def nohup_():
        typed("nohup", "nohup sleep 0.2 >/dev/null 2>&1 & sleep 0.4; echo nohup-ok\n" if not fish else "echo skip\n")

    @g(1)
    def setsid_():
        typed("setsid", "setsid sleep 0.2 & sleep 0.4; echo setsid-ok\n" if not fish else "echo skip\n")

    @g(1)
    def probe_step():
        add("PROBE", (0, b"PROBE"))

    weights = [wt for wt, _ in gens]
    fns = [f for _, f in gens]
    while len(S) < n:
        rng.choices(fns, weights)[0]()
    # probe near the middle and at the end
    S.insert(rng.randint(1, max(1, len(S) // 2)), ("PROBE", [(0, b"PROBE")], False))
    S.append(("PROBE", [(0, b"PROBE")], False))
    return S


ENDINGS = [
    ("exit", b"exit\n"), ("exit-N", None), ("ctrl-d", b"\x04"), ("ctrl-d-twice", b"\x04\x04"), ("kill-hup-self", None),
    ("exec-true", None), ("kill9-self", None), ("close-terminal", None), ("exit-with-bg-job", None),
    ("exit-stopped-job", None),
]


def ending(rng, shell):
    name, data = rng.choice(ENDINGS)
    fish = shell == "fish"
    if name == "exit-N":
        data = f"exit {rng.randint(0, 255)}\n".encode()
    elif name == "kill-hup-self":
        data = (b"kill -HUP $fish_pid\n" if fish else b"kill -HUP $$\n")
    elif name == "exec-true":
        data = b"exec true\n"
    elif name == "kill9-self":
        data = (b"kill -9 $fish_pid\n" if fish else b"kill -9 $$\n")
    elif name == "close-terminal":
        data = b""
    elif name == "exit-with-bg-job":
        data = b"sleep 30 &\nexit\n\nexit\n"
    elif name == "exit-stopped-job":
        data = b"sleep 30\n"
    return name, data


# ----------------------------------------------------------------------------------- normalisation / known

OSC133 = re.compile(rb"\x1b\]133;[^\x07\x1b]*(?:\x07|\x1b\\)")
BANNER = re.compile(rb"Recording in progress\. Type 'exit' or Ctrl\+D to end the recording\.\r*\n|recorder: recording [^\n]*\n")
TRAILER = re.compile(rb"\r?\nRecording (?:completed successfully|ended early)!?.*\Z", re.S)


def norm(b, root, run_tag):
    b = undouble_cr(b)  # a CR the OS's pty doubled under load (only where a control run, with no recorder, has seen it do so)
    b = OSC133.sub(b"", b)
    b = BANNER.sub(b"", b)
    b = b.replace(root.encode(), b"@R@")
    b = re.sub(rb"/dev/pts/\d+", b"/dev/pts/N", b)
    b = re.sub(rb"(\[\d+\][+-]?\s+)\d+", rb"\1PID", b)
    b = re.sub(rb"\[\d+\]\s+Done\s+", b"[N]  Done  ", b)
    return b


# Differences the recorder makes on purpose or cannot avoid. They are matched to be listed, not hidden:
# the report prints each one that was seen, so the inventory of what the recorder changes stays visible.
KNOWN_STATE = [
    (re.compile(r"^TRECS_RECORDING=1$"), "env TRECS_RECORDING=1 is exported (tells programs they are recorded)"),
    (re.compile(r"^ZDOTDIR=|^TRECS_USER_ZDOTDIR="), "zsh: ZDOTDIR / TRECS_USER_ZDOTDIR in the environment (restored in the shell itself)"),
    (re.compile(r"^TERM=xterm-256color$"), "TERM replaced for dumb/unset terminals"),
    (re.compile(r"PS0: not found|PROMPT_COMMAND: not found|no such variable: (precmd_functions|preexec_functions)|^(precmd_functions|preexec_functions|zshexit_functions|chpwd_functions)$"), "hook arrays / PS0 exist (the integration's precmd/preexec hooks, PS0)"),
    (re.compile(r"^add-zsh-hook$|command not found: add-zsh-hook|^_?add-zsh-hook"), "zsh: add-zsh-hook is autoloaded by the integration"),
    (re.compile(r"__trecs"), "integration functions / hooks are defined in the shell (names start with __trecs)"),
    (re.compile(r"133;[ABCD]"), "prompt variables carry OSC 133 marks"),
    (re.compile(r"^(declare -x|declare --|declare -a|declare -\w+) (PS0|PS1|PS2|PROMPT_COMMAND)=|^(PS0|PS1|PS2|PROMPT_COMMAND|PROMPT|PROMPT2)=|^typeset .*(PS1|PS2|PROMPT)"), "prompt variables changed"),
    (re.compile(r"^(PS0|PS1|PS2|PROMPT_COMMAND|TRECS_RECORDING|PROMPT|PROMPT2)$"), "variable name list"),
    (re.compile(r"^(export|typeset -x) (PS0|PS1|PS2|PROMPT|PROMPT2)="), "prompt variables changed"),  # an exported prompt, as the shell lists it
]


def known_state_line(line):
    for rx, why in KNOWN_STATE:
        if rx.search(line):
            return why
    return None


def mask_text(s, root):
    s = s.replace(root, "@R@")
    s = re.sub(r"/dev/pts/\d+", "/dev/pts/N", s)
    s = re.sub(r"(pipe|socket|anon_inode:\[?\w*):?\[\d+\]", r"\1:[N]", s)
    s = re.sub(r"/trecs-shell-\d+", "/trecs-shell-N", s)
    return s


# ------------------------------------------------------------------------------------------------ running

class Setup:
    pass


def env_for(shell, root):
    return {
        "PATH": tool_path(), "HOME": f"{root}/home", "TMPDIR": f"{root}/tmp", "TERM": "xterm-256color",
        "LANG": LOCALE, "LC_ALL": LOCALE, "USER": "root", "LOGNAME": "root", "SHELL": SHELLS[shell], "OBS": f"{root}/obs",
    }


def run_session(workdir, tag, shell, profile_lines, steps, end, mode, size=(30, 100), timeout=12.0):
    """mode: 'bare' or 'rec'. Returns a result dict."""
    root = os.path.join(workdir, tag)
    shutil.rmtree(root, ignore_errors=True)
    for d in ("home", "tmp", "obs", "cwd", "out"):
        os.makedirs(f"{root}/{d}")
    write_profile(shell, f"{root}/home", profile_lines)
    probe = {"bash": PROBE_BASH, "zsh": PROBE_ZSH, "fish": PROBE_FISH}[shell]
    ext = {"bash": "sh", "zsh": "zsh", "fish": "fish"}[shell]
    open(f"{root}/cwd/probe.{ext}", "w").write(probe)
    env = env_for(shell, root)
    shpath = SHELLS[shell]
    if mode == "bare":
        argv = [shpath]
    else:
        argv = [TRECS, "record", "-output", f"{root}/out", "-name", "s", "-cmd", shpath, "-no-upload", "-overwrite"]
    run = Run(argv, env, f"{root}/cwd", rows=size[0], cols=size[1])
    res = {"mode": mode, "segments": [], "hang": None, "root": root}
    try:
        if not run.expect(rb"P> |\$ |> ", 0, timeout):
            res["hang"] = "no prompt at start"
            res["raw"] = bytes(run.buf)
            return res
        run.settle(0.3, 2.0)
        pos = len(run.buf)
        res["startup"] = bytes(run.buf[:pos])
        nprobe = 0
        for i, (label, parts, nosync) in enumerate(steps):
            if label == "PROBE":
                nprobe += 1
                cmd = f". ./probe.{ext} {nprobe}\n" if shell != "fish" else f"source probe.fish {nprobe}\n"
                parts = [(0, cmd.encode())]
            for delay, data in parts:
                if delay:
                    t0 = time.time()
                    while time.time() - t0 < delay:
                        run.pump(0.02)
                if data.startswith(b"RESIZE:"):
                    _, r, c = data.split(b":")
                    run.resize(int(r), int(c))
                elif data:
                    run.send(data, chunk=None)
            if not nosync:
                # A signal key (^C, ^Z, ^backslash) is echoed by the terminal at once, and the shell answers it when it next runs: on a busy
                # machine that is later than a quarter of a second, and a line typed in the meantime is thrown away with the typeahead.
                signalled = any(c in d for _, d in parts for c in (b"\x03", b"\x1a", b"\x1c"))
                run.settle(0.8 if signalled else 0.25, 2.5 if signalled else 1.5)
                run.send(f"builtin printf 'ZZ%sZZ\\n' {i}\n".encode())
                m = run.expect(b"ZZ" + str(i).encode() + b"ZZ\r?\n", pos, timeout)
                if not m:
                    res["hang"] = f"step {i} ({label}) never returned to the prompt"
                    res["raw"] = bytes(run.buf)
                    res["segments"].append((label, bytes(run.buf[pos:])))
                    return res
                run.settle(0.05, 0.5)
                end_pos = len(run.buf)
            else:
                run.settle(0.2, 1.0)
                end_pos = len(run.buf)
            res["segments"].append((label, bytes(run.buf[pos:end_pos])))
            pos = end_pos
        # the end
        ename, edata = end
        res["ending"] = ename
        if ename == "close-terminal":
            run.close()
            time.sleep(0.2)
        elif ename == "exit-stopped-job":
            run.send(edata)
            run.settle(0.3, 1.0)
            run.send(b"\x1a")
            run.settle(0.3, 1.0)
            run.send(b"exit\n")
            run.settle(0.3, 1.0)
            run.send(b"exit\n")
        else:
            run.send(edata)
        rc = run.wait(8.0)
        if rc is None:
            res["hang"] = f"did not end after '{ename}'"
            run.kill()
        res["rc"] = rc
        res["tail"] = bytes(run.buf[pos:])
        res["termios_end"] = run.termios() if ename != "close-terminal" else None
        res["termios_start"] = run.pristine
        res["raw"] = bytes(run.buf)
    except TimeoutError as e:
        res["hang"] = f"{e} (after {len(res['segments'])} steps)"
        res["raw"] = bytes(run.buf)
        return res
    finally:
        res["leftover_early"] = None
        time.sleep(0.4)
        left = [x for x in processes_in(root)]
        res["leftover"] = sorted(set(c.split(" ")[0].split("/")[-1] for _, c in left))
        for pid, _ in left:
            try:
                os.kill(pid, 9)
            except OSError:
                pass
        run.kill()
        run.close()
    res["tmp"] = sorted(os.listdir(f"{root}/tmp"))
    res["files"] = {}
    for dirpath, _, fns in os.walk(f"{root}/cwd"):
        for fn in fns:
            p = os.path.join(dirpath, fn)
            rel = os.path.relpath(p, f"{root}/cwd")
            if rel.startswith("probe."):
                continue
            try:
                data = open(p, "rb").read()
                res["files"][rel] = hashlib.sha256(data).hexdigest()[:16] + f":{len(data)}"
            except OSError:
                pass
    res["obs"] = {}
    for fn in sorted(os.listdir(f"{root}/obs")):
        try:
            res["obs"][fn] = mask_text(open(f"{root}/obs/{fn}", errors="replace").read(), root)
        except OSError:
            pass
    return res


def termios_diff(a, b):
    if not isinstance(a, list) or not isinstance(b, list):
        return None
    out = []
    names = ["iflag", "oflag", "cflag", "lflag", "ispeed", "ospeed"]
    for i, n in enumerate(names):
        if a[i] != b[i]:
            out.append(f"{n}: {a[i]:#o} != {b[i]:#o}")
    if a[6][:16] != b[6][:16]:
        out.append("cc differs")
    return out


# ------------------------------------------------------------------------------------------- comparison

def compare(shell, A1, A2, R, steps, profile_names):
    """Returns (findings, noisy_count, known_seen). A finding is (severity, signature, detail)."""
    F, known = [], {}
    root_r, root_a = R["root"], A1["root"]

    for r, nm in ((A1, "A1"), (A2, "A2")):
        if r["hang"]:
            F.append(("note", "bare-hang", f"{nm}: {r['hang']}"))
    if A1["hang"] == "no prompt at start" and not profile_names:
        # A plain rc (the one every other seed starts from) that gives no prompt is the test setup that is broken - the shell is
        # not being tested at all - and not a quirk of a random profile (an rc that sets a prompt the pattern cannot see).
        F.append(("HIGH", "no-prompt-with-plain-rc",
                  f"{shell}: the bare shell printed no prompt in the first seconds; what it printed: {A1.get('raw', b'')[-300:]!r}"))
    if R["hang"] and not A1["hang"]:
        F.append(("HIGH", "hang-under-recorder", f"{R['hang']}; the end of what the recorded shell printed: {R.get('raw', b'')[-300:]!r}"))
        return F, 0, known
    if A1["hang"] or A2["hang"]:
        # no state was written by a bare run that did not finish, and with one bare run alone nothing says what is noise
        return F, 0, known

    noisy = 0
    s1, s2, sr = (norm(x.get("startup", b""), x["root"], "") for x in (A1, A2, R))
    if s1 == s2 and s1 != sr:
        F.append(("HIGH", "startup-output-differs", f"bare {s1[-250:]!r}\n rec  {sr[-250:]!r}"))
    n = min(len(A1["segments"]), len(R["segments"]))
    for i in range(n):
        label = A1["segments"][i][0]
        a1 = norm(A1["segments"][i][1], root_a, "A1")
        a2 = norm(A2["segments"][i][1], A2["root"], "A2") if i < len(A2["segments"]) else b""
        r = norm(R["segments"][i][1], root_r, "R")
        if a1 == a2:
            if a1 != r:
                k = next((j for j in range(min(len(a1), len(r))) if a1[j] != r[j]), min(len(a1), len(r)))
                F.append(("HIGH" if not label.startswith(("PROBE",)) else "info", f"output-differs:{label.split(':')[0]}",
                          f"step {i} {label} (first difference at byte {k} of {len(a1)}/{len(r)})\n bare: {a1[max(0, k-120):k+160]!r}\n rec : {r[max(0, k-120):k+160]!r}"))
        else:
            noisy += 1

    # shell-state probes
    for fn in sorted(set(A1["obs"]) | set(R["obs"])):
        a1, a2, r = A1["obs"].get(fn, ""), A2["obs"].get(fn, ""), R["obs"].get(fn, "")
        if fn.startswith("probe.") or fn in ("pc.log", "exitstatus"):
            if a1 != a2:
                # take only the lines that are stable between the two bare runs
                pass
            la1, la2, lr = a1.splitlines(), a2.splitlines(), r.splitlines()
            stable = set(la1) & set(la2)
            for ln in la1:
                if ln in stable and ln not in lr:
                    why = known_state_line(ln)
                    if why:
                        known[why] = known.get(why, 0) + 1
                    else:
                        F.append(("HIGH" if fn.startswith("probe.") else "MED", f"state-missing:{fn.split('.')[0]}", f"{fn}: only in bare: {ln[:200]}"))
            for ln in lr:
                if ln not in la1 and ln not in la2:
                    why = known_state_line(ln)
                    if why:
                        known[why] = known.get(why, 0) + 1
                    else:
                        F.append(("HIGH" if fn.startswith("probe.") else "MED", f"state-extra:{fn.split('.')[0]}", f"{fn}: only under recorder: {ln[:200]}"))
        else:
            if a1 == a2 and a1 != r:
                F.append(("MED", f"obs-differs:{fn}", f"{fn}: bare {a1[:100]!r} rec {r[:100]!r}"))

    # files the session produced
    if A1["files"] == A2["files"] and A1["files"] != R["files"]:
        keys = set(A1["files"]) | set(R["files"])
        d = [k for k in sorted(keys) if A1["files"].get(k) != R["files"].get(k)]
        F.append(("HIGH", "files-differ", f"{d[:5]}: bare {[A1['files'].get(k) for k in d[:3]]} rec {[R['files'].get(k) for k in d[:3]]}"))

    # absolute checks inside the output
    for i, (label, seg) in enumerate(R["segments"]):
        s = norm(seg, root_r, "R")
        if re.search(rb"__trecs|trecs-shell|133;[ABCD]", s) and not re.search(rb"__trecs|trecs-shell|133;[ABCD]", norm(A1["segments"][i][1], root_a, "A1") if i < len(A1["segments"]) else b""):
            if not label.startswith("PROBE") and not label.startswith("escapes"):
                F.append(("HIGH", "integration-text-leaked", f"step {i} {label}: {s[-200:]!r}"))
    for i, (label, seg) in enumerate(R["segments"]):
        m = re.match(r"(raw-bytes|allbytes|utf8-split-in|big-paste)", label)
        if m:
            s = norm(seg, root_r, "R")
            hashes = re.findall(rb"\b([0-9a-f]{64})\b", s)
            if len(hashes) >= 2 and hashes[-1] != hashes[-2]:
                F.append(("HIGH", "input-corrupted", f"step {i} {label}: stored {hashes[-2][:12]!r} sent {hashes[-1][:12]!r}"))

    # after the session
    if A1.get("hang") is None and R.get("hang"):
        F.append(("HIGH", "does-not-end", f"{R['hang']}"))
    elif R.get("ending") and A1.get("rc") is not None:
        pass
    if (A1.get("leftover") == A2.get("leftover")):
        extra = [p for p in R.get("leftover", []) if p not in A1.get("leftover", []) and p not in ("trecs",)]
        if R.get("leftover") and not A1.get("leftover") or extra:
            F.append(("HIGH", "leftover-processes", f"bare {A1.get('leftover')}, rec {R.get('leftover')}"))
    if [t for t in R.get("tmp", []) if t.startswith("trecs")]:
        F.append(("MED", "tmpdir-left-behind", f"{R['tmp']}"))
    if R.get("termios_end") is not None and isinstance(R["termios_end"], list):
        d = termios_diff(R["termios_end"], R["termios_start"])
        db = termios_diff(A1.get("termios_end"), A1.get("termios_start")) if A1.get("termios_end") else []
        if d and not db:
            F.append(("HIGH", "terminal-left-changed", "; ".join(d)))
    ea, er = A1["obs"].get("exitstatus"), R["obs"].get("exitstatus")
    if A1["obs"].get("exitstatus") == A2["obs"].get("exitstatus") and ea != er:
        F.append(("HIGH", "shell-exit-status-differs", f"bare {ea!r} rec {er!r}"))
    return F, noisy, known


def one(args):
    seed, shell, nsteps, workdir, keep = args
    rng = random.Random(seed)
    pnames, plines = make_profile(rng, shell)
    steps = gen_steps(rng, shell, nsteps)
    end = ending(rng, shell)
    tag = f"{shell}-{seed}"
    t0 = time.time()
    try:
        A1 = run_session(workdir, tag + "-a1", shell, plines, steps, end, "bare")
        A2 = run_session(workdir, tag + "-a2", shell, plines, steps, end, "bare")
        R = run_session(workdir, tag + "-r1", shell, plines, steps, end, "rec")
        F, noisy, known = compare(shell, A1, A2, R, steps, pnames)
    except Exception:
        return {"seed": seed, "shell": shell, "error": traceback.format_exc(), "findings": [], "profile": pnames}
    out = {"seed": seed, "shell": shell, "profile": pnames, "findings": F, "noisy": noisy, "known": known,
           "steps": [s[0] for s in steps], "ending": end[0], "secs": round(time.time() - t0, 1),
           "nsteps": len(steps), "bare_hang": A1["hang"]}
    if F and any(f[0] in ("HIGH", "MED") for f in F):
        d = os.path.join(workdir, "fail", tag)
        os.makedirs(d, exist_ok=True)
        for nm, r in (("a1", A1), ("a2", A2), ("r1", R)):
            open(f"{d}/{nm}.raw", "wb").write(r.get("raw", b""))
        json.dump({"seed": seed, "shell": shell, "profile": pnames, "profile_lines": plines, "steps": [s[0] for s in steps], "ending": end[0]},
                  open(f"{d}/case.json", "w"), indent=1)
    if not keep:
        for suf in ("-a1", "-a2", "-r1"):
            shutil.rmtree(os.path.join(workdir, tag + suf), ignore_errors=True)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seeds", default="1-30", help="N-M")
    ap.add_argument("--shells", default="bash,zsh,fish")
    ap.add_argument("--steps", type=int, default=18)
    ap.add_argument("--jobs", type=int, default=6)
    ap.add_argument("--work", default=os.path.join(HERE, "work"))
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--json", default=None)
    ap.add_argument("--strict", action="store_true", help="the exit status is 1 for any HIGH or MED finding, not only the gated kinds")
    a = ap.parse_args()
    a.work = os.path.abspath(a.work)  # the sessions run in other directories: HOME and the like must not be relative
    lo, hi = map(int, a.seeds.split("-"))
    preflight(a.shells.split(","), [TRECS])
    os.makedirs(a.work, exist_ok=True)
    jobs = [(s, sh, a.steps, a.work, a.keep) for s in range(lo, hi + 1) for sh in a.shells.split(",")]
    results = []
    t0 = time.time()
    # measured here, once, so that the workers (forked below) have it: see ptyrun.pty_cr_doubling
    print(f"control: a bare pty with a slow reader doubled {pty_cr_doubling()} CRs", flush=True)
    with cf.ProcessPoolExecutor(a.jobs, mp_context=mp.get_context('fork')) as ex:
        for r in ex.map(one, jobs):
            results.append(r)
            mark = "ERR" if r.get("error") else ("FAIL" if any(f[0] in ("HIGH", "MED") for f in r["findings"]) else "ok")
            print(f"{r['shell']:5} seed {r['seed']:4} {mark:4} {r.get('secs', '')}s profile={r['profile']}", flush=True)
            if mark == "ERR":
                print(r["error"])
    if a.json:
        json.dump(results, open(a.json, "w"), default=lambda o: o.decode("latin1") if isinstance(o, bytes) else str(o))
    report(results, time.time() - t0)
    bad = gate(results, a.strict)
    print(f"\n==== gate ({'strict: any HIGH or MED' if a.strict else 'the kinds that change what the shell does'}): "
          f"{'FAILED' if bad else 'passed'} ====")
    for line in bad:
        print("  " + line)
    sys.exit(1 if bad else 0)


# What changes what the shell or a program in it DOES - not only what is printed. These fail the run (exit status 1). The other
# differences (the bytes the terminal gets, the state of the shell, the observation files) are listed in the report but do not, unless
# --strict: a few of them are known and cosmetic (bash prints four call lines under `set -x`, fish repaints its line editor in an
# order of its own, zsh counts the marks in a prompt it cannot mark as zero-width), and the Go tests hold the rest in place.
GATED = {
    "hang-under-recorder", "startup-output-differs", "files-differ", "integration-text-leaked", "input-corrupted", "does-not-end",
    "leftover-processes", "terminal-left-changed", "shell-exit-status-differs", "no-prompt-with-plain-rc",
}


def gate(results, strict):
    bad = []
    for r in results:
        who = f"{r['shell']} seed {r['seed']}"
        if r.get("error"):
            bad.append(f"{who}: harness error")
        for sev, sig, detail in r["findings"]:
            if sig in ("integration-text-leaked", "startup-output-differs") and r["shell"] == "bash" and "set_x" in (r.get("profile") or []) and not strict:
                continue  # the four call lines bash prints for the prompt functions under `set -x`: known, and not fixable from the shell
            if sig == "startup-output-differs" and r["shell"] == "zsh" and {"no_prompt_percent", "right_prompt"} <= set(r.get("profile") or []) and not strict:
                continue  # the right prompt is placed by a width that counts the marks, in a prompt zsh is told not to expand: known
            if sig in GATED or (strict and sev in ("HIGH", "MED")):
                bad.append(f"{who}: [{sev}] {sig} - {detail.splitlines()[0][:140]}")
    return bad


def report(results, secs):
    from collections import defaultdict
    sigs = defaultdict(list)
    known = defaultdict(int)
    noisy = total_steps = 0
    for r in results:
        for k, v in r.get("known", {}).items():
            known[k] += v
        noisy += r.get("noisy", 0)
        total_steps += r.get("nsteps", 0)
        for sev, sig, detail in r["findings"]:
            sigs[(sev, sig, r["shell"])].append((r["seed"], r["profile"], detail))
    print(f"\n==== {len(results)} sessions, {total_steps} steps, {noisy} noisy step comparisons skipped, {secs:.0f}s ====")
    errs = [r for r in results if r.get("error")]
    print(f"harness errors: {len(errs)}")
    print("\n-- findings (grouped) --")
    order = {"HIGH": 0, "MED": 1, "info": 2, "note": 3}
    for (sev, sig, shell), lst in sorted(sigs.items(), key=lambda kv: (order.get(kv[0][0], 9), kv[0][1])):
        seeds = sorted(set(s for s, _, _ in lst))
        profs = defaultdict(int)
        for _, p, _ in lst:
            for x in (p or ["(plain rc)"]):
                profs[x] += 1
        print(f"[{sev}] {sig} ({shell}) x{len(lst)} seeds={seeds[:8]}{'...' if len(seeds) > 8 else ''} profiles={dict(sorted(profs.items(), key=lambda kv: -kv[1])[:4])}")
        print("     e.g.", lst[0][2].replace("\n", "\n     "))
    print("\n-- known, deliberate differences seen (inventory) --")
    for k, v in sorted(known.items(), key=lambda kv: -kv[1]):
        print(f"  {v:5}  {k}")


if __name__ == "__main__":
    main()
