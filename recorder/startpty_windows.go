//go:build windows

package main

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startShell(cmd *exec.Cmd, _ *termSettings, _ func(ptmx *os.File)) (*os.File, error) {
	return pty.Start(cmd)
}
