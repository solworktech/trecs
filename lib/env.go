package lib

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// maxEnvLen caps what is stored about the environment: it is shown in a page
// header, so a long or odd value is cut rather than trusted.
const maxEnvLen = 100

// OSReleasePath is where Linux distributions describe themselves.
const OSReleasePath = "/etc/os-release"

// DetectEnv names the system a recording is made on, for display ("Ubuntu 24.04.1 LTS").
// It reads PRETTY_NAME from the os-release file; macOS and the BSDs have no such file, and
// a file without the key is no better, so it then asks `uname -o`, then `uname -s`, and last
// the Go runtime's name for the OS. It never fails: the result is "" only if nothing at all
// could be learned.
func DetectEnv() string {
	return detectEnv(OSReleasePath, func(flag string) string {
		out, err := exec.Command("uname", flag).Output()
		if err != nil {
			return ""
		}
		return string(out)
	})
}

// detectEnv is DetectEnv with its two sources passed in, for tests.
func detectEnv(osReleasePath string, uname func(flag string) string) string {
	if name := prettyName(osReleasePath); name != "" {
		return name
	}
	// `uname -o` is the documented fallback; not every uname has it (OpenBSD, older macOS), so -s is tried too.
	for _, flag := range []string{"-o", "-s"} {
		if name := cleanEnv(uname(flag)); name != "" {
			return name
		}
	}
	return cleanEnv(runtime.GOOS)
}

// prettyName returns PRETTY_NAME from an os-release file, or "" if the file is missing or has none.
func prettyName(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || key != "PRETTY_NAME" {
			continue
		}
		return cleanEnv(unquoteOSRelease(val))
	}
	return ""
}

// unquoteOSRelease undoes the shell-style quoting os-release values may use: "..." or '...', with \" \\ \$ \` escapes inside double quotes.
func unquoteOSRelease(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(v); i++ {
			if v[i] == '\\' && i+1 < len(v) && strings.ContainsRune("\"\\$`", rune(v[i+1])) {
				i++
			}
			b.WriteByte(v[i])
		}
		return b.String()
	}
	return v
}

// cleanEnv makes a value fit to store and show: control characters dropped, runs of whitespace made single spaces, cut to maxEnvLen runes.
func cleanEnv(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxEnvLen {
		s = string(r[:maxEnvLen])
	}
	return s
}

// ShellName is the name of a shell given the path it was started from ("/bin/bash" -> "bash").
func ShellName(shell string) string {
	return cleanEnv(filepath.Base(strings.TrimSpace(shell)))
}
