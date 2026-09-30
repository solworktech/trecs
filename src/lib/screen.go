package lib

// Screen is a terminal display that can be fed raw recording frame data and
// rendered as tview markup. DisplayBuffer (the original hand-rolled ANSI
// parser) and VTScreen (backed by a real VT100/xterm emulator) both
// implement it, so call sites can switch between them without other
// changes.
type Screen interface {
	// Feed processes one more raw frame of terminal data.
	Feed(data string)
	// Render produces the screen's current content as tview markup.
	Render() string
	// IsBlank reports whether nothing visible is on the screen.
	IsBlank() bool
}

var (
	_ Screen = (*DisplayBuffer)(nil)
	_ Screen = (*VTScreen)(nil)
)
