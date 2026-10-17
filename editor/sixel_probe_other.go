//go:build windows

package main

import "time"

// probeTerminal on Windows asks nothing: the terminal's answers cannot be read before the editor starts there. Sixel is
// used only when TRECS_SIXEL=1 and TRECS_CELL_SIZE=WxH say so (see termGraphicsFromEnv).
func probeTerminal(time.Duration) termGraphics { return termGraphics{} }
