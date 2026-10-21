//go:build !linux && !windows

package main

import "syscall"

// dieWithParent: only Linux can have the kernel end a child with its parent. Elsewhere the recorder ends its ffmpeg itself when
// it is told to by a signal it can catch (see main), and cannot when it is killed outright.
func dieWithParent(*syscall.SysProcAttr) {}
