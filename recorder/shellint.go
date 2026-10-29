package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Shell integration: the recorder starts the shell so that it says, in the output
// itself, where each prompt, each command's input and each command's output begin
// and end (OSC 133 "prompt marks" - the convention kitty, iTerm2, WezTerm and VS
// Code use). A player or editor then needs no guessing from how the prompt looks,
// whatever PS1 is. The shell's own startup files run first, so the user's prompt
// and aliases are all there; the marks are wrapped around the prompt they set.
//
//	ESC ] 133 ; A        a prompt starts (A;k=s: a continuation prompt - PS2)
//	ESC ] 133 ; B        the prompt ends; what follows is what the user types
//	ESC ] 133 ; C        Enter was pressed and the command starts executing
//	ESC ] 133 ; D ; N    the command finished with status N
//
// Each script skips itself if the prompt already carries marks (a shell run from
// VS Code, a prompt framework that emits them): doubled marks would be misread.

const bashIntegration = `
# --- trecs shell integration (prompt marks, OSC 133) ---
# This runs at every prompt of a shell that is the person's own, so it must change nothing they can see or have set:
#  - aliases are off while it is read, as a function body is expanded when it is read (an alias for printf would be in it),
#    and what it runs is "builtin": a function of theirs called local or printf is theirs, and not what this means;
#  - the functions say nothing under set -x (the group's stderr is dropped) and leave the options as they were (local -);
#  - they are called as "f && :": a command that fails there is not the last of its list, so set -e does not end the shell over
#    the status of the command before it (false && true leaves 1), an ERR trap does not fire for it, and $? is still that status;
#  - a PROMPT_COMMAND that is readonly (an audit setup) cannot be wrapped: then nothing is done, and the shell is left alone.
{ __trecs_xt=$-; \set +x; } 2>/dev/null   # nor does loading it show under set -x
__trecs_alias=$(\shopt -p expand_aliases || :); \shopt -u expand_aliases
__trecs_pc=$(builtin declare -p PROMPT_COMMAND 2>/dev/null || :); __trecs_pc=${__trecs_pc#declare }; __trecs_pc=${__trecs_pc%% *}
if { [ "${BASH_VERSINFO[0]}" -gt 4 ] || { [ "${BASH_VERSINFO[0]}" -eq 4 ] && [ "${BASH_VERSINFO[1]}" -ge 4 ]; }; } && [[ $__trecs_pc != -*r* ]]; then
  case ${PS1-} in
  *'133;A'*) ;;
  *)
    # $? first: D reports the status of the command that just ran and hands it on.
    __trecs_pre() { { builtin local - s=$?; builtin set +eux; builtin printf '\e]133;D;%s\a' "$s"; return "$s"; } 2>/dev/null; }
    # Last, so a prompt a user's PROMPT_COMMAND rebuilds each time still gets wrapped.
    __trecs_post() {
      { builtin local - s=$?; builtin set +eux
        case ${PS1-} in *'133;A'*) ;; *) PS1='\[\e]133;A\a\]'"${PS1-}"'\[\e]133;B\a\]' ;; esac
        case ${PS2-} in *'133;A'*) ;; *) PS2='\[\e]133;A;k=s\a\]'"${PS2-}"'\[\e]133;B\a\]' ;; esac
        return "$s"
      } 2>/dev/null
    }
    case ${PS0-} in *'133;C'*) ;; *) PS0='\e]133;C\a'"${PS0-}" ;; esac
    if [[ $(builtin declare -p PROMPT_COMMAND 2>/dev/null || :) == "declare -a"* ]]; then
      PROMPT_COMMAND=('__trecs_pre && :' "${PROMPT_COMMAND[@]}" '__trecs_post && :')
    else
      PROMPT_COMMAND="__trecs_pre && :${PROMPT_COMMAND:+;$PROMPT_COMMAND};__trecs_post && :"
    fi
    ;;
  esac
fi
# One line: the next one would be read with the person's aliases back on, and an alias for case would be in it.
builtin eval "$__trecs_alias"; builtin unset __trecs_alias __trecs_pc; case $__trecs_xt in *x*) builtin unset __trecs_xt; builtin set -x ;; *) builtin unset __trecs_xt ;; esac
`

const zshIntegration = `
# --- trecs shell integration (prompt marks, OSC 133) ---
# Run at every prompt, so it changes nothing the person has set: emulate -L gives the functions zsh's own options (whatever
# they have set - errexit, nounset, ksh arrays, no prompt_percent... - does not reach them) and puts theirs back after.
{ __trecs_xt=0; [[ -o xtrace ]] && __trecs_xt=1; unsetopt xtrace } 2>/dev/null   # nor does loading it show under setopt xtrace
if [[ $PS1 != *'133;A'* ]]; then
  builtin autoload -Uz add-zsh-hook
  __trecs_precmd() {
    { # (the group's stderr is dropped: nothing of this shows under setopt xtrace)
      local s=$? pp=1
      [[ -o prompt_percent ]] || pp=0   # with it off, %{ %} would be printed as they are: the marks go in bare
      emulate -L zsh
      setopt no_xtrace
      builtin print -rn -- $'\e]133;D;'"$s"$'\a'
      # Each prompt, as a theme that rebuilds PS1 on every precmd would have it.
      local o=$'%{' c=$'%}'
      (( pp )) || { o=; c=; }
      [[ $PS1 == *'133;A'* ]] || PS1=${o}$'\e]133;A\a'${c}"$PS1"${o}$'\e]133;B\a'${c}
      [[ $PS2 == *'133;A'* ]] || PS2=${o}$'\e]133;A;k=s\a'${c}"$PS2"${o}$'\e]133;B\a'${c}
      return $s
    } 2>/dev/null
  }
  __trecs_preexec() { { emulate -L zsh; setopt no_xtrace; builtin print -rn -- $'\e]133;C\a'; } 2>/dev/null }
  add-zsh-hook precmd __trecs_precmd
  add-zsh-hook preexec __trecs_preexec
fi
if (( __trecs_xt )); then unset __trecs_xt; setopt xtrace; else unset __trecs_xt; fi
`

// fishDefaultPrompt: a person who has erased fish_prompt (functions -e) still gets fish's own prompt, as fish does not look for the function
// again, so the wrapper must not take the erased one for no prompt at all: it loads the file the prompt comes from.
const fishDefaultPrompt = `    if not builtin functions -q fish_prompt; and test -r "$__fish_data_dir/functions/fish_prompt.fish"
        builtin source "$__fish_data_dir/functions/fish_prompt.fish"
    end`

const fishIntegration = `
# --- trecs shell integration (prompt marks, OSC 133) ---
# "builtin" throughout: a function of the person's called printf or string is theirs, and not what this means.
if not set -q __trecs_marks
    set -g __trecs_marks 1
` + fishDefaultPrompt + `
    # The prompt is run FIRST, and its output is what the marks are wrapped around: anything run before it (even a printf)
    # would reset $status, and a prompt that shows the last command's status would show a success.
    if builtin functions -q fish_prompt
        builtin functions -c fish_prompt __trecs_orig_prompt
        function fish_prompt
            # collected into one string: a prompt of several lines is several items otherwise, and printf would use its format for each
            set -l __trecs_p (__trecs_orig_prompt | builtin string collect)
            builtin printf '\e]133;A\a%s\e]133;B\a' "$__trecs_p"
        end
    else
        function fish_prompt
            builtin printf '\e]133;A\a\e]133;B\a'
        end
    end
    function __trecs_preexec --on-event fish_preexec
        builtin printf '\e]133;C\a'
    end
    function __trecs_postexec --on-event fish_postexec
        builtin printf '\e]133;D;%s\a' $status
    end
end
`

// fish 4 marks where a prompt starts (A), where a command starts (C, with the
// command line itself) and where it ends (D) on its own - doubling those made
// the marks unreadable. What it leaves out is where the prompt ENDS (B), which is
// what separates the prompt from what is typed after it; that is all this adds.
const fishNativeIntegration = `
# --- trecs shell integration: the end of the prompt (OSC 133;B); fish 4 marks the rest ---
if not set -q __trecs_marks
    set -g __trecs_marks 1
` + fishDefaultPrompt + `
    builtin functions -q fish_prompt; and builtin functions -c fish_prompt __trecs_orig_prompt
    function fish_prompt
        builtin functions -q __trecs_orig_prompt; and __trecs_orig_prompt
        builtin printf '\e]133;B\a'
    end
end
`

// fishQuote makes a path fit between single quotes in fish, in which a backslash and a quote are the two characters that are escaped:
// a temporary directory with either in its name (it is under $TMPDIR) must not end the quote.
func fishQuote(p string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(p)
}

var fishVersionRe = regexp.MustCompile(`version (\d+)\.`)

// fishMajorVersion is the major version in "fish, version 4.1.0"; 0 if it can't be read.
func fishMajorVersion(versionOutput string) int {
	if m := fishVersionRe.FindStringSubmatch(versionOutput); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// bashSupportsMarks reports whether the bash at shell is new enough for the integration (4.4: PS0 is what marks the start of
// a command's output). macOS ships bash 3.2, and the integration skips itself there - which, without this check, left the
// recorder believing the shell was marked and waiting for a prompt mark that never came. A bash that can't be asked is
// treated as a new one.
func bashSupportsMarks(shell string) bool {
	out, err := exec.Command(shell, "-c", `echo "${BASH_VERSINFO[0]} ${BASH_VERSINFO[1]}"`).Output()
	if err != nil {
		return true
	}
	var major, minor int
	if _, err := fmt.Sscan(string(out), &major, &minor); err != nil {
		return true
	}
	return major > 4 || (major == 4 && minor >= 4)
}

// shellIntegration is what it takes to start a shell with prompt marks.
type shellIntegration struct {
	args    []string
	env     []string
	cleanup func()
}

// setupShellIntegration prepares the startup files for the given shell program.
// It returns nil (and no error) for a shell it has no integration for.
func setupShellIntegration(shell string) (*shellIntegration, error) {
	return setupShellIntegrationFor(shell, false)
}

// The commands of a script are what a script would run, and not what a person would type: a person's shell expands !word to a
// command from its history (so `echo !rm` would run the last rm again), and a script's does not. So a scripted session turns it off.
const (
	bashScripted = "\nset +H # trecs: the lines are a script's, not typed by a person: no history expansion\n"
	zshScripted  = "\nsetopt NO_BANG_HIST # trecs: the lines are a script's, not typed by a person: no history expansion\n"
)

func scriptedOnly(scripted bool, s string) string {
	if scripted {
		return s
	}
	return ""
}

// setupShellIntegrationFor is setupShellIntegration for a shell that is to be typed a script (scripted) or a person's keystrokes.
func setupShellIntegrationFor(shell string, scripted bool) (*shellIntegration, error) {
	kind := filepath.Base(shell)
	dir, err := os.MkdirTemp("", "trecs-shell-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	write := func(name, content string) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, []byte(content), 0o600)
	}
	env := []string{"TRECS_RECORDING=1"}

	switch kind {
	case "bash":
		if !bashSupportsMarks(shell) {
			cleanup()
			return nil, nil
		}
		rc, err := write("bashrc", "[ -r /etc/bash.bashrc ] && . /etc/bash.bashrc\n[ -r ~/.bashrc ] && . ~/.bashrc\n"+bashIntegration+scriptedOnly(scripted, bashScripted))
		if err != nil {
			cleanup()
			return nil, err
		}
		return &shellIntegration{args: []string{"--rcfile", rc}, env: env, cleanup: cleanup}, nil

	case "zsh":
		// zsh reads its startup files from $ZDOTDIR: point it at ours, which load the user's own from where they were.
		// The user's files must not see the detour, though - $ZDOTDIR is where compinit keeps its dump, where a framework
		// keeps its cache, and a script may print it - so it is the user's own value while their files run: ours restore it
		// before sourcing theirs. (A .zshenv that moves $ZDOTDIR itself is followed: its .zshrc is the one loaded.)
		orig, wasSet := os.LookupEnv("ZDOTDIR")
		if !wasSet {
			orig = ""
		}
		const restore = `if [[ -n $TRECS_USER_ZDOTDIR ]]; then ZDOTDIR=$TRECS_USER_ZDOTDIR; else unset ZDOTDIR; fi` + "\n"
		if _, err := write(".zshenv", "__trecs_zdotdir=$ZDOTDIR\n"+restore+
			`[ -r "${ZDOTDIR:-$HOME}/.zshenv" ] && . "${ZDOTDIR:-$HOME}/.zshenv"`+"\n"+
			"export TRECS_USER_ZDOTDIR=${ZDOTDIR-}\n"+
			"ZDOTDIR=$__trecs_zdotdir; unset __trecs_zdotdir\n"); err != nil {
			cleanup()
			return nil, err
		}
		if _, err := write(".zshrc", restore+"unset TRECS_USER_ZDOTDIR\n"+
			`[ -r "${ZDOTDIR:-$HOME}/.zshrc" ] && . "${ZDOTDIR:-$HOME}/.zshrc"`+"\n"+zshIntegration+scriptedOnly(scripted, zshScripted)); err != nil {
			cleanup()
			return nil, err
		}
		return &shellIntegration{env: append(env, "ZDOTDIR="+dir, "TRECS_USER_ZDOTDIR="+orig), cleanup: cleanup}, nil

	case "fish":
		script := fishIntegration
		// A fish that can't be asked is treated as a new one: adding only the end of
		// the prompt to a fish that marks nothing leaves the recording unmarked (read
		// the old way), while doubling a fish that marks everything makes a mess.
		if out, err := exec.Command(shell, "--version").Output(); err != nil || fishMajorVersion(string(out)) >= 4 {
			script = fishNativeIntegration
		}
		f, err := write("integration.fish", script)
		if err != nil {
			cleanup()
			return nil, err
		}
		return &shellIntegration{args: []string{"--init-command", fmt.Sprintf("source '%s'", fishQuote(f))}, env: env, cleanup: cleanup}, nil
	}
	cleanup()
	return nil, nil
}
