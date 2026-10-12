package lib

import (
	"os"
	"testing"
)

// The player counts frames as LoadFrames does, which is how the commands' frame indices are counted: the recording's header
// line is not a frame to either.
func TestThePlayersFramesAreTheCommandsFrames(t *testing.T) {
	path := "testdata/fish-marked.jsonl" // has a header line
	loaded, _, meta, err := LoadFrames(path)
	if err != nil || meta.Width == 0 {
		t.Fatalf("the fixture should have a header: %+v %v", meta, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tp := NewTerminalPlayer().(*TerminalPlayerImpl)
	tp.file = f
	if err := tp.loadFrames(); err != nil {
		t.Fatal(err)
	}
	if len(tp.frames) != len(loaded) {
		t.Fatalf("the player has %d frames, LoadFrames %d", len(tp.frames), len(loaded))
	}
	cmds, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cmds {
		if got, want := tp.frames[c.FirstRawFrameIndex], loaded[c.FirstRawFrameIndex]; got != want {
			t.Errorf("command %d: the player's frame %d is %+v, not %+v", i, c.FirstRawFrameIndex, got, want)
		}
		// and the frame a command begins with is its prompt
		if i > 0 && !contains(tp.frames[c.FirstRawFrameIndex].Data, "133;A") {
			t.Errorf("command %d does not begin with its prompt: %q", i, tp.frames[c.FirstRawFrameIndex].Data)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
