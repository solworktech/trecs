package main

import (
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

// ---- the card's layout ---------------------------------------------------------------------------------------------

func TestACardIsCentredNoWiderThan80PercentAndWrapped(t *testing.T) {
	l := layoutCueCard("A short card", 100, 30, 0)
	if l.w <= 0 || l.left != (100-l.w)/2 || l.top != (30-l.h)/2 {
		t.Errorf("not centred: %+v", l)
	}
	long := strings.Repeat("word ", 60)
	l = layoutCueCard(long, 100, 30, 0)
	if l.w > 80 || len(l.lines) < 3 {
		t.Errorf("a long text: %d wide (at most 80), %d lines", l.w, len(l.lines))
	}
	for _, line := range l.lines {
		if tview.TaggedStringWidth(line) > 80-2-4 {
			t.Errorf("a line is wider than the card's inside: %q", line)
		}
	}
	if l.left < 0 || l.left+l.w > 100 || l.top < 0 || l.top+l.h > 30 {
		t.Errorf("outside the area: %+v", l)
	}
}

func TestACardsTextIsNotColourTagsAndKeepsItsBlankLines(t *testing.T) {
	l := layoutCueCard("Use [red]this[-] flag\n\nthen that", 100, 30, 0)
	if len(l.lines) != 3 || l.lines[1] != "" {
		t.Fatalf("%q", l.lines)
	}
	if got := tview.TaggedStringWidth(l.lines[0]); got != len("Use [red]this[-] flag") {
		t.Errorf("what was typed is text, and %q is %d wide", l.lines[0], got)
	}
}

func TestACardTallerThanTheAreaIsCutAndSaysSo(t *testing.T) {
	l := layoutCueCard(strings.Repeat("line\n", 50), 80, 12, 0)
	if l.h > 12 || l.lines[len(l.lines)-1] != "…" {
		t.Errorf("h=%d last=%q", l.h, l.lines[len(l.lines)-1])
	}
	if l := layoutCueCard("x", 10, 3, 0); l.w != 0 {
		t.Errorf("an area with no room for a card: %+v", l)
	}
}

// ---- driving the editor ---------------------------------------------------------------------------------------------

const cueFixture = "../lib/testdata/fish-marked.jsonl" // 6 commands in 1.3 s: ls /etc/hostname, echo hello world, ..., echo done

type harness struct {
	t    *testing.T
	em   *EditorMode
	sim  tcell.SimulationScreen
	done chan error
}

// newHarness runs the editor on a simulated 120x40 terminal, over the fixture. mutate sets up the commands - their cards, their
// notes - before anything has played.
func newHarness(t *testing.T, mutate func(cmds []libtrecs.Command)) *harness {
	t.Helper()
	return newHarnessFile(t, cueFixture, mutate)
}

// newHarnessFile is the same over another recording, which has the fixture's six commands.
func newHarnessFile(t *testing.T, fixture string, mutate func(cmds []libtrecs.Command)) *harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	copyFile(t, fixture, path)
	cmds, err := libtrecs.ParseRecording(path)
	if err != nil || len(cmds) != 6 {
		t.Fatalf("%d commands, %v", len(cmds), err)
	}
	if mutate != nil {
		mutate(cmds)
	}
	h := &harness{t: t, sim: tcell.NewSimulationScreen("UTF-8"), done: make(chan error, 1)}
	h.em = NewEditorMode(libtrecs.NewTerminalPlayer(), cmds, path)
	// The simulated terminal is attached, and sized, before the application runs: it is initialised here, and not by Run on
	// another goroutine while the test is already looking at it.
	h.em.app.SetScreen(h.sim)
	h.sim.SetSize(120, 40)
	go func() { h.done <- h.em.Run() }()
	t.Cleanup(func() {
		h.em.app.Stop()
		select {
		case err := <-h.done:
			if err != nil {
				t.Errorf("the editor stopped with an error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the editor did not stop")
		}
	})
	h.waitFor("the editor to be drawn", func() bool { return strings.Contains(h.screen(), "^L Browse") }, 5*time.Second)
	return h
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := readFileBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileBytes(dst, data); err != nil {
		t.Fatal(err)
	}
}

// screen is what is on the simulated terminal, as text. It is read on the UI goroutine - the one that draws on it - because the
// simulated terminal's cells are the live ones, not a copy; "" if the application is not answering (it has stopped).
func (h *harness) screen() string {
	out := make(chan string, 1)
	h.em.app.QueueUpdate(func() {
		cells, w, ht := h.sim.GetContents()
		var sb strings.Builder
		for y := 0; y < ht; y++ {
			for x := 0; x < w; x++ {
				if c := cells[y*w+x]; len(c.Runes) == 0 || c.Runes[0] == '\u00a0' { // a no-break space is a space, to a reader
					sb.WriteByte(' ')
				} else {
					sb.WriteRune(c.Runes[0])
				}
			}
			sb.WriteByte('\n')
		}
		out <- sb.String()
	})
	select {
	case s := <-out:
		return s
	case <-time.After(2 * time.Second):
		return ""
	}
}

func (h *harness) waitFor(what string, cond func() bool, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s. The screen:\n%s", what, h.screen())
}

func (h *harness) waitText(text string, timeout time.Duration) {
	h.t.Helper()
	h.waitFor(fmt.Sprintf("%q on the screen", text), func() bool { return strings.Contains(h.screen(), text) }, timeout)
}

func (h *harness) waitGone(text string, timeout time.Duration) {
	h.t.Helper()
	h.waitFor(fmt.Sprintf("%q off the screen", text), func() bool { return !strings.Contains(h.screen(), text) }, timeout)
}

func (h *harness) key(k tcell.Key) { h.sim.InjectKey(k, 0, tcell.ModNone) }
func (h *harness) typeText(s string) {
	for _, r := range s {
		h.sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

// onUI runs f on the UI goroutine, which owns the editor's state, and waits for it.
func (h *harness) onUI(f func()) {
	h.t.Helper()
	done := make(chan struct{})
	h.em.app.QueueUpdateDraw(func() { f(); close(done) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		h.t.Fatal("the UI goroutine did not answer")
	}
}

func (h *harness) front() (name string) {
	h.onUI(func() { name, _ = h.em.pages.GetFrontPage() })
	return
}

func (h *harness) status() (s string) {
	h.onUI(func() { s = h.em.statusView.GetText(true) })
	return
}

func (h *harness) card(idx int, post bool) (c libtrecs.Annotation, has bool) {
	h.onUI(func() {
		if post {
			c, has = h.em.commands[idx].PostCard, h.em.commands[idx].HasPostCard
		} else {
			c, has = h.em.commands[idx].IntroCard, h.em.commands[idx].HasIntroCard
		}
	})
	return
}

func (h *harness) focusIs(p func(m *cueModal) tview.Primitive) (is bool) {
	h.onUI(func() { is = h.em.app.GetFocus() == p(h.em.cueModal) })
	return
}

// pausePlayback holds playback still. Behind the command list it goes on, and every frame it renders sets the status line: a
// message the modal leaves there would be gone within the fixture's 1.3 s.
func (h *harness) pausePlayback() {
	h.onUI(func() { h.em.player.Pause() })
}

// count is how many times text is on the screen.
func (h *harness) count(text string) int { return strings.Count(h.screen(), text) }

// reparsed is the recording as it is on disk now.
func (h *harness) reparsed() []libtrecs.Command {
	h.t.Helper()
	var path string
	h.onUI(func() { path = h.em.filePath })
	cmds, err := libtrecs.ParseRecording(path)
	if err != nil {
		h.t.Fatal(err)
	}
	return cmds
}

// openModalOn opens the command list and the cue card modal on the command at index idx.
func (h *harness) openModalOn(idx int) {
	h.t.Helper()
	h.key(tcell.KeyCtrlL)
	h.waitText("Jump to Playback", 2*time.Second)
	for i := 0; i < idx; i++ {
		h.key(tcell.KeyDown)
	}
	h.waitFor("the list to be on the command", func() (ok bool) {
		h.onUI(func() { ok = h.em.commandList.GetCurrentItem() == idx })
		return
	}, 2*time.Second)
	h.key(tcell.KeyCtrlA)
}

// ---- the modal -----------------------------------------------------------------------------------------------------

func TestCtrlAInTheCommandListOpensTheModalForTheCommandInFocusAndSaveWritesTheRecording(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlL)
	h.waitText("Add/Edit cue card", 2*time.Second) // the list's legend says so
	h.openModalOn(2)

	h.waitText("Command 3 of 6", 2*time.Second)
	for _, want := range []string{"Add cue card", "Type:", "Intro (before command)", "Content", "Display duration (seconds)", "Save  ^S", "Discard  Esc", "printf", "**bold** *italic* [link](https://url)"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("the modal does not show %q. The screen:\n%s", want, h.screen())
		}
	}
	if h.front() != cueCardPage {
		t.Fatalf("front page: %s", h.front())
	}
	if strings.Contains(h.screen(), "Delete cue card") {
		t.Error("a Delete button for a command that has no card")
	}
	// It is centred on the editor.
	lines := strings.Split(h.screen(), "\n")
	top, bottom := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "Add cue card") && top < 0 && (strings.Contains(l, "═") || strings.Contains(l, "─")) {
			top = i
		}
		if strings.Contains(l, "Discard  Esc") {
			bottom = i
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatalf("modal not found:\n%s", h.screen())
	}
	if mid := (top + bottom) / 2; mid < 14 || mid > 24 {
		t.Errorf("the modal's middle is on row %d of 40", mid)
	}

	// Tab goes through the elements, in order, and round - past the Delete button, which has nothing to delete here.
	if !h.focusIs(func(m *cueModal) tview.Primitive { return m.kind }) {
		t.Fatal("focus should start on the type")
	}
	for _, step := range []struct {
		name string
		p    func(m *cueModal) tview.Primitive
	}{
		{"content", func(m *cueModal) tview.Primitive { return m.text }},
		{"duration", func(m *cueModal) tview.Primitive { return m.duration }},
		{"Save", func(m *cueModal) tview.Primitive { return m.save }},
		{"Discard (the Delete button has nothing to delete)", func(m *cueModal) tview.Primitive { return m.discard }},
		{"type again", func(m *cueModal) tview.Primitive { return m.kind }},
	} {
		h.key(tcell.KeyTab)
		h.waitFor("focus on "+step.name, func() bool { return h.focusIs(step.p) }, 2*time.Second)
	}
	h.key(tcell.KeyBacktab)
	h.waitFor("Shift+Tab to go back", func() bool { return h.focusIs(func(m *cueModal) tview.Primitive { return m.discard }) }, 2*time.Second)
	h.key(tcell.KeyTab)

	// Type the content and a duration; ^S saves - to the file.
	h.key(tcell.KeyTab)
	h.typeText("Hello, cue")
	h.waitText("Hello, cue", 2*time.Second)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyBackspace2)
	h.typeText("7")
	h.key(tcell.KeyCtrlS)
	h.waitFor("the recording to be written and playback to resume", func() bool { return h.front() == "playback" }, 5*time.Second)

	onDisk := h.reparsed()
	c := onDisk[2]
	if !c.HasIntroCard || c.IntroCard.Text != "Hello, cue" || c.IntroCard.DurationSeconds != 7 || c.IntroCard.Kind != libtrecs.AnnotationKindCue || c.IntroCard.Placement != libtrecs.CuePlacementIntro {
		t.Errorf("on disk: %+v (has %v)", c.IntroCard, c.HasIntroCard)
	}
	if c.HasPostCard || onDisk[1].HasIntroCard || onDisk[3].HasIntroCard {
		t.Error("a card on the wrong command, or one too many")
	}
	if got, has := h.card(2, false); !has || got.Text != "Hello, cue" || got.ID == "" {
		t.Errorf("what the editor holds after the reload: %+v (has %v)", got, has)
	}
	var backup string
	h.onUI(func() { backup = h.em.filePath + ".backup" })
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("no backup: %v", err)
	}
	// The status line is not asserted: playback resumes at the command, and the card is shown over its prompt at once.
	h.onUI(func() {
		if h.em.pages.HasPage(cueCardPage) {
			t.Error("the modal's page is still there")
		}
	})
	h.waitFor("the new card to be shown, over the command's prompt", func() bool { return strings.Contains(h.screen(), "Hello, cue") }, 4*time.Second)
}

func TestEscDiscardsAndWritesNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.openModalOn(1)
	h.waitText("Command 2 of 6", 2*time.Second)
	h.key(tcell.KeyTab)
	h.typeText("Typed, then discarded")
	h.key(tcell.KeyEscape)
	h.waitFor("the modal to close", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	for _, post := range []bool{false, true} {
		if _, has := h.card(1, post); has {
			t.Errorf("a discarded card was kept (post=%v)", post)
		}
	}
	for i, c := range h.reparsed() {
		if c.HasIntroCard || c.HasPostCard {
			t.Errorf("command %d has a card on disk", i)
		}
	}
	// Ctrl+L inside the modal does not reach past it.
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
	h.key(tcell.KeyCtrlL)
	time.Sleep(150 * time.Millisecond)
	if h.front() != cueCardPage {
		t.Errorf("^L went through the modal: %s", h.front())
	}
}

// The expanded select showed the chosen option twice - in its field, and at the head of its list - and drew the list over the
// border of the box below it.
func TestTheSelectShowsEachOptionOnceWhenItIsOpenAndLeavesTheModalAlone(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.openModalOn(1)
	h.waitText("Command 2 of 6", 2*time.Second)
	if n := h.count("Intro (before command)"); n != 1 {
		t.Errorf("closed: %d times", n)
	}
	before := h.screen()

	h.key(tcell.KeyEnter)
	h.waitText("Post (after command)", 2*time.Second)
	if a, b := h.count("Intro (before command)"), h.count("Post (after command)"); a != 1 || b != 1 {
		t.Errorf("open, Intro %d times and Post %d times. The screen:\n%s", a, b, h.screen())
	}
	for _, l := range strings.Split(h.screen(), "\n") {
		//nolint
		if strings.Contains(l, " Content ") && (!strings.Contains(l, "┌") || !strings.Contains(l, "┐") || !strings.Contains(l, "──")) {
			t.Errorf("the Content box's border is broken: %q", l)
		}
	}
	// Everything but the two rows that are the select's is as it was.
	was, now := strings.Split(before, "\n"), strings.Split(h.screen(), "\n")
	var changed []int
	for i := range was {
		if was[i] != now[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) > 2 {
		t.Errorf("rows changed outside the select's: %v", changed)
	}

	// Esc closes the list, and not the modal; Down and Enter choose Post.
	h.key(tcell.KeyEscape)
	h.waitGone("Post (after command)", 2*time.Second)
	if h.front() != cueCardPage {
		t.Fatal("Esc closed the modal, not the list")
	}
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	h.waitFor("the type to be Post", func() (ok bool) {
		h.onUI(func() { i, _ := h.em.cueModal.kind.GetCurrentOption(); ok = i == 1 })
		return
	}, 2*time.Second)
	if n := h.count("Post (after command)"); n != 1 {
		t.Errorf("Post is %d times on the screen once chosen", n)
	}
	// Tab with the list open does not leave the select: it is Esc and Enter that close it.
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyTab)
	time.Sleep(100 * time.Millisecond)
	if !h.focusIs(func(m *cueModal) tview.Primitive { return m.kind }) {
		t.Error("Tab moved on from an open list")
	}
}

func TestTheModalOpensOnTheKindOfCardTheCommandHasWhenItHasOneKind(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[1].IntroCard, cmds[1].HasIntroCard = card("Only an intro", 3, libtrecs.CuePlacementIntro), true
		cmds[2].PostCard, cmds[2].HasPostCard = card("Only a post", 4, libtrecs.CuePlacementPost), true
		cmds[3].IntroCard, cmds[3].HasIntroCard = card("Both: intro", 3, libtrecs.CuePlacementIntro), true
		cmds[3].PostCard, cmds[3].HasPostCard = card("Both: post", 5, libtrecs.CuePlacementPost), true
	})
	h.pausePlayback()
	for _, tc := range []struct {
		idx  int
		kind int
		text string
	}{{0, 0, ""}, {1, 0, "Only an intro"}, {2, 1, "Only a post"}, {3, 0, "Both: intro"}} {
		h.openModalOn(tc.idx)
		h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
		var kind int
		var text string
		h.onUI(func() { kind, _ = h.em.cueModal.kind.GetCurrentOption(); text = h.em.cueModal.text.GetText() })
		if kind != tc.kind || text != tc.text {
			t.Errorf("command %d: opened on kind %d with %q, want kind %d with %q", tc.idx+1, kind, text, tc.kind, tc.text)
		}
		h.key(tcell.KeyEscape) // the modal
		h.waitFor("the modal to close", func() bool { return h.front() == "commandlist" }, 2*time.Second)
		h.key(tcell.KeyEscape) // the list: it is opened afresh, at the top, for the next
		h.waitFor("the list to close", func() bool { return h.front() == "playback" }, 2*time.Second)
	}
}

func TestSaveValidatesAndAnExistingCardIsEditedAndDeletedWithItsButton(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[3].PostCard, cmds[3].HasPostCard = libtrecs.Annotation{ID: "cue_x", Text: "Old post", DurationSeconds: 4, Kind: libtrecs.AnnotationKindCue, Placement: libtrecs.CuePlacementPost}, true
	})
	h.pausePlayback()
	h.openModalOn(0)
	h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)

	h.key(tcell.KeyCtrlS) // nothing typed
	h.waitText("Enter the text of the card", 2*time.Second)
	if _, has := h.card(0, false); has {
		t.Error("an empty card was saved")
	}
	h.typeText("x")     // the error has put the focus on the content
	h.key(tcell.KeyTab) // duration
	h.key(tcell.KeyBackspace2)
	h.typeText("0")
	h.key(tcell.KeyCtrlS)
	h.waitText("1 or more", 2*time.Second)
	if h.front() != cueCardPage {
		t.Error("the modal went")
	}
	h.key(tcell.KeyEscape)
	h.waitFor("the modal to close", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	for i, c := range h.reparsed() {
		if c.HasIntroCard || i != 3 && c.HasPostCard {
			t.Errorf("command %d: something was written", i)
		}
	}

	// Command 4 has only a post card: that is what the modal opens on, with the card in it.
	h.openModalOn(3)
	h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
	h.waitText("Old post", 2*time.Second)
	h.waitText("Post (after command)", 2*time.Second)

	// Its button deletes it - Tab goes to it past the Save button - and the file has it no more.
	for i := 0; i < 4; i++ {
		h.key(tcell.KeyTab)
	}
	h.waitFor("focus on Delete", func() bool { return h.focusIs(func(m *cueModal) tview.Primitive { return m.del }) }, 2*time.Second)
	h.key(tcell.KeyEnter)
	h.waitFor("the recording to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	for i, c := range h.reparsed() {
		if c.HasPostCard || c.HasIntroCard {
			t.Errorf("command %d still has a card on disk after the delete", i)
		}
	}
}

func TestTheDeleteButtonIsNotThereWhenThereIsNothingToDeleteAndFollowsTheType(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[1].IntroCard, cmds[1].HasIntroCard = card("Only an intro", 3, libtrecs.CuePlacementIntro), true
	})
	h.pausePlayback()
	shown := func() bool { return strings.Contains(h.screen(), "Delete cue card") }

	h.openModalOn(1)
	h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
	if !shown() {
		t.Error("no Delete button for a command that has an intro card, which is what is shown")
	}
	// Post is chosen: there is none, and the button is not there - and Tab does not stop where it was.
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	h.waitFor("Delete gone for the missing post card", func() bool { return !shown() && strings.Contains(h.screen(), "Discard  Esc") }, 2*time.Second)
	h.key(tcell.KeyTab) // content
	h.key(tcell.KeyTab) // duration
	h.key(tcell.KeyTab) // Save
	h.key(tcell.KeyTab) // Discard
	h.waitFor("focus on Discard", func() bool { return h.focusIs(func(m *cueModal) tview.Primitive { return m.discard }) }, 2*time.Second)
	// Asked for anyway, from a macro or a mouse, it does nothing.
	h.onUI(func() { h.em.deleteCueCard() })
	if _, has := h.card(1, false); !has {
		t.Error("Delete deleted the intro card while the post was chosen")
	}
	// The row is centred with two buttons as with three.
	for _, l := range strings.Split(h.screen(), "\n") {
		if strings.Contains(l, "Save  ^S") {
			a, b := strings.Index(l, "Save  ^S"), strings.Index(l, "Esc")+3
			if left, right := a-strings.Index(l, "║"), strings.LastIndex(l, "║")-b; left-right > 3 || right-left > 3 {
				t.Errorf("the buttons are not centred: %d to the left, %d to the right: %q", left, right, l)
			}
		}
	}
	// Back to Intro: it is there again.
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyEnter)
	h.waitFor("Delete back", shown, 2*time.Second)
}

func TestIfTheRecordingCannotBeWrittenTheModalStaysAndSaysSo(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.openModalOn(1)
	h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
	h.onUI(func() { h.em.filePath = "/no/such/directory/terminal.jsonl" })
	h.key(tcell.KeyTab)
	h.typeText("A card that cannot be written")
	h.key(tcell.KeyCtrlS)
	h.waitText("Not saved", 2*time.Second)
	if h.front() != cueCardPage {
		t.Errorf("the modal went: %s", h.front())
	}
}

func TestTheLastCommandsPostCardSaysItStaysAndADeletedCommandHasNoModal(t *testing.T) {
	h := newHarness(t, nil)
	h.pausePlayback()
	h.openModalOn(5)
	h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	h.waitText("Stays on screen until playback is replayed or moved", 2*time.Second)
	h.key(tcell.KeyEscape)
	h.waitFor("the modal to close", func() bool { return h.front() == "commandlist" }, 2*time.Second)

	h.onUI(func() { h.em.deletedCommands[1] = true })
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp) // to command 2 (index 1)
	h.waitFor("the list on command 2", func() (ok bool) {
		h.onUI(func() { ok = h.em.commandList.GetCurrentItem() == 1 })
		return
	}, 2*time.Second)
	h.key(tcell.KeyCtrlA)
	h.waitText("marked for deletion", 2*time.Second)
	if h.front() == cueCardPage {
		t.Error("a modal for a command that is to be deleted")
	}
}

// ---- playback ------------------------------------------------------------------------------------------------------

func card(text string, seconds int, placement string) libtrecs.Annotation {
	return libtrecs.Annotation{ID: "cue_" + text, Text: text, DurationSeconds: seconds, Kind: libtrecs.AnnotationKindCue, Placement: placement}
}

// firstSeen watches the screen, and notes when each marker was first on it.
type trace struct {
	mu   chan struct{}
	seen map[string]time.Time
	stop chan struct{}
}

func (h *harness) trace(markers ...string) *trace {
	tr := &trace{mu: make(chan struct{}, 1), seen: map[string]time.Time{}, stop: make(chan struct{})}
	go func() {
		for {
			select {
			case <-tr.stop:
				return
			case <-time.After(8 * time.Millisecond):
			}
			s := h.screen()
			now := time.Now()
			tr.mu <- struct{}{}
			for _, m := range markers {
				if _, ok := tr.seen[m]; !ok && strings.Contains(s, m) {
					tr.seen[m] = now
				}
			}
			<-tr.mu
		}
	}()
	return tr
}

func (tr *trace) at(marker string) (time.Time, bool) {
	tr.mu <- struct{}{}
	defer func() { <-tr.mu }()
	t, ok := tr.seen[marker]
	return t, ok
}

// the fixture's prompt, drawn once for each command that has begun
const prompt = "root@vm /tmp#"

func TestTheFirstIntroComesBeforeAnythingAndEveryOtherCardJustAfterThePromptOverWhatIsThere(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("INTRO ZERO", 1, libtrecs.CuePlacementIntro), true
		cmds[0].PostCard, cmds[0].HasPostCard = card("POST ZERO", 1, libtrecs.CuePlacementPost), true
		cmds[1].IntroCard, cmds[1].HasIntroCard = card("INTRO ONE", 1, libtrecs.CuePlacementIntro), true
		cmds[5].PostCard, cmds[5].HasPostCard = card("FINAL CARD", 1, libtrecs.CuePlacementPost), true
	})
	tr := h.trace("INTRO ZERO", "ls /etc/hostname", "POST ZERO", "INTRO ONE")
	t.Cleanup(func() { close(tr.stop) })
	has := func(s string) bool { return strings.Contains(h.screen(), s) }

	// 1. The first command's intro is what the recording begins with: nothing has been drawn, not even the prompt.
	h.waitFor("the first intro, with nothing drawn", func() bool { return has("INTRO ZERO") && !has(prompt) }, 4*time.Second)

	// 2. The next command's prompt has rendered, and the first command is still on the screen: the post card is over both.
	// The command has not been typed yet - playback is paused for the card, just after its prompt.
	h.waitFor("the post card over the first command and the second prompt", func() bool {
		return has("POST ZERO") && has("ls /etc/hostname") && h.count(prompt) == 2 && !has("echo hello world") && !has("INTRO ZERO")
	}, 4*time.Second)
	// There was no waiting for it.
	first, _ := tr.at("ls /etc/hostname")
	post, _ := tr.at("POST ZERO")
	if d := post.Sub(first); d > 800*time.Millisecond {
		t.Errorf("the post card came %v after the first command's output", d)
	}

	// 3. Then the next command's intro, over the same screen - not cleared - after the post card has gone.
	h.waitFor("the intro over what is there", func() bool {
		return has("INTRO ONE") && has("ls /etc/hostname") && h.count(prompt) == 2 && !has("POST ZERO") && !has("echo hello world")
	}, 4*time.Second)

	// 4. Playback goes on: the second command is typed, and the recording plays to its end, where the last card goes up.
	h.waitFor("the second command to be typed", func() bool { return has("echo hello world") && !has("INTRO ONE") }, 4*time.Second)
	h.waitFor("the last card", func() bool { return has("FINAL CARD") }, 8*time.Second)
	if !has("echo done") || !strings.Contains(h.status(), "Finished") {
		t.Errorf("the card is over the last command's output, which is still there; status %q. The screen:\n%s", h.status(), h.screen())
	}
	time.Sleep(900 * time.Millisecond) // longer than its duration: it is not timed
	if !has("FINAL CARD") {
		t.Fatal("the last card went away by itself")
	}

	// 5. Replay (Space at the end) takes it off, and starts again with the first intro.
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitFor("the last card to go and the first intro to come", func() bool { return !has("FINAL CARD") && has("INTRO ZERO") }, 3*time.Second)
}

// After a jump the screen is empty but for the prompt of the command: its intro is shown over that.
func TestAJumpToACommandShowsItsIntroOverItsPrompt(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[2].IntroCard, cmds[2].HasIntroCard = card("INTRO TWO", 30, libtrecs.CuePlacementIntro), true
		cmds[1].PostCard, cmds[1].HasPostCard = card("POST ONE", 30, libtrecs.CuePlacementPost), true
	})
	has := func(s string) bool { return strings.Contains(h.screen(), s) }
	h.waitFor("playback to be held for the post card, as it is at the second command", func() bool { return has("POST ONE") }, 4*time.Second)
	h.key(tcell.KeyCtrlL)
	h.waitText("Jump to Playback", 2*time.Second)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyCtrlP)
	h.waitFor("the intro over the prompt alone", func() bool {
		return has("INTRO TWO") && h.count(prompt) == 1 && !has("echo hello world") && !has("printf")
	}, 3*time.Second)
	if has("POST ONE") {
		t.Error("the post card of the command before the one jumped to: nothing had ended")
	}
}

func TestTheLastCardGoesWhenPlaybackIsMovedThroughTheCommandList(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[5].PostCard, cmds[5].HasPostCard = card("FINAL CARD", 1, libtrecs.CuePlacementPost), true
	})
	has := func(s string) bool { return strings.Contains(h.screen(), s) }
	h.waitFor("the last card", func() bool { return has("FINAL CARD") }, 6*time.Second)

	h.key(tcell.KeyCtrlL)
	h.waitText("Jump to Playback", 2*time.Second)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyCtrlP)
	h.waitFor("playback, without the card", func() bool { return has("Pause/Resume") && !has("FINAL CARD") && !has("Jump to Playback") }, 2*time.Second)
	// It plays on to the end, and the card is shown again when the end is reached again.
	h.waitFor("the card, at the end again", func() bool { return has("FINAL CARD") }, 6*time.Second)
}

func TestCtrlXSkipsTheFirstIntroAndPlaybackGoesOnWithTheFrameThatWasHeld(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("INTRO ZERO", 30, libtrecs.CuePlacementIntro), true
	})
	has := func(s string) bool { return strings.Contains(h.screen(), s) }
	h.waitFor("the intro", func() bool { return has("INTRO ZERO") }, 4*time.Second)
	if s := h.status(); !strings.Contains(s, "Dismiss Cue Card") {
		t.Errorf("status while a card is up: %q", s)
	}
	// Space does nothing while it is up, as for a note that is previewed.
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	time.Sleep(150 * time.Millisecond)
	if !has("INTRO ZERO") {
		t.Fatal("Space took the card away")
	}
	h.key(tcell.KeyCtrlX)
	h.waitFor("the card gone and the command shown", func() bool { return !has("INTRO ZERO") && has("ls /etc/hostname") }, 3*time.Second)
	h.waitFor("the recording to play on", func() bool { return has("echo done") }, 4*time.Second)
	if s := h.status(); strings.Contains(s, "Dismiss Cue Card") {
		t.Errorf("status afterwards: %q", s)
	}
}

func TestCtrlXSkipsACardShownAfterAPromptAndWhatWouldHaveFollowedIt(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].PostCard, cmds[0].HasPostCard = card("POST ZERO", 30, libtrecs.CuePlacementPost), true
		cmds[1].IntroCard, cmds[1].HasIntroCard = card("INTRO ONE", 30, libtrecs.CuePlacementIntro), true
	})
	has := func(s string) bool { return strings.Contains(h.screen(), s) }
	h.waitFor("the post card", func() bool { return has("POST ZERO") }, 4*time.Second)
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone) // does nothing
	time.Sleep(150 * time.Millisecond)
	if !has("POST ZERO") {
		t.Fatal("Space took the card away")
	}
	h.key(tcell.KeyCtrlX)
	h.waitFor("playback to go on", func() bool { return !has("POST ZERO") && has("echo hello world") }, 3*time.Second)
	if has("INTRO ONE") {
		t.Error("the intro that followed was shown after the post card was skipped")
	}
}

// A command that has a note that is previewed as well as an intro card has the note first, as it always has; the card follows
// when its prompt, which the note held back, has been drawn - over the prompt alone, which the note had cleared the screen for.
func TestANoteIsFollowedByTheCommandsIntroCardWhenItsPromptHasBeenDrawn(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[1].IntroCard, cmds[1].HasIntroCard = card("INTRO ONE", 1, libtrecs.CuePlacementIntro), true
		cmds[1].Annotation, cmds[1].HasAnnotation = libtrecs.Annotation{ID: "ann_1", Text: "NOTE ONE", DurationSeconds: 1}, true
	})
	has := func(s string) bool { return strings.Contains(h.screen(), s) }
	tr := h.trace("ls /etc/hostname", "NOTE ONE", "INTRO ONE", "echo hello world")
	t.Cleanup(func() { close(tr.stop) })

	h.waitFor("the intro over the prompt the note held back", func() bool {
		return has("INTRO ONE") && h.count(prompt) == 1 && !has("NOTE ONE") && !has("echo hello world")
	}, 12*time.Second)
	h.waitFor("the command's output to follow", func() bool { _, ok := tr.at("echo hello world"); return ok }, 6*time.Second)

	first, _ := tr.at("ls /etc/hostname")
	note, okN := tr.at("NOTE ONE")
	intro, okI := tr.at("INTRO ONE")
	out, _ := tr.at("echo hello world")
	if !okI || !okN {
		t.Fatalf("note %v, intro %v", okN, okI)
	}
	//nolint
	if !first.Before(note) || !note.Before(intro) || !intro.Before(out) {
		t.Errorf("order: command 0 %v, note %v, intro %v, command 1 %v", first.UnixMilli(), note.UnixMilli(), intro.UnixMilli(), out.UnixMilli())
	}
	if d := intro.Sub(note); d < 800*time.Millisecond || d > 2500*time.Millisecond {
		t.Errorf("the intro came %v after the note (which is shown for a second)", d)
	}
}

// The path a previewed note takes goes through the same code: nothing about it should have changed.
func TestANoteWithNoCardsIsPreviewedAsItWas(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[1].Annotation, cmds[1].HasAnnotation = libtrecs.Annotation{ID: "ann_1", Text: "NOTE ONE", DurationSeconds: 1}, true
	})
	tr := h.trace("ls /etc/hostname", "NOTE ONE", "hello world")
	t.Cleanup(func() { close(tr.stop) })
	h.waitFor("the output after the note", func() bool { _, ok := tr.at("hello world"); return ok }, 12*time.Second)

	first, _ := tr.at("ls /etc/hostname")
	note, okN := tr.at("NOTE ONE")
	out, _ := tr.at("hello world")
	if !okN {
		t.Fatal("the note was not previewed")
	}
	// the previous command's output is given its linger, and then the note, and then the command
	if d := note.Sub(first); d < annotationLingerDelay-500*time.Millisecond {
		t.Errorf("the note came %v after the previous command: the pause for it to be read is %v", d, annotationLingerDelay)
	}
	if !first.Before(note) || !note.Before(out) {
		t.Error("order")
	}
}

func readFileBytes(p string) ([]byte, error)  { return os.ReadFile(p) }
func writeFileBytes(p string, b []byte) error { return os.WriteFile(p, b, 0o644) }

// ---- links in a card: buttons ---------------------------------------------------------------------------------------

func TestALinkInACardIsAButtonAndItsUrlIsNotShown(t *testing.T) {
	l := layoutCueCard("Get it:\n[Download Trecs](https://github.com/solworktech/trecs/releases)", 100, 30, 0)
	joined := strings.Join(l.lines, "\n")
	if strings.Contains(joined, "https://") || strings.Contains(joined, "](") {
		t.Errorf("the Markdown is in the card: %q", joined)
	}
	if !strings.Contains(joined, cueButtonFocusTag+"\u00a0Download\u00a0Trecs\u00a0"+cueTextTag) {
		t.Errorf("the link is not a button, padded, in the focused style (it is the only one): %q", joined)
	}
	// What is typed beside it is text.
	l = layoutCueCard("Use [red]this[-] with [Trecs](https://a.example)", 100, 30, 0)
	if j := strings.Join(l.lines, "\n"); !strings.Contains(j, "[red[]this") && !strings.Contains(j, "[red[]") {
		t.Errorf("a colour tag in the text is not text any more: %q", j)
	}
}

func TestOnlyTheFocusedButtonIsDrawnAsFocusedAndALabelIsNeverCutAcrossLines(t *testing.T) {
	text := "A sentence long enough that the first button lands near the end of a line, then [Download the Trecs recorder](https://a.example/a) and [Read the docs](https://b.example/b) to go on."
	for focus := 0; focus < 2; focus++ {
		l := layoutCueCard(text, 60, 30, focus)
		joined := strings.Join(l.lines, "\n")
		if n := strings.Count(joined, cueButtonFocusTag); n != 1 {
			t.Fatalf("focus %d: %d focused buttons", focus, n)
		}
		want := []string{"Download\u00a0the\u00a0Trecs\u00a0recorder", "Read\u00a0the\u00a0docs"}[focus]
		if !strings.Contains(joined, cueButtonFocusTag+"\u00a0"+want) {
			t.Errorf("focus %d is not on %q: %q", focus, want, joined)
		}
		for _, label := range []string{"Download the Trecs recorder", "Read the docs"} {
			found := false
			for _, line := range l.lines {
				if strings.Contains(line, strings.ReplaceAll(label, " ", "\u00a0")) {
					found = true
				}
			}
			if !found {
				t.Errorf("focus %d: %q is not on one line: %q", focus, label, l.lines)
			}
		}
	}
}

func TestALinkThatIsNotAWebLinkIsTheTextItWasWrittenAs(t *testing.T) {
	l := layoutCueCard("[Open](file:///etc/passwd)", 100, 30, 0)
	j := strings.Join(l.lines, "\n")
	if strings.Contains(j, cueButtonTag) || strings.Contains(j, cueButtonFocusTag) {
		t.Errorf("a button for a file: link: %q", j)
	}
	o := newCueOverlay()
	o.setText("[Open](file:///etc/passwd) [Run](javascript:alert(1))")
	if len(o.links) != 0 {
		t.Errorf("%+v", o.links)
	}
}

func TestFocusGoesRoundTheButtons(t *testing.T) {
	o := newCueOverlay()
	o.setText("[A](https://a.example) [B](https://b.example) [C](https://c.example)")
	order := []string{}
	for i := 0; i < 4; i++ {
		l, _ := o.focused()
		order = append(order, l.Text)
		o.moveFocus(1)
	}
	o.moveFocus(-1)
	back, _ := o.focused()
	if strings.Join(order, "") != "ABCA" || back.Text != "A" {
		t.Errorf("forwards %v, then back one: %s", order, back.Text)
	}
	o.moveFocus(-1)
	if l, _ := o.focused(); l.Text != "C" {
		t.Errorf("back from the first goes round to the last: %s", l.Text)
	}
	o.setText("")
	if _, ok := o.focused(); ok || len(o.links) != 0 {
		t.Error("an empty card has buttons")
	}
}

// ---- links in a card: in the editor ----------------------------------------------------------------------------------

// an opener that opens nothing, and says what it was asked to
type fakeOpener struct {
	mu   chan struct{}
	urls []string
	err  error
}

func newFakeOpener(err error) *fakeOpener { return &fakeOpener{mu: make(chan struct{}, 1), err: err} }

func (f *fakeOpener) open(url string) error {
	f.mu <- struct{}{}
	defer func() { <-f.mu }()
	f.urls = append(f.urls, url)
	return f.err
}

func (f *fakeOpener) opened() []string {
	f.mu <- struct{}{}
	defer func() { <-f.mu }()
	return append([]string(nil), f.urls...)
}

func (h *harness) useOpener(f *fakeOpener) { h.onUI(func() { h.em.openURL = f.open }) }

func TestEnterOpensTheFocusedLinkOfACardAndTabMovesToTheNext(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("Grab it: [One](https://one.example/x) or [Two Words](https://two.example/y)", 30, libtrecs.CuePlacementIntro), true
	})
	f := newFakeOpener(nil)
	h.useOpener(f)
	has := func(s string) bool { return strings.Contains(h.screen(), s) }

	h.waitFor("the card, with its buttons", func() bool { return has("Grab it:") && has("One") && has("Two Words") }, 4*time.Second)
	if has("](") || strings.Contains(strings.Join(strings.Split(h.screen(), "\n")[2:30], "\n"), "https://") {
		t.Errorf("the Markdown, or an address, is in the card. The screen:\n%s", h.screen())
	}
	// The address is in the status line, for the button that has the focus.
	h.waitFor("the status line to say where Enter goes", func() bool {
		return strings.Contains(h.status(), "Open https://one.example/x") && strings.Contains(h.status(), "Tab")
	}, 2*time.Second)

	h.key(tcell.KeyEnter)
	h.waitFor("the first link to be opened", func() bool { return len(f.opened()) == 1 }, 2*time.Second)
	if f.opened()[0] != "https://one.example/x" {
		t.Errorf("%v", f.opened())
	}
	if !has("Grab it:") || !strings.Contains(h.status(), "Opened https://one.example/x") {
		t.Errorf("the card went, or the status did not say: %q", h.status())
	}

	h.key(tcell.KeyTab)
	h.waitFor("the status line to move to the second", func() bool { return strings.Contains(h.status(), "Open https://two.example/y") }, 2*time.Second)
	h.key(tcell.KeyEnter)
	h.waitFor("the second link to be opened", func() bool { return len(f.opened()) == 2 }, 2*time.Second)
	if f.opened()[1] != "https://two.example/y" {
		t.Errorf("%v", f.opened())
	}
	h.key(tcell.KeyBacktab)
	h.waitFor("the status line to go back", func() bool { return strings.Contains(h.status(), "Open https://one.example/x") }, 2*time.Second)

	// Space still does nothing while the card is up; ^A skips it, and Enter has then nothing to open.
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	time.Sleep(120 * time.Millisecond)
	if !has("Grab it:") {
		t.Fatal("Space took the card away")
	}
	h.key(tcell.KeyCtrlX)
	h.waitFor("the card to go", func() bool { return !has("Grab it:") }, 3*time.Second)
	h.key(tcell.KeyEnter)
	time.Sleep(150 * time.Millisecond)
	if n := len(f.opened()); n != 2 {
		t.Errorf("Enter opened a link with no card up: %d opened", n)
	}
}

func TestACardWithNoLinksLeavesEnterAndTabAlone(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("No links here", 30, libtrecs.CuePlacementIntro), true
	})
	f := newFakeOpener(nil)
	h.useOpener(f)
	h.waitFor("the card", func() bool { return strings.Contains(h.screen(), "No links here") }, 4*time.Second)
	if s := h.status(); strings.Contains(s, "Enter") {
		t.Errorf("status offers Enter for a card with no buttons: %q", s)
	}
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyTab)
	time.Sleep(150 * time.Millisecond)
	if len(f.opened()) != 0 || !strings.Contains(h.screen(), "No links here") {
		t.Errorf("opened %v", f.opened())
	}
}

// The last card is the one a call to action is for: it is the one that stays.
func TestTheLastCardsButtonsWorkAfterPlaybackHasEndedAndItsStatusStaysFinished(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[5].PostCard, cmds[5].HasPostCard = card("Thanks for watching.\n\n[Download Trecs](https://github.com/solworktech/trecs/releases)", 1, libtrecs.CuePlacementPost), true
	})
	f := newFakeOpener(nil)
	h.useOpener(f)
	has := func(s string) bool { return strings.Contains(h.screen(), s) }
	h.waitFor("the last card", func() bool { return has("Thanks for watching.") && has("Download Trecs") }, 6*time.Second)
	h.waitFor("the status line", func() bool {
		s := h.status()
		return strings.Contains(s, "Finished") && strings.Contains(s, "Open https://github.com/solworktech/trecs/releases")
	}, 2*time.Second)

	time.Sleep(1200 * time.Millisecond) // longer than its duration: it is not timed
	h.key(tcell.KeyEnter)
	h.waitFor("the link to be opened", func() bool { return len(f.opened()) == 1 }, 2*time.Second)
	if f.opened()[0] != "https://github.com/solworktech/trecs/releases" || !has("Download Trecs") {
		t.Errorf("opened %v, card still there %v", f.opened(), has("Download Trecs"))
	}
	if s := h.status(); !strings.Contains(s, "Opened") {
		t.Errorf("status: %q", s)
	}
	h.key(tcell.KeyTab) // a single button: the status goes on saying that playback has finished
	time.Sleep(100 * time.Millisecond)
	if s := h.status(); !strings.Contains(s, "Finished") {
		t.Errorf("Tab lost the message that playback has finished: %q", s)
	}
	// Replay takes the card, and so the button, away.
	h.sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitFor("the card to go", func() bool { return !has("Download Trecs") }, 3*time.Second)
	h.key(tcell.KeyEnter)
	time.Sleep(150 * time.Millisecond)
	if len(f.opened()) != 1 {
		t.Errorf("Enter opened a link with no card up: %v", f.opened())
	}
}

func TestIfALinkCannotBeOpenedTheStatusSaysSoAndKeepsTheAddress(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("[Trecs](https://trecs.example/p)", 30, libtrecs.CuePlacementIntro), true
	})
	h.useOpener(newFakeOpener(fmt.Errorf("no display")))
	h.waitFor("the card", func() bool { return strings.Contains(h.screen(), "Trecs") && strings.Contains(h.status(), "Enter") }, 4*time.Second)
	h.key(tcell.KeyEnter)
	h.waitFor("the failure", func() bool {
		s := h.status()
		return strings.Contains(s, "Could not open the link: no display") && strings.Contains(s, "https://trecs.example/p")
	}, 2*time.Second)
}

func TestALinkThatIsNotAWebLinkOffersNoButtonAndOpensNothing(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("[Open](file:///etc/passwd)", 30, libtrecs.CuePlacementIntro), true
	})
	f := newFakeOpener(nil)
	h.useOpener(f)
	h.waitFor("the card, with the text as it was written", func() bool { return strings.Contains(h.screen(), "[Open](file:///etc/passwd)") }, 4*time.Second)
	h.key(tcell.KeyEnter)
	time.Sleep(150 * time.Millisecond)
	if len(f.opened()) != 0 || strings.Contains(h.status(), "Enter") {
		t.Errorf("opened %v, status %q", f.opened(), h.status())
	}
}

// The text of a card is saved as it was typed, Markdown and all: what shows it on the web reads the same file.
func TestACardWithALinkIsSavedAsTheMarkdownItWasTyped(t *testing.T) {
	const md = "Get the recorder:\n[Download Trecs](https://github.com/solworktech/trecs/releases)"
	h := newHarness(t, nil)
	h.pausePlayback()
	h.openModalOn(2)
	h.waitFor("the modal", func() bool { return h.front() == cueCardPage }, 2*time.Second)
	h.key(tcell.KeyTab)
	h.typeText("Get the recorder:")
	h.key(tcell.KeyEnter)
	h.typeText("[Download Trecs](https://github.com/solworktech/trecs/releases)")
	h.key(tcell.KeyCtrlS)
	h.waitFor("the recording to be written", func() bool { return h.front() == "playback" }, 5*time.Second)
	if got := h.reparsed()[2].IntroCard.Text; got != md {
		t.Errorf("on disk: %q, typed %q", got, md)
	}
	// ... and it is shown, when playback comes to it, as a button.
	h.waitFor("the card, with its button", func() bool {
		s := h.screen()
		return strings.Contains(s, "Download Trecs") && !strings.Contains(s, "https://github.com/solworktech/trecs/releases)")
	}, 4*time.Second)
}

// ---- bold and italics ---------------------------------------------------------------------------------------------------

func TestBoldAndItalicAreStyledAndThePlainTextIsRegular(t *testing.T) {
	l := layoutCueCard("A **bold** and an *italic* and ***both*** word", 100, 30, 0)
	j := strings.Join(l.lines, "\n")
	for _, want := range []string{
		cueBoldTag + "bold" + cueTextTag, cueItalicTag + "italic" + cueTextTag, cueBoldItalicTag + "both" + cueTextTag,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("lacks %q: %q", want, j)
		}
	}
	if strings.Contains(j, "*") {
		t.Errorf("the markers are still in it: %q", j)
	}
	// What is not emphasis is the text it was written as.
	if j := strings.Join(layoutCueCard("2 * 3 * 4 and snake_case_name", 100, 30, 0).lines, "\n"); !strings.Contains(j, "2 * 3 * 4 and snake_case_name") || strings.Contains(j, cueItalicTag) {
		t.Errorf("%q", j)
	}
}

func TestAStyleCarriesOverAWrappedLineAndALineBreak(t *testing.T) {
	// a bold run that has to wrap, and one with a line break in it
	text := "**" + strings.Repeat("bold words that go on and on ", 6) + "end** and then **one\ntwo**"
	l := layoutCueCard(text, 60, 30, 0)
	if len(l.lines) < 4 {
		t.Fatalf("expected the text to wrap: %q", l.lines)
	}
	for i, line := range l.lines {
		if strings.Contains(line, "words") || strings.Contains(line, "end") || strings.Contains(line, "two") {
			if !strings.Contains(line, cueBoldTag) {
				t.Errorf("line %d lost its bold: %q", i, line)
			}
		}
	}
	// ... and the style does not leak past where it ends
	last := l.lines[len(l.lines)-1]
	if strings.HasPrefix(last, cueBoldTag) && !strings.Contains(last, "two") {
		t.Errorf("bold went on past its end: %q", last)
	}
	plain := layoutCueCard("**bold**\nplain after", 100, 30, 0).lines
	if len(plain) != 2 || strings.Contains(plain[1], cueBoldTag) {
		t.Errorf("a line after bold text is bold: %q", plain)
	}
}

// attrs reads what a word on the screen is drawn in.
func (h *harness) attrsOf(word string) (found bool, bold, italic bool) {
	out := make(chan [3]bool, 1)
	h.em.app.QueueUpdate(func() {
		cells, w, ht := h.sim.GetContents()
		for y := 0; y < ht; y++ {
			for x := 0; x+len(word) <= w; x++ {
				ok := true
				for i, r := range []rune(word) {
					c := cells[y*w+x+i].Runes
					if len(c) == 0 || c[0] != r {
						ok = false
						break
					}
				}
				if ok {
					_, _, attr := cells[y*w+x].Style.Decompose()
					out <- [3]bool{true, attr&tcell.AttrBold != 0, attr&tcell.AttrItalic != 0}
					return
				}
			}
		}
		out <- [3]bool{}
	})
	r := <-out
	return r[0], r[1], r[2]
}

func TestOnTheScreenBoldIsBoldItalicIsItalicAndPlainIsNeither(t *testing.T) {
	h := newHarness(t, func(cmds []libtrecs.Command) {
		cmds[0].IntroCard, cmds[0].HasIntroCard = card("Plain then **strong** then *slanted* then ***both***.\n\n**A bold line that is long enough to need wrapping onto a second line of the card, with a last word: omega**", 30, libtrecs.CuePlacementIntro), true
	})
	h.waitFor("the card", func() bool { return strings.Contains(h.screen(), "Plain then") }, 4*time.Second)
	for _, tc := range []struct {
		word         string
		bold, italic bool
	}{
		{"Plain", false, false}, {"strong", true, false}, {"slanted", false, true}, {"both", true, true},
		{"omega", true, false}, // the last word of a wrapped bold line: on the second line, and still bold
	} {
		found, bold, italic := h.attrsOf(tc.word)
		if !found {
			t.Errorf("%q is not on the screen:\n%s", tc.word, h.screen())
			continue
		}
		if bold != tc.bold || italic != tc.italic {
			t.Errorf("%q is bold %v and italic %v, want %v and %v", tc.word, bold, italic, tc.bold, tc.italic)
		}
	}
	if s := h.screen(); strings.Contains(s, "**") || strings.Contains(s, "*slanted*") {
		t.Errorf("a marker is on the screen:\n%s", s)
	}
}
