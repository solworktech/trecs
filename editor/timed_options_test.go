package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// What a timed annotation has besides its text: whether playback is paused when it is shown, and whether its first line is in the list of
// commands. And what goes with them: the modal being opaque, "Jump to annotation" in the list, a pause that keeps what is up, and Ctrl+E in a
// text input.

// ---- the two selects ------------------------------------------------------------------------------------------------------------

func (h *harness) pauseAndNav() (pause, nav string) {
	h.onUI(func() {
		_, pause = h.em.timedModal.pause.GetCurrentOption()
		_, nav = h.em.timedModal.nav.GetCurrentOption()
	})
	return
}

func TestTheModalOffersPausePlaybackStartingOnYesAndShowInNavMenuStartingOnNo(t *testing.T) {
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.timedModalHas("Pause playback?  Yes ▾", "Show in nav menu?  No ▾")
	if p, n := h.pauseAndNav(); p != "Yes" || n != "No" {
		t.Errorf("starts on %q and %q", p, n)
	}
}

func TestPauseAndNavAreWrittenAndEditingShowsThem(t *testing.T) {
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.setTimeField("00:03")
	h.typeText("Left alone")
	h.key(tcell.KeyCtrlS)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)

	h.pausePlayback()
	h.onUI(func() { h.em.openTimedModalAt(6000) })
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	h.onUI(func() {
		h.em.timedModal.text.SetText("Both changed", true)
		h.em.timedModal.pause.SetCurrentOption(1) // No
		h.em.timedModal.nav.SetCurrentOption(1)   // Yes
	})
	h.key(tcell.KeyCtrlS)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)

	got := map[string]libtrecs.Annotation{}
	all, _ := h.timedOnDisk()
	for _, a := range all {
		got[a.Text] = a
	}
	if a := got["Left alone"]; !a.PausePlayback || a.ShowInNav {
		t.Errorf("the defaults are written as they are: pause %v, nav %v", a.PausePlayback, a.ShowInNav)
	}
	if a := got["Both changed"]; a.PausePlayback || !a.ShowInNav {
		t.Errorf("what was chosen is written: pause %v, nav %v", a.PausePlayback, a.ShowInNav)
	}
	var path string
	h.onUI(func() { path = h.em.filePath })
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"pausePlayback":true`) || !strings.Contains(string(raw), `"showInNav":true`) {
		t.Errorf("the file lacks the fields the web player reads:\n%s", raw)
	}

	// each is what editing shows
	h.pausePlayback()
	h.onUI(func() { h.em.openTimedModalAt(6000) })
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	if p, n := h.pauseAndNav(); p != "No" || n != "Yes" {
		t.Errorf("editing the changed one shows %q and %q", p, n)
	}
	h.onUI(func() { h.em.openTimedModalAt(3000) })
	if p, n := h.pauseAndNav(); p != "Yes" || n != "No" {
		t.Errorf("editing the other shows %q and %q", p, n)
	}
}

// One written before there was a choice has neither field: it did not pause, and was not in the nav menu.
func TestAnAnnotationWithNeitherFieldIsNoAndNoWhenEdited(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("OLD ONE", 30))
	h.pausePlayback()
	h.onUI(func() { h.em.openTimedModalAt(1000) })
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	if p, n := h.pauseAndNav(); p != "No" || n != "No" {
		t.Errorf("an old annotation is %q and %q", p, n)
	}
}

func TestTheSelectsAreOnTheTabRoundAndOpenWithEnter(t *testing.T) {
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.onUI(func() { h.em.app.SetFocus(h.em.timedModal.pause) })
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	h.waitFor("No chosen", func() bool { p, _ := h.pauseAndNav(); return p == "No" }, 2*time.Second)
	h.key(tcell.KeyTab)
	h.waitFor("focus on the nav select", func() bool { return h.focusedIs(func(m *timedModal) tview.Primitive { return m.nav }) }, 2*time.Second)
}

// fillPlayback puts a character that nothing else on the screen has in every cell of the playback, so that any of it showing in a
// modal is seen.
func (h *harness) fillPlayback() {
	h.onUI(func() {
		_, _, w, ht := h.em.playbackView.GetInnerRect()
		line := strings.Repeat("§", w)
		h.em.playbackView.SetText(strings.TrimSuffix(strings.Repeat(line+"\n", ht), "\n"))
	})
}

// nothingOfThePlaybackIn fails for every cell of the rect that shows the filler.
func (h *harness) nothingOfThePlaybackIn(what string, x, y, w, ht int) {
	h.t.Helper()
	lines := strings.Split(h.screen(), "\n")
	for row := y; row < y+ht && row < len(lines); row++ {
		r := []rune(lines[row])
		for col := x; col < x+w && col < len(r); col++ {
			if r[col] == '§' {
				h.t.Errorf("the playback shows in %s at column %d of row %d: %q", what, col, row, string(r[x:min(x+w, len(r))]))
				break
			}
		}
	}
}

func TestNothingOfThePlaybackShowsInTheTimedModal(t *testing.T) {
	h := newStretched(t, twoTimed)
	h.pausePlayback()
	h.fillPlayback()
	for _, open := range []func(){
		func() {
			h.key(tcell.KeyCtrlA)
			h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
			h.key(tcell.KeyCtrlN)
		}, // adding
		func() {
			h.key(tcell.KeyCtrlA)
			h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
			h.key(tcell.KeyEnter)
		}, // editing: with Delete
	} {
		h.pausePlayback()
		open()
		h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
		h.fillPlayback()
		h.waitFor("the filler to be drawn", func() bool { return strings.Contains(h.screen(), "§") }, 2*time.Second)
		var x, y, w, ht int
		h.onUI(func() { x, y, w, ht = h.em.timedModal.box.GetRect() })
		h.nothingOfThePlaybackIn("the modal", x, y, w, ht)
		h.key(tcell.KeyEscape)
		h.waitFor("the modal to go", func() bool { return h.front() != timedNotePage }, 2*time.Second)
	}
}

func TestNothingOfThePlaybackShowsInTheList(t *testing.T) {
	h := newStretched(t, twoTimed)
	h.openTimedList()
	h.fillPlayback()
	h.waitFor("the filler to be drawn", func() bool { return strings.Contains(h.screen(), "§") }, 2*time.Second)
	var x, y, w, ht int
	h.onUI(func() { x, y, w, ht = h.em.timedListModal.box.GetRect() })
	h.nothingOfThePlaybackIn("the list", x, y, w, ht)
}

// ---- Jump to annotation ----------------------------------------------------------------------------------------------------------

func twoTimedLate(cmds []libtrecs.Command) {
	cmds[3].TimedAnnotations = []libtrecs.Annotation{
		{ID: "tm_a", Timestamp: 3000, Text: "EARLIER ONE", DurationSeconds: 30, Kind: libtrecs.AnnotationKindTimed},
		{ID: "tm_b", Timestamp: 8000, Text: "LATER ONE", DurationSeconds: 30, Kind: libtrecs.AnnotationKindTimed},
	}
}

func TestTheListHasAJumpToAnnotationButton(t *testing.T) {
	h := newStretched(t, twoTimedLate)
	h.openTimedList()
	h.timedModalHas("Jump to annotation", "Add New", "Discard")
}

func TestJumpToAnnotationSeeksToItsExactTimeAndShowsIt(t *testing.T) {
	h := newStretched(t, twoTimedLate)
	h.waitFor("playback to start", func() bool { return h.position() > 200 }, 3*time.Second)
	h.openTimedList()
	h.key(tcell.KeyDown) // the later one
	h.key(tcell.KeyCtrlG)
	h.waitFor("the list to go", func() bool { return h.front() == "playback" }, 2*time.Second)
	h.waitFor("the annotation at the time jumped to", func() bool { return strings.Contains(h.screen(), "LATER ONE") }, 3*time.Second)
	if pos := h.position(); pos < 8000 || pos > 9500 {
		t.Errorf("playback is at %d ms: it was sent to 8000", pos)
	}
	if strings.Contains(h.screen(), "EARLIER ONE") {
		t.Error("the one before it was shown")
	}
	if h.playbackState() != libtrecs.PlaybackPlaying {
		t.Errorf("playback is %v: it goes on from the annotation", h.playbackState())
	}
	// the screen is the command's, as it had drawn by then: not an empty one
	var host int
	var input string
	h.onUI(func() { host = h.em.timedHost(8000); input = strings.Split(h.em.commands[host].InputText, "\n")[0] })
	if !strings.Contains(h.screen(), input) {
		t.Errorf("the screen lacks the command that holds the time, %q:\n%s", input, h.screen())
	}
}

func TestJumpToAnnotationGoesBackToo(t *testing.T) {
	h := newStretched(t, twoTimedLate)
	h.waitFor("playback to be later", func() bool { return h.position() > 9000 }, 12*time.Second)
	h.openTimedList() // the first row, the earlier one, is in focus
	h.timedModalHas("Jump to annotation")
	h.key(tcell.KeyCtrlG)
	h.waitFor("playback to be where it was sent", func() bool { p := h.position(); return p >= 3000 && p < 5000 }, 3*time.Second)
	h.waitFor("the annotation at 3 s", func() bool { return strings.Contains(h.screen(), "EARLIER ONE") }, 3*time.Second)
	if strings.Contains(h.screen(), "LATER ONE") {
		t.Error("one that is after the time was shown")
	}
}

func TestTheJumpButtonIsAButtonToo(t *testing.T) {
	h := newStretched(t, twoTimedLate)
	h.openTimedList()
	var activate func()
	h.onUI(func() { activate = func() { h.em.timedListJump() } })
	h.onUI(activate)
	h.waitFor("playback", func() bool { return h.front() == "playback" }, 2*time.Second)
	h.waitFor("the first one", func() bool { return strings.Contains(h.screen(), "EARLIER ONE") }, 3*time.Second)
}

// ---- what is up stays while playback is paused --------------------------------------------------------------------------------------

func TestAnAnnotationStaysWhilePlaybackIsPausedAndGoesAfterItsDurationOnceResumed(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("HELD NOTE", 2))
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "HELD NOTE") }, 4*time.Second)
	h.pausePlayback()
	time.Sleep(2800 * time.Millisecond) // longer than the two seconds it is for
	if !strings.Contains(h.screen(), "HELD NOTE") {
		t.Fatal("it went while playback was paused")
	}
	h.onUI(func() { h.em.player.Resume() })
	resumed := time.Now()
	h.waitFor("it to go once playback is going on", func() bool { return !strings.Contains(h.screen(), "HELD NOTE") }, 4*time.Second)
	if d := time.Since(resumed); d > 2500*time.Millisecond {
		t.Errorf("it stayed %v after playback was resumed", d)
	}
}

func TestACountedTimeIsNotLostByPausing(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("HELD NOTE", 3))
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "HELD NOTE") }, 4*time.Second)
	time.Sleep(1200 * time.Millisecond) // it has been up for about 1.5 s of its 3
	h.pausePlayback()
	time.Sleep(1500 * time.Millisecond)
	h.onUI(func() { h.em.player.Resume() })
	time.Sleep(700 * time.Millisecond) // 2.2 s of the 3 are counted: it is still up
	if !strings.Contains(h.screen(), "HELD NOTE") {
		t.Error("the time it was up before the pause was counted twice, or the pause was counted")
	}
	h.waitGone("HELD NOTE", 4*time.Second)
}

func TestTickTimedCountsOnlyWhilePlaybackIsNotPausedByTheViewer(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	h.onUI(func() {
		em := h.em
		em.showTimed(&libtrecs.Annotation{Text: "T", DurationSeconds: 3, Kind: libtrecs.AnnotationKindTimed})
		start := em.timedTickAt
		em.tickTimed(true, start.Add(10*time.Second)) // paused by the viewer: nothing is counted
		if !em.timedShown || em.timedRemaining != 3*time.Second {
			t.Errorf("paused ten seconds: shown %v, %v left", em.timedShown, em.timedRemaining)
		}
		em.tickTimed(false, start.Add(10*time.Second+time.Second)) // one second of playing
		if !em.timedShown || em.timedRemaining != 2*time.Second {
			t.Errorf("one second of playing: shown %v, %v left", em.timedShown, em.timedRemaining)
		}
		em.tickTimed(false, start.Add(10*time.Second+4*time.Second))
		if em.timedShown {
			t.Error("still up after its duration was played")
		}
	})
}

func TestTickTimedCountsWhilePlaybackIsHeldByTheAnnotationItself(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	h.onUI(func() {
		em := h.em
		em.showTimed(&libtrecs.Annotation{Text: "T", DurationSeconds: 3, Kind: libtrecs.AnnotationKindTimed})
		em.timedPausedIt = true // as if it had paused playback
		start := em.timedTickAt
		em.tickTimed(true, start.Add(time.Second))
		if !em.timedShown || em.timedRemaining != 2*time.Second {
			t.Errorf("one second of its own pause: shown %v, %v left", em.timedShown, em.timedRemaining)
		}
		em.tickTimed(true, start.Add(4*time.Second))
		if em.timedShown || em.player.GetPlaybackState() != libtrecs.PlaybackPlaying {
			t.Errorf("after its duration: shown %v, playback %v", em.timedShown, em.player.GetPlaybackState())
		}
	})
}

// ---- Pause playback ---------------------------------------------------------------------------------------------------------------

func pausingAtOneSecond(seconds int) func(cmds []libtrecs.Command) {
	return func(cmds []libtrecs.Command) {
		cmds[3].TimedAnnotations = []libtrecs.Annotation{{ID: "tm_p", Timestamp: 1000, Text: "PAUSING NOTE", DurationSeconds: seconds, Kind: libtrecs.AnnotationKindTimed, PausePlayback: true}}
	}
}

func TestAnAnnotationThatPausesHoldsPlaybackForItsDurationAndThenResumesIt(t *testing.T) {
	h := newStretched(t, pausingAtOneSecond(2))
	h.waitFor("playback to be paused by it", func() bool {
		return h.playbackState() == libtrecs.PlaybackPaused && strings.Contains(h.screen(), "PAUSING NOTE")
	}, 4*time.Second)
	shown, pos := time.Now(), h.position()
	time.Sleep(1200 * time.Millisecond) // within its two seconds
	if !strings.Contains(h.screen(), "PAUSING NOTE") || h.playbackState() != libtrecs.PlaybackPaused {
		t.Fatal("it ended before its duration")
	}
	if p := h.position(); p != pos {
		t.Errorf("playback moved while it was held: %d to %d", pos, p)
	}
	h.waitFor("playback to go on by itself", func() bool { return h.playbackState() == libtrecs.PlaybackPlaying }, 3*time.Second)
	if d := time.Since(shown); d < 1800*time.Millisecond || d > 3500*time.Millisecond {
		t.Errorf("it held playback for %v, not about 2 s", d)
	}
	h.waitGone("PAUSING NOTE", 1*time.Second)
	time.Sleep(400 * time.Millisecond)
	if strings.Contains(h.screen(), "PAUSING NOTE") || h.playbackState() != libtrecs.PlaybackPlaying {
		t.Errorf("it came back, or paused playback again: %v", h.playbackState())
	}
}

func TestResumingByHandWhileAnAnnotationHoldsPlaybackLetsItRunItsCourse(t *testing.T) {
	h := newStretched(t, pausingAtOneSecond(30))
	h.waitFor("playback to be paused by it", func() bool {
		return h.playbackState() == libtrecs.PlaybackPaused && strings.Contains(h.screen(), "PAUSING NOTE")
	}, 4*time.Second)
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone) // the viewer resumes it themselves
	h.waitFor("playback to resume", func() bool { return h.playbackState() == libtrecs.PlaybackPlaying }, 2*time.Second)
	h.waitFor("the annotation to stop counting itself as holding playback", func() (ok bool) {
		h.onUI(func() { ok = !h.em.timedPausedIt })
		return
	}, 2*time.Second)
	h.pausePlayback() // and pauses again: now it is the viewer's pause, which the annotation outlasts
	time.Sleep(600 * time.Millisecond)
	if !strings.Contains(h.screen(), "PAUSING NOTE") {
		t.Error("it went during a pause of the viewer's")
	}
}

func TestDismissingAnAnnotationThatPausedResumesPlayback(t *testing.T) {
	h := newStretched(t, pausingAtOneSecond(30))
	h.waitFor("playback to be paused by it", func() bool {
		return h.playbackState() == libtrecs.PlaybackPaused && strings.Contains(h.screen(), "PAUSING NOTE")
	}, 4*time.Second)
	h.key(tcell.KeyCtrlX)
	h.waitFor("it to go", func() bool { return !strings.Contains(h.screen(), "PAUSING NOTE") }, 2*time.Second)
	h.waitFor("playback to go on", func() bool { return h.playbackState() == libtrecs.PlaybackPlaying }, 2*time.Second)
}

func TestDismissingAnAnnotationThatDidNotPauseLeavesPlaybackPaused(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("PLAIN NOTE", 30))
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "PLAIN NOTE") }, 4*time.Second)
	h.pausePlayback() // by the viewer
	h.key(tcell.KeyCtrlX)
	h.waitFor("it to go", func() bool { return !strings.Contains(h.screen(), "PLAIN NOTE") }, 2*time.Second)
	time.Sleep(300 * time.Millisecond)
	if h.playbackState() != libtrecs.PlaybackPaused {
		t.Errorf("playback is %v: the viewer had paused it, and the annotation did not", h.playbackState())
	}
}

// ---- Ctrl+E in a text input -------------------------------------------------------------------------------------------------------

// editorReturns stands in for $EDITOR: the file it is given is left with text.
func editorReturns(t *testing.T, text string) *[]string {
	t.Helper()
	var seen []string
	old := externalEditorRun
	externalEditorRun = func(editorCmd, path string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen = append(seen, string(b))
		return os.WriteFile(path, []byte(text), 0o644)
	}
	t.Cleanup(func() { externalEditorRun = old })
	return &seen
}

func (h *harness) timedText() (text string) {
	h.onUI(func() { text = h.em.timedModal.text.GetText() })
	return
}

func TestCtrlEInTheTextOfTheTimedModalOpensItInTheEditorAndTakesWhatComesBack(t *testing.T) {
	seen := editorReturns(t, "written in the editor\nwith **two** lines\n")
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.typeText("typed here")
	h.key(tcell.KeyCtrlE)
	h.waitFor("what the editor left", func() bool { return h.timedText() == "written in the editor\nwith **two** lines" }, 3*time.Second)
	if len(*seen) != 1 || (*seen)[0] != "typed here" {
		t.Errorf("the editor was given %q", *seen)
	}
	if h.front() != timedNotePage {
		t.Errorf("the modal is not where it was: %s", h.front())
	}
	h.setTimeField("00:02") // and what is saved is it
	h.key(tcell.KeyCtrlS)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	if all, _ := h.timedOnDisk(); len(all) != 1 || all[0].Text != "written in the editor\nwith **two** lines" {
		t.Errorf("on disk: %+v", all)
	}
}

func TestCtrlEInAnInputFieldTakesTheFirstLineAndWhatItAccepts(t *testing.T) {
	editorReturns(t, "1x2y\nsecond line\n")
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.onUI(func() { h.em.app.SetFocus(h.em.timedModal.duration) })
	h.key(tcell.KeyCtrlE)
	h.waitFor("a duration of digits", func() (ok bool) {
		h.onUI(func() { ok = h.em.timedModal.duration.GetText() == "12" })
		return
	}, 3*time.Second)
	h.onUI(func() { h.em.app.SetFocus(h.em.timedModal.time) })
	h.key(tcell.KeyCtrlE)
	h.waitFor("a time of digits and colons", func() (ok bool) {
		h.onUI(func() { ok = h.em.timedModal.time.GetText() == "12" })
		return
	}, 3*time.Second)
}

func TestCtrlEInTheCueCardModalAndTheEditPageToo(t *testing.T) {
	editorReturns(t, "from the editor\n")
	h := newHarness(t, nil)
	h.pausePlayback()
	h.openModalOn(0)
	h.waitFor("the card modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
	h.onUI(func() { h.em.app.SetFocus(h.em.cueModal.text) })
	h.key(tcell.KeyCtrlE)
	h.waitFor("the card text", func() (ok bool) {
		h.onUI(func() { ok = h.em.cueModal.text.GetText() == "from the editor" })
		return
	}, 3*time.Second)
	h.key(tcell.KeyEscape)
	h.waitFor("the playback page", func() bool { return h.front() != cueCardPage }, 2*time.Second)

	// The edit page: the focus is on one of its fields, and it is that one that is edited - the note, not the input before it.
	h.onUI(func() { h.em.cancelAnnotation() })
	h.key(tcell.KeyCtrlE) // on the playback page it is what it was: edit the command being played
	h.waitFor("the edit page", func() bool { return h.front() == "edit" }, 2*time.Second)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyTab)
	h.waitFor("focus on the note", func() (ok bool) {
		h.onUI(func() { ok = h.em.app.GetFocus() == h.em.annotationField })
		return
	}, 2*time.Second)
	var before string
	h.onUI(func() { before = h.em.inputField.GetText() })
	h.key(tcell.KeyCtrlE)
	h.waitFor("the note from the editor", func() (ok bool) {
		h.onUI(func() { ok = h.em.annotationField.GetText() == "from the editor" })
		return
	}, 3*time.Second)
	h.onUI(func() {
		if h.em.inputField.GetText() != before {
			t.Errorf("the input field was changed: %q", h.em.inputField.GetText())
		}
	})
}

func TestCtrlEWhereThereIsNoTextInputIsWhatItWas(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlE)
	h.waitFor("the edit page", func() bool { return h.front() == "edit" }, 2*time.Second)
}

func TestIfTheEditorFailsTheStatusSaysSoAndTheTextIsKept(t *testing.T) {
	old := externalEditorRun
	externalEditorRun = func(string, string) error { return os.ErrNotExist }
	t.Cleanup(func() { externalEditorRun = old })
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.typeText("kept")
	h.key(tcell.KeyCtrlE)
	h.waitFor("the status to say", func() bool { return strings.Contains(h.status(), "External editor failed") }, 3*time.Second)
	if h.timedText() != "kept" {
		t.Errorf("the text is %q", h.timedText())
	}
}
