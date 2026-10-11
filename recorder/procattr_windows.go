//go:build windows

package main

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}
