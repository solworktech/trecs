package main

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Scripted sessions: with -commands-file the recorder types the commands itself,
// so a recording can be made where there is no keyboard and no terminal - a CI
// job, say. After each command it waits for the shell's next prompt (the
// prompt mark, when the shell has integration; otherwise a quiet spell) so
// output and prompts come out in the order a person would have seen them.

// readScript reads the commands, one per line. Blank lines and lines starting
// with # are skipped. A line that ends with a backslash, or leaves a quote open,
// is continued by the next - they are typed one line at a time, like a person
// would, and the shell asks for each in turn.
func readScript(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		// A control character is a keystroke - Ctrl+C, Escape, Ctrl+D - and not text: typed, it would do what the key does,
		// and it cannot be seen when the file is read to check it. (A tab is text: see keystrokes.)
		for _, r := range line {
			if r != '\t' && (r < ' ' || r == 0x7f || (r >= 0x80 && r <= 0x9f)) {
				return nil, fmt.Errorf("%s has a control character (%U) in %q: it would be typed as a key, and it cannot be seen", path, r, line)
			}
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s has no commands in it", path)
	}
	return lines, nil
}

// keystrokes is what is typed for a line: a tab is quoted (Ctrl+V), so that the shell takes it for a tab in the command and does not
// complete a name with it.
func keystrokes(line string) string { return strings.ReplaceAll(line, "\t", "\x16\t") }

// A shell asks its terminal what it is (fish: primary device attributes, the
// terminal's version, whether it speaks the kitty keyboard protocol, its background
// colour, its terminfo capabilities) and waits for
// the answers - two seconds, then it prints a warning that ends up in the
// recording. A scripted session has no terminal to answer, and forwarding the
// questions to the real one would leave its replies in a stdin nobody reads, so
// the recorder answers the one that matters (primary device attributes: "a
// VT220-class terminal") itself, and does not pass the questions on. The questions
// are still in the recording, as they are in one made by hand.
var (
	deviceAttributesQuery = regexp.MustCompile("\x1b\\[0?c")
	terminalQueryRe       = regexp.MustCompile(
		// CSI: device attributes, XTVERSION, the kitty keyboard flags, status and cursor reports (5n, 6n, ?996n...), mode queries
		// (DECRQM), window reports (14t, 16t, 18t...), graphics-size queries
		"\x1b\\[(?:[>=]?0?c|>0?q|\\?u|\\??[0-9;]*n|\\??[0-9;]*\\$p|(?:1[4-689]|2[01])t|\\?[0-9;]*S)" +
			// OSC asking for a colour or a setting (background 11;?, foreground 10;?, palette 4;n;?, clipboard 52;c;?), ended by BEL or ST
			"|\x1b\\][0-9]+(?:;[0-9A-Za-z:,/]*)*;\\?(?:\x07|\x1b\\\\)" +
			// DCS asking for a terminfo capability (XTGETTCAP, ESC P + q) or a setting (DECRQSS, ESC P $ q)
			"|\x1bP[+$]q[^\x1b\x07]*\x1b\\\\" +
			// a kitty graphics query
			"|\x1b_G[^\x1b]*a=q[^\x1b]*\x1b\\\\")
)

const deviceAttributesReply = "\x1b[?62;22c"

// noteOutput is called with everything the shell writes. It remembers when it
// last wrote, and signals when a prompt has just ended (mark B), which is the
// moment the shell is ready for the next line.
func (tr *TerminalRecorderImpl) noteOutput(data string) {
	atomic.StoreInt64(&tr.lastOutput, time.Now().UnixNano())
	const markB, markC = "\x1b]133;B", "\x1b]133;C" // the same length: one tail serves both
	window := tr.markTail + data
	if strings.Contains(window, markB) {
		select {
		case tr.promptCh <- struct{}{}:
		default:
		}
	}
	atomic.AddInt64(&tr.commandsStarted, int64(strings.Count(window, markC)))
	tr.markTail = window[max(0, len(window)-(len(markB)-1)):] // a mark can be cut between reads
	if !tr.interactive {
		for range deviceAttributesQuery.FindAllString(data, -1) {
			_, _ = tr.ptmx.WriteString(deviceAttributesReply)
		}
	}
}

// waitPrompt waits until the shell is ready for the next line. It reports false
// if the recording was stopped or the wait timed out. afterLine says a line has
// just been entered: if it then neither starts a command nor shows a prompt, the
// shell is waiting for more of it - an open quote in fish, which has no PS2 to
// announce that - and the next line can go in.
func (tr *TerminalRecorderImpl) waitPrompt(timeout time.Duration, afterLine bool, startedBefore int64) bool {
	var deadline <-chan time.Time // none: a command takes as long as it takes
	if timeout > 0 {
		deadline = time.After(timeout)
	}
	if tr.marks {
		var grace <-chan time.Time
		if afterLine {
			grace = time.After(1200 * time.Millisecond)
		}
		for {
			select {
			case <-tr.promptCh:
				return true
			case <-deadline:
				return false
			case <-tr.done:
				return false
			case <-grace:
				if atomic.LoadInt64(&tr.commandsStarted) == startedBefore {
					return true // nothing started, no prompt: it wants the rest of the command
				}
				grace = nil // a command is running: wait for its prompt
			}
		}
	}
	// No marks: the shell is ready when it has gone quiet.
	start := time.Now()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			quiet := time.Since(time.Unix(0, atomic.LoadInt64(&tr.lastOutput)))
			// quiet is not enough: a command that is running without a word (a copy, a sleep, a question waiting for its answer) is
			// quiet too, and the next line must not be typed into it. The shell must also be what the terminal is waiting on.
			if quiet > 600*time.Millisecond && time.Since(start) > 800*time.Millisecond && tr.shellInForeground() {
				return true
			}
		case <-deadline:
			return false
		case <-tr.done:
			return false
		}
	}
}

// scriptPromptTimeout is added to -command-timeout, and is for the tests to shorten. By default (-command-timeout 0) a command
// is waited for as long as it runs: it is not interrupted - that could leave a half-done install or a half-written file that
// running it by hand would not - and nothing is typed while it runs, so a command that waits for input waits, as it would at a
// keyboard that nobody touches. -command-timeout sets a limit for a job that must end; the shell is then hung up on, as when a terminal is closed.
var scriptPromptTimeout time.Duration

// abortScript ends a scripted session that cannot go on: the recording is kept as far as it got, and the recorder fails.
func (tr *TerminalRecorderImpl) abortScript(why string) {
	tr.fail(why)
	_ = tr.Stop()
}

// feedScript types the commands into the shell, then ends the session.
func (tr *TerminalRecorderImpl) feedScript(lines []string) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	pause := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-tr.done:
			return false
		}
	}
	jitter := func(lo, hi int) time.Duration { return time.Duration(lo+rng.Intn(hi-lo+1)) * time.Millisecond }
	// forgetPrompt drops a "the shell is ready" signal that is already there. It is called just before a line is entered: a prompt
	// that was drawn before the Enter - old fish (3.1) draws its first one twice, 2 ms apart, and repaints it at other times - is
	// not the end of the command that the Enter starts. Left in, it released the line after it as soon as this one was entered,
	// and every later line stayed one step early: the next command was typed into `apt update` while it ran. What shows up after
	// the Enter is the shell's answer to it, and is kept.
	forgetPrompt := func() {
		select {
		case <-tr.promptCh:
		default:
		}
	}
	enter := func() bool { forgetPrompt(); _, err := tr.ptmx.WriteString("\r"); return err == nil }
	typeLine := func(line string) bool {
		if !tr.config.HumanLike {
			forgetPrompt()
			_, err := tr.ptmx.WriteString(keystrokes(line) + "\r")
			return err == nil
		}
		for _, r := range keystrokes(line) {
			if _, err := tr.ptmx.WriteString(string(r)); err != nil {
				return false
			}
			d := jitter(40, 110)
			if r == ' ' && rng.Intn(4) == 0 {
				d += jitter(80, 220) // a pause between words
			}
			if !pause(d) {
				return false
			}
		}
		return pause(jitter(250, 500)) && enter()
	}

	// A line typed into a shell that is not at a prompt goes to whatever is reading: it must not be. So when the shell is not
	// where the script expects it, the session ends, and says so, instead of typing on.
	if !tr.waitPrompt(time.Minute, false, 0) {
		tr.abortScript("the shell never showed a prompt: nothing was typed")
		return
	}
	// Old fish draws its first prompt twice, 2 ms apart: the second mark is not an answer to anything that is typed, and a line
	// entered in those milliseconds (without -human-like there is no pause) would take it for one. So the shell is given a moment
	// to finish drawing before the first line goes in.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if time.Since(time.Unix(0, atomic.LoadInt64(&tr.lastOutput))) > 250*time.Millisecond {
			break
		}
		if !pause(50 * time.Millisecond) {
			return
		}
	}
	last := ""
	for _, line := range lines {
		forgetPrompt() // any prompt seen before this line
		if tr.config.HumanLike && !pause(jitter(600, 1000)) {
			return
		}
		started := atomic.LoadInt64(&tr.commandsStarted)
		if !typeLine(line) {
			return
		}
		if !tr.waitPrompt(tr.config.CommandTimeout+scriptPromptTimeout, true, started) {
			select {
			case <-tr.done:
				return
			default:
				tr.abortScript(fmt.Sprintf("no prompt after %q within %v: the shell was hung up on, and the rest of the file was not typed", line, tr.config.CommandTimeout+scriptPromptTimeout))
				return
			}
		}
		last = strings.TrimSpace(line)
	}
	if last != "exit" && last != "logout" {
		if tr.config.HumanLike && !pause(jitter(600, 1000)) {
			return
		}
		typeLine("exit")
	}
}
