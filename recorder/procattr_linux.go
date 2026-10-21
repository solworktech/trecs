//go:build linux

package main

import "syscall"

// dieWithParent has the kernel end the child when the recorder does, however it does: ffmpeg is in a process group of its own,
// and one that is left behind by a recorder that was killed goes on recording the microphone, the camera or the screen -
// it does not end by itself when its input is closed.
func dieWithParent(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGTERM }
