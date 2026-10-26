//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package main

import "os"

// termSettings: on a system where the settings cannot be copied, there are none to copy.
type termSettings struct{}

func readTermios(*os.File) *termSettings   { return nil }
func writeTermios(*os.File, *termSettings) {}
