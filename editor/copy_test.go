package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// copied collects what the editor would put on the clipboard.
func (h *harness) copied() *[]string {
	var got []string
	h.onUI(func() {
		h.em.copyFn = func(s string) string {
			got = append(got, s)
			return "Copied."
		}
	})
	return &got
}

// where finds the cell a word starts at on the screen.
func (h *harness) where(word string) (x, y int) {
	lines := strings.Split(h.screen(), "\n")
	for y, l := range lines {
		if at := strings.Index(l, word); at >= 0 {
			return len([]rune(l[:at])), y
		}
	}
	h.t.Fatalf("%q is not on the screen:\n%s", word, h.screen())
	return
}

// seekToHello leaves the editor paused with "echo hello world", and what it printed, on the screen.
func (h *harness) seekToHello() {
	h.pausePlayback()
	h.typeText("/")
	h.waitFor("the box", func() bool { return h.front() == searchPage }, 2*time.Second)
	h.typeText("hello wor")
	h.key(tcell.KeyEnter)
	h.waitText("matching in", 2*time.Second)
	h.key(tcell.KeyDown) // the second match: the line it printed, not the command
	h.key(tcell.KeyEnter)
	h.waitFor("the playback page", func() bool { return h.front() == "playback" }, 2*time.Second)
	h.waitText("Paused at", 2*time.Second)
	h.waitFor("the mouse", func() bool {
		on := false
		h.onUI(func() { on = h.em.mouseOn })
		return on
	}, 3*time.Second)
}

func (h *harness) drag(x0, y0, x1, y1 int) {
	h.sim.InjectMouse(x0, y0, tcell.Button1, tcell.ModNone)
	h.sim.InjectMouse((x0+x1)/2, (y0+y1)/2, tcell.Button1, tcell.ModNone)
	h.sim.InjectMouse(x1, y1, tcell.Button1, tcell.ModNone)
	h.sim.InjectMouse(x1, y1, tcell.ButtonNone, tcell.ModNone)
}

func TestTheMouseIsTheEditorsOnlyWhilePlaybackIsNotGoingOn(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.waitFor("the mouse to be taken", func() bool {
		on := false
		h.onUI(func() { on = h.em.mouseOn })
		return on
	}, 3*time.Second)
	h.em.player.Resume()
	h.waitFor("the mouse to be given back", func() bool {
		on := true
		h.onUI(func() { on = h.em.mouseOn })
		return !on
	}, 3*time.Second)
}

func TestDraggingOverThePlaybackScreenCopiesWhatIsThere(t *testing.T) {
	h := newHarness(t, nil)
	h.seekToHello()
	got := h.copied()

	x, y := h.where("hello world")
	h.drag(x, y, x+10, y) // "hello world" is eleven cells
	h.waitFor("the copy", func() bool { h.onUI(func() {}); return len(*got) == 1 }, 2*time.Second)
	if (*got)[0] != "hello world" {
		t.Errorf("copied %q", (*got)[0])
	}
	if !strings.Contains(h.status(), "Copied.") {
		t.Errorf("status %q", h.status())
	}
}

func TestADragAcrossRowsCopiesLinesAndLeavesOutTheSpacesAtTheEnd(t *testing.T) {
	h := newHarness(t, nil)
	h.seekToHello()
	got := h.copied()
	x, y := h.where("hello world")
	h.drag(x-2, y, x+10, y+1) // from the command to the line it printed, and a few cells past the end of both
	h.waitFor("the copy", func() bool { h.onUI(func() {}); return len(*got) == 1 }, 2*time.Second)
	lines := strings.Split((*got)[0], "\n")
	if len(lines) != 2 || lines[0] != "o hello world" || lines[1] != "hello world" {
		t.Fatalf("copied %q", (*got)[0])
	}
	for _, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("a line ends in spaces: %q", l)
		}
	}
}

func TestAClickIsNotACopyAndAKeyPutsTheSelectionAway(t *testing.T) {
	h := newHarness(t, nil)
	h.seekToHello()
	got := h.copied()
	x, y := h.where("hello world")
	h.drag(x, y, x, y) // press and release on the same cell
	time.Sleep(200 * time.Millisecond)
	if len(*got) != 0 {
		t.Errorf("a click copied %q", *got)
	}
	h.drag(x, y, x+4, y)
	h.waitFor("the copy", func() bool { h.onUI(func() {}); return len(*got) == 1 }, 2*time.Second)
	h.typeText("x")
	h.waitFor("the selection to go", func() bool {
		on := true
		h.onUI(func() { on = h.em.sel.on })
		return !on
	}, 2*time.Second)
}

func TestTheResultsOfASearchCanBeCopied(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.typeText("/")
	h.waitFor("the box", func() bool { return h.front() == searchPage }, 2*time.Second)
	h.typeText("hello wor")
	h.key(tcell.KeyEnter)
	h.waitText("matching in", 2*time.Second)
	h.waitFor("the mouse", func() bool {
		on := false
		h.onUI(func() { on = h.em.mouseOn })
		return on
	}, 3*time.Second)
	got := h.copied()
	x, y := h.where("echo hello world  (starts")
	h.drag(x, y, x+14, y)
	h.waitFor("the copy", func() bool { h.onUI(func() {}); return len(*got) == 1 }, 2*time.Second)
	if (*got)[0] != "echo hello worl" {
		t.Errorf("copied %q", (*got)[0])
	}
}

func TestTheClipboardProgramIsUsedWhenThereIsOne(t *testing.T) {
	bin := t.TempDir()
	out := bin + "/got"
	fake := "#!/bin/sh\n/bin/cat > " + out + "\n"
	if err := writeFileBytes(bin+"/xclip", []byte(fake)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin+"/xclip", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if !copyViaTool("some text\nover two lines") {
		t.Fatal("the program was not used")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := readFileBytes(out)
		if string(got) == "some text\nover two lines" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("it was given %q", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Setenv("PATH", t.TempDir())
	if copyViaTool("x") {
		t.Error("copied with no program to do it")
	}
}
