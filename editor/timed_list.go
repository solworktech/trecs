package main

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// The list of timed annotations, which Ctrl+A shows when there are some: a row for each, with its time and the first line of its
// text. Enter on a row opens the annotation in the modal that edits it; "Jump to annotation" (Ctrl+G) puts playback at the time of
// the one that is in focus - and playback goes on, so it is shown; "Add New" (Ctrl+N) opens that modal at the time playback is at;
// Esc leaves, and gives playback back if it was going on.

const timedListRowMaxRunes = 52 // what fits in a row of the list; a longer first line is cut with "..."

type timedListModal struct {
	frame tview.Primitive
	box   *tview.Flex
	list  *tview.List
	add   *tview.Button
	jump  *tview.Button
	esc   *tview.Button
	times []int64 // the time of each row, in the order of the list
	held  bool    // playback was going on when the list opened, and so goes on when it is left, or the modal it leads to is
}

// timedFirstLine is the first line of an annotation's text that has something on it, shortened to fit a row.
func timedFirstLine(text string) string {
	first := ""
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			first = t
			break
		}
	}
	r := []rune(first)
	if len(r) > timedListRowMaxRunes {
		first = string(r[:timedListRowMaxRunes-3]) + "..."
	}
	return first
}

func (em *EditorMode) createTimedListModal() {
	m := &timedListModal{}
	em.timedListModal = m

	m.list = tview.NewList().ShowSecondaryText(false)
	m.list.SetHighlightFullLine(true)
	m.list.SetBackgroundColor(tcell.ColorBlack)
	m.list.SetBorder(true).SetTitle(" Annotations ")

	button := func(label string, do func()) *tview.Button {
		b := tview.NewButton(label)
		b.SetStyle(tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite))
		b.SetActivatedStyle(tcell.StyleDefault.Background(timedModalMark).Foreground(tcell.ColorBlack))
		b.SetSelectedFunc(do)
		return b
	}
	m.add = button("^N Add New", em.timedListAddNew)
	m.jump = button("^G Jump to annotation", em.timedListJump)
	m.esc = button("ESC Discard", em.closeTimedList)
	buttons := opaque(tview.NewFlex(), false).SetDirection(tview.FlexColumn).
		AddItem(nil, 0, 1, false).AddItem(m.add, 12, 0, false).
		AddItem(nil, 2, 0, false).AddItem(m.jump, 23, 0, false).
		AddItem(nil, 2, 0, false).AddItem(m.esc, 13, 0, false).AddItem(nil, 0, 1, false)
	buttons.SetBackgroundColor(timedModalBackground)

	m.box = opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).
		AddItem(m.list, 0, 1, true).
		AddItem(buttons, 1, 0, false)
	m.box.SetBorder(true).SetTitle(" Timed annotations ").SetBorderPadding(0, 0, 1, 1)
	m.box.SetBackgroundColor(timedModalBackground)

	m.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(m.box, 70, 0, true).AddItem(nil, 0, 1, false), 16, 0, true).
		AddItem(nil, 0, 1, false)
}

// openTimedEntry is what Ctrl+A does: the list of the annotations there are, or the modal to add one if there are none.
func (em *EditorMode) openTimedEntry() {
	if len(em.commands) == 0 {
		em.setStatus("There are no commands to put an annotation with.")
		return
	}
	all := em.timedList()
	if len(all) == 0 {
		em.openTimedModal()
		return
	}
	m := em.timedListModal
	m.held = em.player.GetPlaybackState() == libtrecs.PlaybackPlaying
	if m.held {
		em.player.Pause()
	}
	m.list.Clear()
	m.times = m.times[:0]
	for _, a := range all {
		m.times = append(m.times, a.Timestamp)
		ts := a.Timestamp
		row := fmt.Sprintf("%7s  %s", libtrecs.ClockText(ts), tview.Escape(timedFirstLine(a.Text)))
		m.list.AddItem(row, "", 0, func() { em.timedListEdit(ts) })
	}
	m.list.SetCurrentItem(0)
	if em.pages.HasPage(timedListPage) {
		em.pages.RemovePage(timedListPage)
	}
	em.pages.AddPage(timedListPage, m.frame, true, true)
	em.app.SetFocus(m.list)
}

func (em *EditorMode) removeTimedListPage() {
	if em.pages.HasPage(timedListPage) {
		em.pages.RemovePage(timedListPage)
	}
}

// closeTimedList leaves the list, and gives playback back.
func (em *EditorMode) closeTimedList() {
	em.removeTimedListPage()
	em.app.SetFocus(em.playbackView)
	if m := em.timedListModal; m.held {
		m.held = false
		em.player.Resume()
	}
}

// timedListEdit opens the annotation that is at a time, in the modal, with its text, style and duration.
func (em *EditorMode) timedListEdit(ts int64) {
	em.removeTimedListPage()
	em.openTimedModalAt(ts)
}

// timedListJump puts playback at the time of the annotation in focus, and gives it back: it plays from there, and so shows it.
func (em *EditorMode) timedListJump() {
	m := em.timedListModal
	at := m.list.GetCurrentItem()
	if at < 0 || at >= len(m.times) {
		return
	}
	ts := m.times[at]
	em.removeTimedListPage()
	em.app.SetFocus(em.playbackView)
	m.held = false
	em.jumpToTime(ts)
}

// timedListAddNew opens the modal for a new annotation, at the time playback is at (which is where it stopped, for the list).
func (em *EditorMode) timedListAddNew() {
	em.removeTimedListPage()
	em.openTimedModalAt(em.player.Position())
}

// handleTimedListInput is handleInput's for the list's page.
func (em *EditorMode) handleTimedListInput(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEscape:
		em.closeTimedList()
		return nil
	case tcell.KeyCtrlN:
		em.timedListAddNew()
		return nil
	case tcell.KeyCtrlG:
		em.timedListJump()
		return nil
	}
	return event
}
