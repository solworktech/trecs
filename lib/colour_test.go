package lib

import (
	"bytes"
	"testing"
)

func TestPaintLeavesAWriterThatIsNotATerminalAlone(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if got := Paint(&bytes.Buffer{}, Green, "ok"); got != "ok" {
		t.Errorf("Paint to a buffer = %q", got)
	}
}
