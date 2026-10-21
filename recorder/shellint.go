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
if [ "${BASH_VERSINFO[0]}" -gt 4 ] || { [ "${BASH_VERSINFO[0]}" -eq 4 ] && [ "${BASH_VERSINFO[1]}" -ge 4 ]; }; then
  case $PS1 in
  *'133;A'*) ;;
  *)
    # $? first: D reports the status of the command that just ran and hands it on.
    __trecs_pre() { local s=$?; printf '\e]133;D;%s\a' "$s"; return "$s"; }
    # Last, so a prompt a user's PROMPT_COMMAND rebuilds each time still gets wrapped.
    __trecs_post() {
      local s=$?
      case $PS1 in *'133;A'*) ;; *) PS1='\[\e]133;A\a\]'"$PS1"'\[\e]133;B\a\]' ;; esac
      case $PS2 in *'133;A'*) ;; *) PS2='\[\e]133;A;k=s\a\]'"$PS2"'\[\e]133;B\a\]' ;; esac
      return "$s"
    }
    case $PS0 in *'133;C'*) ;; *) PS0='\e]133;C\a'"$PS0" ;; esac
    if [[ $(declare -p PROMPT_COMMAND 2>/dev/null) == "declare -a"* ]]; then
      PROMPT_COMMAND=(__trecs_pre "${PROMPT_COMMAND[@]}" __trecs_post)
    else
      PROMPT_COMMAND="__trecs_pre${PROMPT_COMMAND:+;$PROMPT_COMMAND};__trecs_post"
    fi
    ;;
  esac
fi
`

const zshIntegration = `
# --- trecs shell integration (prompt marks, OSC 133) ---
if [[ $PS1 != *'133;A'* ]]; then
  autoload -Uz add-zsh-hook
  __trecs_precmd() {
    local s=$?
    print -rn -- $'\e]133;D;'"$s"$'\a'
    # Each prompt, as a theme that rebuilds PS1 on every precmd would have it.
    [[ $PS1 == *'133;A'* ]] || PS1=$'%{\e]133;A\a%}'"$PS1"$'%{\e]133;B\a%}'
    [[ $PS2 == *'133;A'* ]] || PS2=$'%{\e]133;A;k=s\a%}'"$PS2"$'%{\e]133;B\a%}'
  }
  __trecs_preexec() { print -rn -- $'\e]133;C\a' }
  add-zsh-hook precmd __trecs_precmd
  add-zsh-hook preexec __trecs_preexec
fi
`

const fishIntegration = `
# --- trecs shell integration (prompt marks, OSC 133) ---
if not set -q __trecs_marks
    set -g __trecs_marks 1
    # The prompt is run FIRST, and its output is what the marks are wrapped around: anything run before it (even a printf)
    # would reset $status, and a prompt that shows the last command's status would show a success.
    if functions -q fish_prompt
        functions -c fish_prompt __trecs_orig_prompt
        function fish_prompt
            set -l __trecs_p (__trecs_orig_prompt)
            printf '\e]133;A\a%s\e]133;B\a' (string join \n -- $__trecs_p)
        end
    else
        function fish_prompt
            printf '\e]133;A\a\e]133;B\a'
        end
    end
    function __trecs_preexec --on-event fish_preexec
        printf '\e]133;C\a'
    end
    function __trecs_postexec --on-event fish_postexec
        printf '\e]133;D;%s\a' $status
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
    functions -q fish_prompt; and functions -c fish_prompt __trecs_orig_prompt
    function fish_prompt
        functions -q __trecs_orig_prompt; and __trecs_orig_prompt
        printf '\e]133;B\a'
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
