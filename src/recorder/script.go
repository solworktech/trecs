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

// A shell asks its terminal what it is (fish: primary device attributes, the
// terminal's version, whether it speaks the kitty keyboard protocol) and waits for
// the answers - two seconds, then it prints a warning that ends up in the
// recording. A scripted session has no terminal to answer, and forwarding the
// questions to the real one would leave its replies in a stdin nobody reads, so
// the recorder answers the one that matters (primary device attributes: "a
// VT220-class terminal") itself, and does not pass the questions on. The questions
// are still in the recording, as they are in one made by hand.
var (
	deviceAttributesQuery = regexp.MustCompile("\x1b\\[0?c")
	terminalQueryRe       = regexp.MustCompile("\x1b\\[(?:[>=]?0?c|>0?q|\\?u|[56]n|\\?[0-9;]*\\$p)")
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
	deadline := time.After(timeout)
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
			if quiet > 600*time.Millisecond && time.Since(start) > 800*time.Millisecond {
				return true
			}
		case <-deadline:
			return false
		case <-tr.done:
			return false
		}
	}
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
	enter := func() bool { _, err := tr.ptmx.WriteString("\r"); return err == nil }
	typeLine := func(line string) bool {
		if !tr.config.HumanLike {
			_, err := tr.ptmx.WriteString(line + "\r")
			return err == nil
		}
		for _, r := range line {
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

	if !tr.waitPrompt(20*time.Second, false, 0) {
		fmt.Fprintln(os.Stderr, "recorder: the shell never showed a prompt")
	}
	last := ""
	for _, line := range lines {
		select { // forget any prompt seen before this line
		case <-tr.promptCh:
		default:
		}
		if tr.config.HumanLike && !pause(jitter(600, 1000)) {
			return
		}
		started := atomic.LoadInt64(&tr.commandsStarted)
		if !typeLine(line) {
			return
		}
		if !tr.waitPrompt(2*time.Minute, true, started) {
			select {
			case <-tr.done:
				return
			default:
				fmt.Fprintf(os.Stderr, "recorder: no prompt after %q; carrying on\n", line)
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
