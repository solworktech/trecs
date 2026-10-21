package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestThePlaybackLegendEndsWithFindThenHelp(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	first := strings.TrimRight(strings.Split(h.screen(), "\n")[0], " ")
	if !strings.HasSuffix(first, "/ find   ? help") {
		t.Errorf("the legend: %q", first)
	}
	h.sim.SetSize(14, 40) // too narrow for both: the help is the last to go
	h.waitFor("the narrow legend", func() bool {
		f := strings.TrimRight(strings.Split(h.screen(), "\n")[0], " ")
		return strings.HasSuffix(f, "? help") && !strings.Contains(f, "/ find")
	}, 3*time.Second)
	h.sim.SetSize(120, 40)
	h.key(tcell.KeyCtrlL)
	h.waitFor("the command list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	if f := strings.Split(h.screen(), "\n")[0]; strings.Contains(f, "/ find") {
		t.Errorf("only the playback page can search: %q", f)
	}
}

func TestSlashSearchesAndEnterOnAMatchSeeksPaused(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.typeText("/")
	h.waitFor("the search box", func() bool { return h.front() == searchPage }, 2*time.Second)
	h.waitText("At least 3 characters", 2*time.Second)

	h.typeText("ab") // too short
	h.key(tcell.KeyEnter)
	h.waitText("Type at least 3 characters.", 2*time.Second)

	h.typeText("c")
	h.key(tcell.KeyBackspace2)
	h.key(tcell.KeyBackspace2)
	h.key(tcell.KeyBackspace2)
	h.typeText("hello wor")
	h.key(tcell.KeyEnter)
	h.waitText("matching in", 2*time.Second)
	h.waitText("echo hello world", 2*time.Second) // the command, as the heading
	if !strings.Contains(h.screen(), "1 line matching in 1 command") && !strings.Contains(h.screen(), "2 lines matching in 1 command") {
		t.Errorf("the status: %q", h.screen())
	}

	// the matched stretch is highlighted
	found := false
	h.onUI(func() {
		cells, w, ht := h.sim.GetContents()
		for y := 0; y < ht && !found; y++ {
			var row strings.Builder
			bg := true
			for x := 0; x < w; x++ {
				c := cells[y*w+x]
				if len(c.Runes) > 0 && c.Style != tcell.StyleDefault {
					_, b, _ := c.Style.Decompose()
					if b == tcell.ColorYellow {
						row.WriteRune(c.Runes[0])
					}
				}
			}
			_ = bg
			if strings.Contains(row.String(), "hello wor") {
				found = true
			}
		}
	})
	if !found {
		t.Errorf("the term is not highlighted:\n%s", h.screen())
	}

	h.key(tcell.KeyEnter) // on the match in focus
	h.waitFor("back on the playback page", func() bool { return h.front() == "playback" }, 2*time.Second)
	h.waitText("Paused at", 2*time.Second)
	if !strings.Contains(h.screen(), "hello world") {
		t.Errorf("the line is not on the screen:\n%s", h.screen())
	}
	time.Sleep(400 * time.Millisecond)
	if st := h.status(); !strings.Contains(st, "Paused at") {
		t.Errorf("playback went on: %q", st)
	}
}

func TestEscClosesTheSearchAndLeavesPlaybackPaused(t *testing.T) {
	h := newHarness(t, nil)
	h.typeText("/")
	h.waitFor("the search box", func() bool { return h.front() == searchPage }, 2*time.Second)
	h.key(tcell.KeyEscape)
	h.waitFor("the playback page", func() bool { return h.front() == "playback" }, 2*time.Second)
	h.waitText("Paused.", 2*time.Second)
}

func TestSearchOptionsAndErrors(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.typeText("/")
	h.waitFor("the search box", func() bool { return h.front() == searchPage }, 2*time.Second)
	h.typeText("(?=hello")
	h.key(tcell.KeyEnter)
	h.waitText("Not a valid pattern", 2*time.Second)
	// Alt+R turns the regex off: it is only characters, and nothing in the recording has them
	h.sim.InjectKey(tcell.KeyRune, 'r', tcell.ModAlt)
	h.key(tcell.KeyEnter)
	h.waitText("No matches.", 2*time.Second)
	// Match case: "HELLO" with a capital counts as it is typed, and is not found
	h.key(tcell.KeyCtrlU)
	h.typeText("HELLO")
	h.sim.InjectKey(tcell.KeyRune, 'r', tcell.ModAlt) // regex on again
	h.key(tcell.KeyEnter)
	h.waitText("No matches.", 2*time.Second)
	h.key(tcell.KeyCtrlU)
	h.typeText("hello")
	h.key(tcell.KeyEnter)
	h.waitText("matching in", 2*time.Second)
}
