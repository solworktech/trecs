package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libtrecs "trecs/lib"
)

// A scripted session that prints a sixel graphic: it is in the recording, whole, and the screen the player builds from it
// has the picture.
func TestARecordedSixelGraphicIsKept(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cmds.txt")
	// a picture 16 px wide, 5 bands (30 px) tall, red; and a query of the kind that is not kept
	cmd := `printf 'before\n\033P+q544e\033\\\033P0;1;0q"1;1;16;30#1;2;100;0;0#1!16~-#1!16~-#1!16~-#1!16~-#1!16~\033\\after\n'`
	if err := os.WriteFile(script, []byte(cmd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "terminal.jsonl")
	tr, err := NewTerminalRecorder(&libtrecs.RecordingConfig{
		TerminalEnabled: true, TerminalCommand: "/bin/sh", ScriptFile: script, NoMarks: true, Cols: 100, Rows: 30,
	}, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Skipf("cannot start a scripted session here: %v", err)
	}
	done := make(chan struct{})
	go func() { tr.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = tr.Stop()
	}
	_ = tr.Stop()

	frames, _, _, err := libtrecs.LoadFrames(out)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range frames {
		all.WriteString(f.Data)
	}
	got := all.String()
	if !strings.Contains(got, "\x1bP0;1;0q\"1;1;16;30#1;2;100;0;0#1!16~-#1!16~-#1!16~-#1!16~-#1!16~\x1b\\") {
		t.Fatalf("the graphic is not in the recording whole: %q", got)
	}
	if strings.Contains(got, "\x1bP+q544e") {
		t.Error("a query that is not kept is in the recording")
	}
	s := libtrecs.NewVTScreenSized(100, 30)
	s.SetCellSize(8, 16)
	for _, f := range frames {
		s.Feed(f.Data)
	}
	ps := s.ImagePlacements()
	if len(ps) != 1 || ps[0].PxW != 16 || ps[0].PxH != 30 || ps[0].Cols != 2 || ps[0].Rows != 2 {
		t.Errorf("placements: %+v", ps)
	}
}
