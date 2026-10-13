package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// ---- time and layout -------------------------------------------------------------------------------------------------------

func TestParseClockText(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "5": 5000, "65": 65000, "1:05": 65000, "01:05": 65000, " 1:05 ": 65000, "10:00": 600000, "1:02:03": 3723000, "0:00": 0} {
		if got, err := parseClockText(in); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", " ", ":", "1:", ":30", "1:60", "1:2:3:4", "abc", "-1", "1:-5", "+5", "1.5", "1:02:60", "1:60:00", "1 5"} {
		if got, err := parseClockText(in); err == nil {
			t.Errorf("%q was taken for %d ms", in, got)
		}
	}
}

func TestATimedAnnotationsBoxIsAtTheBottomLeftNoWiderThan60PercentAndWrapped(t *testing.T) {
	l := layoutTimed("A short one", 100, 30)
	if l.left != 1 || l.top+l.h != 29 {
		t.Errorf("not at the bottom left: %+v", l)
	}
	l = layoutTimed(strings.Repeat("word ", 80), 100, 30)
	if l.w > 60 || len(l.lines) < 4 || l.left < 0 || l.top < 0 || l.top+l.h > 30 {
		t.Errorf("a long text: %d wide (at most 60), %d lines: %+v", l.w, len(l.lines), l)
	}
	if l := layoutTimed(strings.Repeat("line\n", 80), 80, 12); l.h > 12 || l.lines[len(l.lines)-1] != "…" {
		t.Errorf("a tall one is cut: %+v", l)
	}
	if l := layoutTimed("x", 10, 3); l.w != 0 {
		t.Errorf("no room: %+v", l)
	}
	// a link cannot be clicked here: it is its label, and its address
	j := strings.Join(layoutTimed("See [the docs](https://example.com/docs) now", 100, 30).lines, "\n")
	if !strings.Contains(j, cueLinkTag+"the docs"+cueTextTag) || !strings.Contains(j, "(https://example.com/docs)") || strings.Contains(j, cueButtonTag) {
		t.Errorf("%q", j)
	}
}

// ---- the editor ---------------------------------------------------------------------------------------------------------------

// The fixture's timeline is 1.3 s long. Stretched eight times it is 10 s, and a time is a second of it.
func stretchedFixture(t *testing.T, factor float64) string {
	t.Helper()
	data, err := os.ReadFile(cueFixture)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatal(err)
		}
		if ts, ok := obj["timestamp"].(float64); ok {
			obj["timestamp"] = float64(int64(ts * factor))
		}
		b, _ := json.Marshal(obj)
		out = append(out, string(b))
	}
	path := filepath.Join(t.TempDir(), "stretched.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newStretched(t *testing.T, mutate func(cmds []libtrecs.Command)) *harness {
	t.Helper()
	return newHarnessFile(t, stretchedFixture(t, 8), mutate)
}

func (h *harness) position() (ms int64) {
	h.onUI(func() { ms = h.em.player.Position() })
	return
}

func (h *harness) playbackState() (s libtrecs.PlaybackState) {
	h.onUI(func() { s = h.em.player.GetPlaybackState() })
	return
}

// find is where a word is on the screen: its column and its row.
func (h *harness) find(word string) (x, y int, ok bool) {
	for row, l := range strings.Split(h.screen(), "\n") {
		if i := strings.Index(l, word); i >= 0 {
			return len([]rune(l[:i])), row, true
		}
	}
	return 0, 0, false
}

// styleOf is how a word on the screen is drawn.
func (h *harness) styleOf(word string) (found bool, fg tcell.Color, attr tcell.AttrMask) {
	type res struct {
		ok   bool
		fg   tcell.Color
		attr tcell.AttrMask
	}
	out := make(chan res, 1)
	h.em.app.QueueUpdate(func() {
		cells, w, ht := h.sim.GetContents()
		for y := 0; y < ht; y++ {
			for x := 0; x+len(word) <= w; x++ {
				match := true
				for i, r := range []rune(word) {
					c := cells[y*w+x+i].Runes
					if len(c) == 0 || c[0] != r {
						match = false
						break
					}
				}
				if match {
					f, _, a := cells[y*w+x].Style.Decompose()
					out <- res{true, f, a}
					return
				}
			}
		}
		out <- res{}
	})
	r := <-out
	return r.ok, r.fg, r.attr
}

var codeBlue = tcell.NewHexColor(0x5fafff)

func (h *harness) timedModalHas(want ...string) {
	h.t.Helper()
	for _, w := range want {
		if !strings.Contains(h.screen(), w) {
			h.t.Errorf("the modal does not show %q. The screen:\n%s", w, h.screen())
		}
	}
}

func (h *harness) setTimeField(text string) {
	h.onUI(func() { h.em.timedModal.time.SetText(text) })
}

func (h *harness) timedOnDisk() (all []libtrecs.Annotation, holder map[string]int) {
	h.t.Helper()
	holder = map[string]int{}
	for i, c := range h.reparsed() {
		for _, a := range c.TimedAnnotations {
			all = append(all, a)
			holder[a.Text] = i
		}
	}
	return
}

func TestCtrlAOnThePlaybackScreenOpensTheAnnotationModalAtTheCurrentTimeAndPausesPlayback(t *testing.T) {
	h := newStretched(t, nil)
	h.waitFor("playback to be past a second and a half", func() bool { return h.position() > 1500 }, 5*time.Second)
	h.waitText("Timed Annotations", 1*time.Second) // the legend
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	h.timedModalHas("Add annotation", "Time (m:ss or h:mm:ss)", "Annotation - **bold** *italic* `code` [link](url)", "Display duration (seconds)", "Save  ^S", "Discard  Esc")
	if strings.Contains(h.screen(), "Delete annotation") {
		t.Error("a Delete button where there is nothing to delete")
	}
	if h.playbackState() != libtrecs.PlaybackPaused {
		t.Errorf("playback goes on behind the modal: %v", h.playbackState())
	}
	// the time is where playback was: a second of it
	var field string
	var pos int64
	h.onUI(func() { field = h.em.timedModal.time.GetText(); pos = h.em.player.Position() })
	if want := libtrecs.ClockText(pos); field != want || pos < 1500 || pos > 3000 {
		t.Errorf("the time is %q, playback is at %d ms (%q)", field, pos, want)
	}
	h.key(tcell.KeyEscape)
	h.waitFor("the modal to close and playback to go on", func() bool { return h.front() == "playback" && h.playbackState() == libtrecs.PlaybackPlaying }, 2*time.Second)
}

func TestTabGoesRoundTheModalFromTheTextAndTheTimeCanBeChanged(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	if !h.focusedIs(func(m *timedModal) tview.Primitive { return m.text }) {
		t.Fatal("focus should start on the text: what is typed first is the annotation")
	}
	for _, step := range []struct {
		name string
		p    func(m *timedModal) tview.Primitive
	}{
		{"duration", func(m *timedModal) tview.Primitive { return m.duration }},
		{"Save", func(m *timedModal) tview.Primitive { return m.save }},
		{"Discard (nothing to delete)", func(m *timedModal) tview.Primitive { return m.discard }},
		{"time", func(m *timedModal) tview.Primitive { return m.time }},
		{"style", func(m *timedModal) tview.Primitive { return m.style }},
		{"pause", func(m *timedModal) tview.Primitive { return m.pause }},
		{"nav", func(m *timedModal) tview.Primitive { return m.nav }},
		{"text again", func(m *timedModal) tview.Primitive { return m.text }},
	} {
		h.key(tcell.KeyTab)
		h.waitFor("focus on "+step.name, func() bool { return h.focusedIs(step.p) }, 2*time.Second)
	}
	for _, back := range []struct {
		name string
		p    func(m *timedModal) tview.Primitive
	}{
		{"nav", func(m *timedModal) tview.Primitive { return m.nav }},
		{"pause", func(m *timedModal) tview.Primitive { return m.pause }},
		{"style", func(m *timedModal) tview.Primitive { return m.style }},
	} {
		h.key(tcell.KeyBacktab)
		h.waitFor("Shift+Tab to the "+back.name, func() bool { return h.focusedIs(back.p) }, 2*time.Second)
	}
	h.key(tcell.KeyBacktab)
	h.waitFor("Shift+Tab to the time", func() bool { return h.focusedIs(func(m *timedModal) tview.Primitive { return m.time }) }, 2*time.Second)
	// the time takes digits and colons, and nothing else
	h.setTimeField("")
	h.typeText("0:0x7:")
	h.typeText("5")
	h.waitFor("the field to have what is accepted of 0:0x7:5", func() (ok bool) {
		h.onUI(func() { ok = h.em.timedModal.time.GetText() == "0:07:5" })
		return
	}, 2*time.Second)
}

func (h *harness) focusedIs(p func(m *timedModal) tview.Primitive) (is bool) {
	h.onUI(func() { is = h.em.app.GetFocus() == p(h.em.timedModal) })
	return
}

func TestSavingWritesTheAnnotationAtItsTimeInTheCommandThatTimeIsIn(t *testing.T) {
	h := newStretched(t, nil) // its commands begin at 0.2, 0.3, 0.3, 0.3, 10 and 10 s: 2 s is in the fourth
	h.pausePlayback()
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	h.setTimeField("00:02")
	h.typeText("Remember: **check** the `ls` output")
	h.key(tcell.KeyTab) // the duration
	h.key(tcell.KeyBackspace2)
	h.typeText("3")
	h.key(tcell.KeyCtrlS)
	h.waitFor("the recording to be written and playback to resume", func() bool { return h.front() == "playback" }, 5*time.Second)

	all, holder := h.timedOnDisk()
	if len(all) != 1 {
		t.Fatalf("%d timed annotations on disk", len(all))
	}
	a := all[0]
	if a.Timestamp != 2000 || a.DurationSeconds != 3 || a.Kind != libtrecs.AnnotationKindTimed || a.Text != "Remember: **check** the `ls` output" || a.ID == "" {
		t.Errorf("%+v", a)
	}
	if holder[a.Text] != 3 {
		t.Errorf("it is with command %d: 2 s is in the fourth", holder[a.Text]+1)
	}
	var backup string
	h.onUI(func() { backup = h.em.filePath + ".backup" })
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("no backup: %v", err)
	}
	if c := h.reparsed(); c[0].HasAnnotation || c[3].HasAnnotation || c[0].HasIntroCard {
		t.Error("it became a note, or a card")
	}
	h.onUI(func() {
		if n := len(h.em.commands[3].TimedAnnotations); n != 1 {
			t.Errorf("what the editor holds after the reload: %d", n)
		}
		if h.em.pages.HasPage(timedNotePage) {
			t.Error("the modal's page is still there")
		}
	})
}

func TestSaveSaysWhatIsWrongAndWritesNothing(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)

	h.key(tcell.KeyCtrlS) // nothing typed
	h.waitText("Enter the text of the annotation", 2*time.Second)
	h.typeText("text")
	h.setTimeField("1:75")
	h.key(tcell.KeyCtrlS)
	h.waitText("minutes and seconds are under 60", 2*time.Second)
	h.setTimeField("59:00")
	h.key(tcell.KeyCtrlS)
	h.waitText("only 00:10 long", 2*time.Second)
	h.setTimeField("")
	h.key(tcell.KeyCtrlS)
	h.waitText("a time is minutes and seconds", 2*time.Second)
	h.setTimeField("00:03")
	h.onUI(func() { h.em.timedModal.duration.SetText("0") })
	h.key(tcell.KeyCtrlS)
	h.waitText("1 or more", 2*time.Second)
	if h.front() != timedNotePage {
		t.Errorf("the modal went: %s", h.front())
	}
	if all, _ := h.timedOnDisk(); len(all) != 0 {
		t.Errorf("something was written: %+v", all)
	}
}

func TestAnAnnotationAtTheSecondIsEditedReplacedAndDeleted(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	h.setTimeField("00:02")
	h.typeText("First version")
	h.key(tcell.KeyCtrlS)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	first, _ := h.timedOnDisk()
	if len(first) != 1 {
		t.Fatalf("%+v", first)
	}

	// opened at that second, the modal shows it, and a Delete button; the time can be moved to another second, which has none
	h.pausePlayback()
	h.onUI(func() { h.em.openTimedModalAt(2300) })
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	h.timedModalHas("First version", "Delete annotation", "Editing the annotation at this second")
	h.setTimeField("00:05")
	h.waitFor("Delete to go", func() bool { return !strings.Contains(h.screen(), "Delete annotation") }, 2*time.Second)
	h.setTimeField("00:02")
	h.waitFor("Delete to come back, and its warning", func() bool {
		return strings.Contains(h.screen(), "Delete annotation") && strings.Contains(h.screen(), "saving replaces it")
	}, 2*time.Second)

	// saving here replaces it: the same annotation, with its id
	h.onUI(func() { h.em.timedModal.text.SetText("Second version", true) })
	h.key(tcell.KeyCtrlS)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	second, _ := h.timedOnDisk()
	if len(second) != 1 || second[0].Text != "Second version" || second[0].ID != first[0].ID || second[0].Timestamp != 2000 {
		t.Errorf("replaced: %+v (it was %+v)", second, first)
	}

	// Delete, by Tab to it past the duration and Save
	h.pausePlayback()
	h.onUI(func() { h.em.openTimedModalAt(2000) })
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	for i := 0; i < 3; i++ {
		h.key(tcell.KeyTab)
	}
	h.waitFor("focus on Delete", func() bool { return h.focusedIs(func(m *timedModal) tview.Primitive { return m.del }) }, 2*time.Second)
	h.key(tcell.KeyEnter)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	if all, _ := h.timedOnDisk(); len(all) != 0 {
		t.Errorf("it is still on disk: %+v", all)
	}
}

// ---- in playback ----------------------------------------------------------------------------------------------------------------

func timedAtOneSecond(text string, seconds int) func(cmds []libtrecs.Command) {
	return func(cmds []libtrecs.Command) {
		cmds[3].TimedAnnotations = []libtrecs.Annotation{{ID: "tm_a", Timestamp: 1000, Text: text, DurationSeconds: seconds, Kind: libtrecs.AnnotationKindTimed}}
	}
}

func TestATimedAnnotationAppearsAtItsTimeAtTheBottomLeftGoesAfterItsDurationAndPlaybackNeverStops(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("TIMED NOTE", 2))
	tr := h.trace("TIMED NOTE")
	t.Cleanup(func() { close(tr.stop) })
	h.waitFor("the annotation, and the trace to have seen it", func() bool { _, seen := tr.at("TIMED NOTE"); return seen }, 4*time.Second)
	shown, _ := tr.at("TIMED NOTE")
	if pos := h.position(); pos < 900 || pos > 1700 {
		t.Errorf("shown at %d ms: it is for 1000", pos)
	}
	x, y, _ := h.find("TIMED NOTE")
	if x > 6 || y < 30 {
		t.Errorf("at column %d, row %d of 120 by 40: it is for the bottom left", x, y)
	}
	if h.playbackState() != libtrecs.PlaybackPlaying {
		t.Errorf("playback is %v while it is up", h.playbackState())
	}
	if s := h.status(); !strings.Contains(s, "Dismiss Annotation") {
		t.Errorf("status: %q", s)
	}
	h.waitFor("it to go, after its two seconds", func() bool { return !strings.Contains(h.screen(), "TIMED NOTE") }, 4*time.Second)
	end := time.Now()
	if d := end.Sub(shown); d < 1700*time.Millisecond || d > 2800*time.Millisecond {
		t.Errorf("it was up for %v: it is for 2 s", d)
	}
	if s := h.status(); strings.Contains(s, "Dismiss Annotation") {
		t.Errorf("status afterwards: %q", s)
	}
	if h.playbackState() != libtrecs.PlaybackPlaying {
		t.Errorf("playback is %v", h.playbackState())
	}
}

func TestCtrlXDismissesATimedAnnotationAndSpaceStillPauses(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("TIMED NOTE", 30))
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "TIMED NOTE") }, 4*time.Second)
	// Space pauses: nothing about it holds playback
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitFor("playback to pause", func() bool { return h.playbackState() == libtrecs.PlaybackPaused }, 2*time.Second)
	if !strings.Contains(h.screen(), "TIMED NOTE") {
		t.Error("it went when playback was paused")
	}
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitFor("playback to resume", func() bool { return h.playbackState() == libtrecs.PlaybackPlaying }, 2*time.Second)
	// Ctrl+X dismisses it, and does not open the modal
	h.key(tcell.KeyCtrlX)
	h.waitFor("it to go", func() bool { return !strings.Contains(h.screen(), "TIMED NOTE") }, 2*time.Second)
	time.Sleep(150 * time.Millisecond)
	if h.front() != "playback" {
		t.Errorf("the modal opened to dismiss it: %s", h.front())
	}
	// Ctrl+A is for the timed annotations - the list of them, as there is one - with nothing to dismiss
	h.key(tcell.KeyCtrlA)
	h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
}

func TestAJumpTakesAwayAndSkipsTimedAnnotationsAndAReplayShowsThemAgain(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("TIMED NOTE", 30))
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "TIMED NOTE") }, 4*time.Second)
	// to the fifth command, at 10 s: the annotation is behind it
	h.key(tcell.KeyCtrlL)
	h.waitText("Jump to Playback", 2*time.Second)
	for i := 0; i < 4; i++ {
		h.key(tcell.KeyDown)
	}
	h.key(tcell.KeyCtrlP)
	h.waitFor("playback, without the annotation", func() bool { return h.front() == "playback" && !strings.Contains(h.screen(), "TIMED NOTE") }, 2*time.Second)
	time.Sleep(700 * time.Millisecond)
	if strings.Contains(h.screen(), "TIMED NOTE") {
		t.Fatal("what was passed over was shown")
	}
	// back to the first command: the annotation is ahead of it again
	h.waitFor("playback to have ended", func() bool { return strings.Contains(h.status(), "Finished") }, 4*time.Second)
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone) // replay
	h.waitFor("the annotation, again", func() bool { return strings.Contains(h.screen(), "TIMED NOTE") }, 5*time.Second)
}

func TestMarkdownInATimedAnnotationBoldItalicCodeAndLinks(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("Use **bold** and *italic* with `inline` and [the docs](https://example.com/docs).\n```sh\nblocked line\n```\nAfter", 30))
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "blocked line") }, 4*time.Second)
	for _, want := range []struct {
		word         string
		fg           tcell.Color
		bold, italic bool
		underline    bool
	}{
		{"bold", tcell.ColorWhite, true, false, false},
		{"italic", tcell.ColorWhite, false, true, false},
		{"inline", codeBlue, false, false, false}, // code is blue
		{"blocked", codeBlue, false, false, false},
		{"docs", tcell.NewHexColor(0x6fdc7b), false, false, false}, // a link: green, and its address after it
		{"After", tcell.ColorWhite, false, false, false},
	} {
		found, fg, attr := h.styleOf(want.word)
		if !found {
			t.Errorf("%q is not on the screen:\n%s", want.word, h.screen())
			continue
		}
		if fg != want.fg || (attr&tcell.AttrBold != 0) != want.bold || (attr&tcell.AttrItalic != 0) != want.italic || (attr&tcell.AttrUnderline != 0) != want.underline {
			t.Errorf("%q: colour %v bold %v italic %v underline %v; want %v %v %v %v", want.word, fg, attr&tcell.AttrBold != 0, attr&tcell.AttrItalic != 0, attr&tcell.AttrUnderline != 0, want.fg, want.bold, want.italic, want.underline)
		}
	}
	s := h.screen()
	for _, gone := range []string{"**", "`", "](", "```", "sh\n"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q is on the screen:\n%s", gone, s)
		}
	}
	if !strings.Contains(s, "(https://example.com/") || !strings.Contains(s, "docs).") { // the address wraps at a slash
		t.Errorf("a link cannot be clicked here: its address should be after its label:\n%s", s)
	}
}

func TestCodeInACueCardIsBlueToo(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("Run `ls -la` first:\n```\ncode block\n```\n**bold `emph`**", 30, libtrecs.CuePlacementIntro), true
	})
	h.waitFor("the card", func() bool { return strings.Contains(h.screen(), "code block") }, 4*time.Second)
	for _, want := range []struct {
		word string
		bold bool
	}{{"ls", false}, {"code block", false}, {"emph", true}} {
		found, fg, attr := h.styleOf(want.word)
		if !found || fg != codeBlue || (attr&tcell.AttrBold != 0) != want.bold {
			t.Errorf("%q: found %v, colour %v, bold %v; want blue, bold %v", want.word, found, fg, attr&tcell.AttrBold != 0, want.bold)
		}
	}
	if found, fg, _ := h.styleOf("Run"); !found || fg != tcell.ColorWhite {
		t.Errorf("the text around code is white: %v", fg)
	}
	if s := h.screen(); strings.Contains(s, "`") {
		t.Errorf("a backtick is on the screen:\n%s", s)
	}
}

// What Ctrl+A did - show or hide the annotation of the command being played - is Ctrl+T.
func TestCtrlTShowsAndHidesTheCommandsAnnotationAsCtrlADid(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[5].Annotation, cmds[5].HasAnnotation = libtrecs.Annotation{ID: "ann_5", Text: "NOTE OF THE LAST"}, true
	})
	h.waitFor("playback to end", func() bool { return strings.Contains(h.status(), "Finished") }, 6*time.Second)
	h.key(tcell.KeyCtrlT)
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "NOTE OF THE LAST") }, 2*time.Second)
	if !strings.Contains(h.status(), "^X") || !strings.Contains(h.status(), "Close Annotation") {
		t.Errorf("while it is up the hint is how to close it: %q", h.status())
	}
	// while it is up, Ctrl+X closes it
	h.key(tcell.KeyCtrlX)
	h.waitFor("it to go", func() bool { return !strings.Contains(h.screen(), "NOTE OF THE LAST") }, 2*time.Second)
	time.Sleep(120 * time.Millisecond)
	if h.front() != "playback" {
		t.Errorf("the modal opened: %s", h.front())
	}
	h.key(tcell.KeyCtrlT)
	h.waitFor("the annotation again", func() bool { return strings.Contains(h.screen(), "NOTE OF THE LAST") }, 2*time.Second)
	h.key(tcell.KeyCtrlT)
	h.waitFor("it to go again", func() bool { return !strings.Contains(h.screen(), "NOTE OF THE LAST") }, 2*time.Second)
}

func TestTheLegendSaysCtrlATimedAnnotations(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	if !strings.Contains(h.screen(), "^A Timed Annotations") || !strings.Contains(h.screen(), "^L Browse") {
		t.Errorf("the legend:\n%s", strings.Split(h.screen(), "\n")[0])
	}
}

// What is deleted takes out its time: an annotation later than it is earlier by as much, and one in it is gone - in the editor, as
// in the library, and written to the file.
func TestTimedAnnotationsFollowTheirCommandsWhenAnotherIsDeletedAndTheRecordingWritten(t *testing.T) {
	h := newStretched(t, func(cmds []libtrecs.Command) {
		cmds[4].TimedAnnotations = []libtrecs.Annotation{{ID: "tm_late", Timestamp: 10_020, Text: "LATE", DurationSeconds: 2, Kind: libtrecs.AnnotationKindTimed}}
	})
	h.pausePlayback()
	h.onUI(func() { h.em.deletedCommands[1] = true }) // 0.264 s to 0.312 s of the stretched recording
	h.key(tcell.KeyCtrlA)                             // the list of the one there is
	h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
	h.key(tcell.KeyCtrlN) // and a new one
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	h.setTimeField("00:03")
	h.typeText("A new one")
	h.key(tcell.KeyCtrlS) // writes the deletion with it
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	all, holder := h.timedOnDisk()
	if len(all) != 2 {
		t.Fatalf("%+v", all)
	}
	var late libtrecs.Annotation
	for _, a := range all {
		if a.Text == "LATE" {
			late = a
		}
	}
	if late.Timestamp >= 10_020 || late.Timestamp < 9_900 || holder["LATE"] != 3 { // five commands now: the fifth is index 3
		t.Errorf("LATE at %d ms, with command %d: it moved up with the command it is in", late.Timestamp, holder["LATE"]+1)
	}
	if n := len(h.reparsed()); n != 5 {
		t.Errorf("%d commands", n)
	}
	_ = fmt.Sprint
}

// A jump forward goes over the annotations that are between: they are not shown - all at once, or one of them - as the progress
// clock catches up with where playback went. This is the case that counts: the annotation has not been shown when the jump is made.
func TestAJumpForwardDoesNotShowWhatItWentOver(t *testing.T) {
	h := newStretched(t, func(cmds []libtrecs.Command) {
		cmds[3].TimedAnnotations = []libtrecs.Annotation{{ID: "tm_over", Timestamp: 5000, Text: "WENT OVER", DurationSeconds: 30, Kind: libtrecs.AnnotationKindTimed}}
	})
	h.waitFor("playback to be going, early", func() bool { return h.position() > 300 && h.position() < 2000 }, 3*time.Second)
	h.key(tcell.KeyCtrlL) // to the fifth command, at 10 s: the annotation, at 5 s, is between
	h.waitText("Jump to Playback", 2*time.Second)
	for i := 0; i < 4; i++ {
		h.key(tcell.KeyDown)
	}
	h.key(tcell.KeyCtrlP)
	h.waitFor("playback after the jump", func() bool { return h.front() == "playback" && h.position() >= 9900 }, 3*time.Second)
	for i := 0; i < 8; i++ { // the progress clock ticks every 100 ms: it has had its chance
		time.Sleep(100 * time.Millisecond)
		if strings.Contains(h.screen(), "WENT OVER") {
			t.Fatalf("an annotation that was gone over was shown, at %d ms", h.position())
		}
	}
}

// The same by the edit page's jumps, which do not go through the playback screen's: a long way forward is a seek.
func TestAPositionThatJumpsAheadIsASeekAndNothingIsCaughtUpOn(t *testing.T) {
	h := newStretched(t, func(cmds []libtrecs.Command) {
		cmds[3].TimedAnnotations = []libtrecs.Annotation{{ID: "tm_a", Timestamp: 4000, Text: "BETWEEN", DurationSeconds: 30, Kind: libtrecs.AnnotationKindTimed}}
	})
	h.pausePlayback()
	h.onUI(func() {
		h.em.timedLast = 1000
		h.em.fireTimed(9000) // from 1 s to 9 s in one tick: nothing plays that fast
	})
	time.Sleep(150 * time.Millisecond)
	if strings.Contains(h.screen(), "BETWEEN") {
		t.Error("it caught up with a position that jumped ahead")
	}
	// but at the pace of playback it is shown
	h.onUI(func() {
		h.em.timedLast = 3950
		h.em.fireTimed(4050)
	})
	h.waitFor("it, when the position goes by it", func() bool { return strings.Contains(h.screen(), "BETWEEN") }, 2*time.Second)
}

// ---- the style -----------------------------------------------------------------------------------------------------------------

func (h *harness) timedStyle() (index int, label string) {
	h.onUI(func() { index, label = h.em.timedModal.style.GetCurrentOption() })
	return
}

func (h *harness) openTimedModalPaused() {
	h.t.Helper()
	h.pausePlayback()
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
}

func (h *harness) focusTimedStyle() {
	h.t.Helper()
	for i := 0; i < 3; i++ {
		h.key(tcell.KeyBacktab) // from the text: the nav menu's select, the pause's, then the style's
	}
	h.waitFor("focus on the style", func() bool { return h.focusedIs(func(m *timedModal) tview.Primitive { return m.style }) }, 2*time.Second)
}

func TestTheModalOffersTheStylesInTheCueCardsSelectAndStartsOnTheBottom(t *testing.T) {
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.timedModalHas("Style:", "Bottom (left corner) ▾")
	if i, label := h.timedStyle(); i != 1 || label != "Bottom (left corner)" {
		t.Errorf("starts on %d, %q: the bottom is what it was before there was a choice", i, label)
	}
	h.focusTimedStyle()
	h.key(tcell.KeyEnter) // opens it, as the cue card's Type does
	h.waitFor("both options", func() bool {
		return strings.Contains(h.screen(), "Centred (over the terminal)") && strings.Contains(h.screen(), "Bottom (left corner)")
	}, 2*time.Second)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyEnter)
	h.waitFor("Centred chosen", func() bool { i, _ := h.timedStyle(); return i == 0 }, 2*time.Second)
	h.timedModalHas("Centred (over the terminal) ▾")
}

func TestEscClosesTheStyleListAndNotTheModalAndTabDoesNotLeaveItOpen(t *testing.T) {
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.focusTimedStyle()
	h.key(tcell.KeyEnter)
	h.waitFor("the list open", func() (open bool) { h.onUI(func() { open = h.em.timedModal.style.IsOpen() }); return }, 2*time.Second)
	h.key(tcell.KeyEscape)
	h.waitFor("the list closed", func() (open bool) { h.onUI(func() { open = !h.em.timedModal.style.IsOpen() }); return }, 2*time.Second)
	if h.front() != timedNotePage {
		t.Fatalf("Esc closed the modal with the list open: %s", h.front())
	}
	h.key(tcell.KeyEscape) // and now it does
	h.waitFor("the modal to close", func() bool { return h.front() == "playback" }, 2*time.Second)
}

func TestTheStyleChosenIsWrittenAndIsWhatEditingShows(t *testing.T) {
	h := newStretched(t, nil)
	h.openTimedModalPaused()
	h.setTimeField("00:02")
	h.typeText("A centred one")
	h.focusTimedStyle()
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyEnter)
	h.waitFor("Centred chosen", func() bool { i, _ := h.timedStyle(); return i == 0 }, 2*time.Second)
	h.key(tcell.KeyCtrlS)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	if all, _ := h.timedOnDisk(); len(all) != 1 || all[0].Placement != libtrecs.TimedPlacementCentered {
		t.Fatalf("on disk: %+v", all)
	}
	// opened at its second, the modal shows what it is: Centred, not the default
	h.pausePlayback()
	h.onUI(func() { h.em.openTimedModalAt(2000) })
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	if i, label := h.timedStyle(); i != 0 {
		t.Errorf("editing a centred one shows %d, %q", i, label)
	}
	// and one at another second, with the default, is written as the bottom
	h.setTimeField("00:06")
	h.onUI(func() { h.em.timedModal.text.SetText("A bottom one", true); h.em.timedModal.style.SetCurrentOption(1) })
	h.key(tcell.KeyCtrlS)
	h.waitFor("it to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	got := map[string]string{}
	all, _ := h.timedOnDisk()
	for _, a := range all {
		got[a.Text] = a.Placement
	}
	if got["A centred one"] != "centered" || got["A bottom one"] != "bottom" || len(all) != 2 {
		t.Errorf("on disk: %v", got)
	}
}

// One that was written before there was a choice has no placement: it is the bottom, which is how it was shown.
func TestAnAnnotationWithNoPlacementIsTheBottomWhenEditedAndWhenShown(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("OLD ONE", 30))
	h.waitFor("it, at the bottom left", func() bool { x, y, ok := h.find("OLD ONE"); return ok && x < 6 && y > 25 }, 4*time.Second)
	h.pausePlayback()
	h.onUI(func() { h.em.openTimedModalAt(1000) })
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	if i, _ := h.timedStyle(); i != 1 {
		t.Errorf("an old one is edited as %d", i)
	}
}

func TestACentredTimedAnnotationIsShownAsACardAndABottomOneAtTheBottomLeft(t *testing.T) {
	centred := func(placement string) func(cmds []libtrecs.Command) {
		return func(cmds []libtrecs.Command) {
			cmds[3].TimedAnnotations = []libtrecs.Annotation{{ID: "tm_c", Timestamp: 1000, Placement: placement, DurationSeconds: 30, Kind: libtrecs.AnnotationKindTimed,
				Text: "CENTRED NOTE with `code` and [a link](https://example.com/x)"}}
		}
	}
	h := newStretched(t, centred(libtrecs.TimedPlacementCentered))
	h.waitFor("it", func() bool { return strings.Contains(h.screen(), "CENTRED NOTE") }, 4*time.Second)
	x, y, _ := h.find("CENTRED NOTE")
	if x < 25 || x > 75 || y < 8 || y > 28 {
		t.Errorf("at column %d, row %d of 120 by 40: it is for the middle", x, y)
	}
	if _, fg, _ := h.styleOf("╭"); fg != cueCardBorder {
		t.Errorf("its edge is %v: a card's is the card's green", fg)
	}
	if found, fg, _ := h.styleOf("code"); !found || fg != codeBlue {
		t.Errorf("its code: found %v, colour %v", found, fg)
	}
	if s := h.screen(); !strings.Contains(s, "a link") || !strings.Contains(s, "(https://example.com/x)") || strings.Contains(s, "`") {
		t.Errorf("a link in it is its label and its address, and no Markdown is shown:\n%s", s)
	}
	if h.playbackState() != libtrecs.PlaybackPlaying {
		t.Errorf("playback is %v while it is up", h.playbackState())
	}

	b := newStretched(t, centred(libtrecs.TimedPlacementBottom))
	b.waitFor("it", func() bool { return strings.Contains(b.screen(), "CENTRED NOTE") }, 4*time.Second)
	if x, y, _ := b.find("CENTRED NOTE"); x > 6 || y < 30 {
		t.Errorf("a bottom one is at column %d, row %d", x, y)
	}
	if _, fg, _ := b.styleOf("╭"); fg != timedBorder {
		t.Errorf("its edge is %v", fg)
	}
}

// ---- Ctrl+A adds, Ctrl+X dismisses --------------------------------------------------------------------------------------------

func previewOnTheSecondCommand(cmds []libtrecs.Command) {
	cmds[1].Annotation, cmds[1].HasAnnotation = libtrecs.Annotation{ID: "ann_p", Text: "PREVIEW NOTE", DurationSeconds: 30}, true
}

func TestCtrlADoesNotAddWhileAPreviewHoldsPlaybackAndCtrlXDismissesItAndThenItDoes(t *testing.T) {
	h := newHarness(t, previewOnTheSecondCommand)
	h.waitFor("the preview", func() bool { return strings.Contains(h.screen(), "PREVIEW NOTE") }, 4*time.Second)
	if s := h.status(); !strings.Contains(s, "^X") || !strings.Contains(s, "Close Annotation") {
		t.Errorf("the hint is for Ctrl+X: %q", s)
	}
	h.key(tcell.KeyCtrlA)
	h.waitText("Dismiss what is showing first", 2*time.Second)
	if h.front() != "playback" || !strings.Contains(h.screen(), "PREVIEW NOTE") {
		t.Errorf("Ctrl+A opened the modal, or dismissed the preview: %s", h.front())
	}
	h.key(tcell.KeyCtrlX)
	h.waitFor("the preview to go and playback to go on", func() bool {
		return !strings.Contains(h.screen(), "PREVIEW NOTE") && strings.Contains(h.screen(), "echo hello world")
	}, 3*time.Second)
	h.waitFor("playback to end", func() bool { return strings.Contains(h.status(), "Finished") }, 5*time.Second)
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal, now that nothing is up", func() bool { return h.front() == timedNotePage }, 2*time.Second)
}

func TestCtrlADoesNotDismissACardAndCtrlXDoes(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("INTRO ZERO", 30, libtrecs.CuePlacementIntro), true
	})
	h.waitFor("the card", func() bool { return strings.Contains(h.screen(), "INTRO ZERO") }, 4*time.Second)
	if s := h.status(); !strings.Contains(s, "^X") || !strings.Contains(s, "Dismiss Cue Card") {
		t.Errorf("the hint is for Ctrl+X: %q", s)
	}
	h.key(tcell.KeyCtrlA)
	h.waitText("Dismiss what is showing first", 2*time.Second)
	time.Sleep(150 * time.Millisecond)
	if !strings.Contains(h.screen(), "INTRO ZERO") || h.front() != "playback" {
		t.Error("Ctrl+A took the card away, or opened the modal")
	}
	h.key(tcell.KeyCtrlX)
	h.waitFor("the card to go", func() bool { return !strings.Contains(h.screen(), "INTRO ZERO") }, 3*time.Second)
}

func TestCtrlAWhileATimedAnnotationIsUpListsThemAndLeavesItUp(t *testing.T) {
	h := newStretched(t, timedAtOneSecond("TIMED NOTE", 30))
	h.waitFor("the annotation", func() bool { return strings.Contains(h.screen(), "TIMED NOTE") }, 4*time.Second)
	h.key(tcell.KeyCtrlA)
	h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
	if !strings.Contains(h.screen(), "TIMED NOTE") {
		t.Error("the timed annotation went when the list opened: it is not what the list is for")
	}
}

func TestCtrlXWithNothingUpDoesNothing(t *testing.T) {
	h := newStretched(t, nil)
	h.key(tcell.KeyCtrlX)
	time.Sleep(200 * time.Millisecond)
	if h.front() != "playback" || h.playbackState() != libtrecs.PlaybackPlaying {
		t.Errorf("page %s, playback %v", h.front(), h.playbackState())
	}
}
