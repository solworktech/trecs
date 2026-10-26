//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// startShell starts cmd on a new pseudo-terminal, as pty.Start does (own session, the terminal as its controlling one) - but with
// the terminal made ready first: it has the settings of the one the person is at (outer; nil: the kernel's) and the size of
// its window (size is told to set it). A shell started on a terminal of no size and of other settings, and then given its
// own, would meanwhile see the wrong ones: readline asks for the size once, at the start.
func startShell(cmd *exec.Cmd, outer *termSettings, size func(ptmx *os.File)) (*os.File, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tty.Close() }() // the shell has its own by then
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	writeTermios(tty, outer)
	size(ptmx)
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		return nil, err
	}
	return ptmx, nil
}
