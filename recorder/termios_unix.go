//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// termSettings are a terminal's settings (the termios): which key is erase, which interrupts, whether Ctrl+S stops the output...
type termSettings = unix.Termios

// readTermios reads the settings of the terminal f is; nil if it cannot be told.
func readTermios(f *os.File) *termSettings {
	t, err := unix.IoctlGetTermios(int(f.Fd()), ioctlReadTermios)
	if err != nil {
		return nil
	}
	return t
}

// writeTermios gives the terminal f those settings; nothing if there are none.
func writeTermios(f *os.File, t *termSettings) {
	if t != nil {
		_ = unix.IoctlSetTermios(int(f.Fd()), ioctlWriteTermios, t)
	}
}
