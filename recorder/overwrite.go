package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ergochat/readline"

	libtrecs "trecs/lib"
)

// A recording is not overwritten without being asked about: a second `record -name demo` would otherwise replace the first
// one, silently, the moment it started.

// sessionNameOf is the name a session goes by: the one asked for, else the time it starts.
func sessionNameOf(config *libtrecs.RecordingConfig) string {
	if config.SessionName != "" {
		return config.SessionName
	}
	return time.Now().Format("2006-01-02_15-04-05")
}

// filesInTheWay are the files a session with this configuration would write over, and that are there.
func filesInTheWay(config *libtrecs.RecordingConfig, dir string) []string {
	var names []string
	if config.TerminalEnabled {
		names = append(names, "terminal.jsonl")
	}
	if config.AudioEnabled {
		names = append(names, "audio."+audioExtension(config.AudioCodec))
	}
	if config.CameraEnabled {
		names = append(names, "camera.mp4")
	}
	if config.ScreenEnabled {
		names = append(names, "screen.mp4")
	}
	var there []string
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			there = append(there, n)
		}
	}
	return there
}

// chooseSessionTarget sees to it that the session does not write over a recording that is there. If one is, a person is asked:
// the exact answer YES overwrites it, anything else asks for another name - or a path - to record to, with Tab completing
// paths. With nobody to ask it is an error (unless overwrite, -overwrite, says it is wanted). It may change config.SessionName
// and config.OutputDir.
func chooseSessionTarget(config *libtrecs.RecordingConfig, in io.Reader, out io.Writer, interactive, overwrite bool) error {
	outputDir := config.OutputDir
	if outputDir == "" {
		outputDir = "./recordings"
	}
	reader := bufio.NewReader(in)
	for {
		dir := filepath.Join(outputDir, sessionNameOf(config))
		in := filesInTheWay(config, dir)
		if len(in) == 0 || overwrite {
			config.OutputDir = outputDir
			return nil
		}
		if !interactive {
			return fmt.Errorf("%s already has %s: record under another -name, or give -overwrite to replace it", dir, strings.Join(in, ", "))
		}
		say(out, "%s already has %s.\n", dir, strings.Join(in, ", "))
		say(out, "%s", yellow(out, "Type 'YES' to overwrite it, anything else to record to another name or path: "))
		answer, err := reader.ReadString('\n')
		if strings.TrimSpace(answer) == "YES" {
			config.OutputDir = outputDir
			return nil
		}
		if err != nil && answer == "" {
			return errors.New("nothing was answered")
		}
		next, err := askPath("Name or path to record to (Tab completes paths): ")
		if err != nil {
			return err
		}
		if next == "" {
			continue
		}
		next = expandHome(next)
		if strings.ContainsRune(next, filepath.Separator) || filepath.IsAbs(next) {
			next = filepath.Clean(next)
			outputDir, config.SessionName = filepath.Dir(next), filepath.Base(next)
		} else {
			config.SessionName = next
		}
	}
}

// checkSessionName refuses a -name that is not a name: with a path separator or ".." in it the recording would be written somewhere
// other than the output directory it was asked to go to. (To record somewhere else, give -output; the question that asks for another
// name when one is taken does take a path, as the person is typing it there.)
func checkSessionName(name string) error {
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("-name %q is not a name: it must not contain a path separator or be . or .. (use -output to say where to record to)", name)
	}
	return nil
}

// askPath asks for a line with readline's editing, history and Tab completion of paths.
func askPath(prompt string) (string, error) {
	rl, err := readline.NewFromConfig(&readline.Config{Prompt: prompt, AutoComplete: pathCompleter{}})
	if err != nil {
		return "", err
	}
	defer func() { _ = rl.Close() }()
	line, err := rl.ReadLine()
	if err != nil { // Ctrl+C, Ctrl+D
		return "", errors.New("cancelled")
	}
	return strings.TrimSpace(line), nil
}

// expandHome turns a leading ~ or ~/ into the home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// pathCompleter completes the path being typed with what is on disk: the files and directories whose names begin with what
// has been typed of the last name (directories end in a separator, and hidden ones are offered only to a dot).
type pathCompleter struct{}

func (pathCompleter) Do(line []rune, pos int) ([][]rune, int) {
	typed := expandHome(string(line[:pos]))
	dir, prefix := filepath.Split(typed)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.IsDir() {
			name += string(filepath.Separator)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([][]rune, len(names))
	for i, n := range names {
		out[i] = []rune(n)[len([]rune(prefix)):]
	}
	return out, len([]rune(prefix))
}
