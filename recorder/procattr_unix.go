//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts a child in a process group of its own. The terminal sends Ctrl+C to the foreground process
// group; an ffmpeg that is in it is stopped by a stray Ctrl+C in the middle of its file instead of by the recorder,
// when it has finished.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
