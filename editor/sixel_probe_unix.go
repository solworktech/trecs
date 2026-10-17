//go:build !windows

package main

import (
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// probeTerminal asks the terminal two things before the editor takes it over: whether it draws sixel graphics (the
// answer to "primary device attributes" lists feature 4 when it does), and, if it says, how large a character cell is
// in pixels (XTWINOPS 16, answered "CSI 6 ; height ; width t"). The two questions are sent together, the cell size
// first: the answer to the device attributes comes last from every terminal, so it is also the end of the answers, and a
// terminal that does not know the first simply says nothing to it. A terminal that says nothing at all within
// the time allowed is taken not to draw sixels.
func probeTerminal(timeout time.Duration) (t termGraphics) {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) || os.Getenv("TERM") == "dumb" {
		return t
	}
	old, err := term.MakeRaw(in)
	if err != nil {
		return t
	}
	defer func() { _ = term.Restore(in, old) }()

	if _, err := os.Stdout.WriteString("\x1b[16t\x1b[c"); err != nil {
		return t
	}
	var reply []byte
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		fds := []unix.PollFd{{Fd: int32(in), Events: unix.POLLIN}}
		wait := int(time.Until(deadline) / time.Millisecond)
		if n, err := unix.Poll(fds, max(wait, 1)); err != nil || n == 0 {
			if err == unix.EINTR {
				continue
			}
			break
		}
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			break
		}
		reply = append(reply, buf[:n]...)
		if endsDeviceAttributes(string(reply)) {
			break
		}
	}
	return parseProbeReply(string(reply))
}

// endsDeviceAttributes: the reply holds a complete answer to "primary device attributes", ESC [ ? ... c.
func endsDeviceAttributes(s string) bool {
	i := strings.LastIndex(s, "\x1b[?")
	return i >= 0 && strings.Contains(s[i:], "c")
}
