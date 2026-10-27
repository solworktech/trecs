package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	libtrecs "trecs/lib"
)

// pausedSix writes a recording of six commands with 8.5 s of quiet between them (it is 52 s long), and returns its path.
func pausedSix(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":"meta","width":80,"height":24}` + "\n")
	for k := 0; k < 6; k++ {
		s := 1000 + k*10000
		for _, f := range []struct {
			at   int
			data string
		}{{s, `\u001b]133;A\u0007$ \u001b]133;B\u0007`}, {s + 500, "echo hi"}, {s + 900, `\r\n\u001b]133;C\u0007`}, {s + 1000, `hi\r\n`}, {s + 1500, `\u001b]133;D;0\u0007`}} {
			fmt.Fprintf(&b, `{"timestamp":%d,"data":"%s"}`+"\n", f.at, f.data)
		}
	}
	path := filepath.Join(t.TempDir(), "six.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *harness) lengthOnDisk() int64 {
	cmds := h.reparsed()
	return libtrecs.LastTimestamp(cmds)
}

func TestCtrlYAsksForTheLengthAndCutsThePauses(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	before := h.lengthOnDisk()

	h.key(tcell.KeyCtrlY)
	h.waitText("Trim long pauses", 3*time.Second)
	if got := h.front(); got != idleTrimPage {
		t.Fatalf("the front page is %q", got)
	}
	for _, want := range []string{"Longest pause to keep", "2s", "Trim", "Cancel"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("%q is not in the modal:\n%s", want, h.screen())
		}
	}

	// the box has 2s in it: it is replaced
	for range "2s" {
		h.key(tcell.KeyBackspace2)
	}
	h.typeText("1s")
	h.key(tcell.KeyEnter)
	h.waitFor("the recording to be written", func() bool { return h.lengthOnDisk() < before }, 10*time.Second)
	if got, want := h.lengthOnDisk(), int64(6*(1500)+5*1000+1000); got != want { // 6 commands of 1.5 s, 5 pauses cut to 1 s, the first second
		t.Errorf("the recording is %d ms, want %d", got, want)
	}
	if _, err := os.Stat(h.path() + ".backup"); err != nil {
		t.Errorf("no backup: %v", err)
	}
	h.waitFor("the status to say so", func() bool { return strings.Contains(h.status(), "Cut ") }, 5*time.Second)
	if got := h.front(); got != "playback" {
		t.Errorf("the front page is %q", got)
	}
}

func TestCtrlYAWrongLengthIsSaidAndNothingIsWritten(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	before := h.lengthOnDisk()
	for _, bad := range []string{"soon", "0", "10ms"} {
		h.key(tcell.KeyCtrlY)
		h.waitText("Trim long pauses", 3*time.Second)
		for range "2s" {
			h.key(tcell.KeyBackspace2)
		}
		h.typeText(bad)
		h.key(tcell.KeyEnter)
		time.Sleep(200 * time.Millisecond)
		if got := h.front(); got != idleTrimPage {
			t.Errorf("%q: the modal closed (%q)", bad, got)
		}
		h.key(tcell.KeyEscape)
		h.waitGone("Trim long pauses", 3*time.Second)
	}
	if h.lengthOnDisk() != before {
		t.Error("the recording was changed")
	}
}

func TestCtrlYEscLeavesTheRecordingAlone(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlY)
	h.waitText("Trim long pauses", 3*time.Second)
	h.key(tcell.KeyCtrlY) // the key that opened it closes it too
	h.waitGone("Trim long pauses", 3*time.Second)
	if _, err := os.Stat(h.path() + ".backup"); err == nil {
		t.Error("a backup was made")
	}
}

func TestCtrlYWaitsForWhatIsStaged(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	h.onUI(func() { h.em.deletedCommands[1] = true })
	h.key(tcell.KeyCtrlY)
	time.Sleep(200 * time.Millisecond)
	if h.front() == idleTrimPage {
		t.Error("the modal opened with changes that are not written, which it would lose")
	}
	if !strings.Contains(h.status(), "not written") {
		t.Errorf("the status: %q", h.status())
	}
}

func TestTheHelpKeyIsShownAsQuestionMarkAndBothOpenTheHelp(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	if !strings.Contains(h.screen(), "? help") {
		t.Errorf("the legend:\n%s", h.screen())
	}
	for name, press := range map[string]func(){
		"?":  func() { h.typeText("?") },
		"F1": func() { h.key(tcell.KeyF1) },
	} {
		press()
		h.waitText("keys", 3*time.Second)
		if !strings.Contains(h.screen(), "? / F1") {
			t.Errorf("%s: the menu does not show both keys:\n%s", name, h.screen())
		}
		h.key(tcell.KeyEscape)
		h.waitGone("keys", 3*time.Second)
	}
}

func TestParseIdleLimit(t *testing.T) {
	for in, want := range map[string]time.Duration{"2s": 2 * time.Second, "500ms": 500 * time.Millisecond, "3": 3 * time.Second, " 1.5 ": 1500 * time.Millisecond, "1m30s": 90 * time.Second} {
		if got, err := parseIdleLimit(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "soon", "0", "0s", "50ms", "-2s"} {
		if _, err := parseIdleLimit(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}
