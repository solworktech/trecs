package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// Timed annotations: an annotation at a second of playback, added from the playback screen with Ctrl+A. It is not with a command,
// as a note and a card are: it is shown when playback reaches its time, for its duration, and playback goes on - nothing is
// paused, nothing is cleared. It is shown in one of two styles, which are chosen when it is added: as a card, centred over the
// terminal, or in a box at the bottom left of it. Its text is Markdown: links, bold, italics and code (see
// libtrecs.ParseCueText). It is held by the command its time is in (see libtrecs.AnnotationKindTimed), which is how it moves with
// that command when others are deleted.
//
// On the playback screen, Ctrl+A lists them, to edit one or add another (or adds one, if there are none yet) (it is not done while a preview or cards are holding playback: they are dismissed
// first); Ctrl+X dismisses what is up - a preview, cards that hold playback, or a timed annotation; and Ctrl+T shows or hides
// the annotation of the command being played.

const (
	timedNotePage          = "timednote" // the modal's page
	timedListPage          = "timedlist" // the list of them's
	timedDefaultSeconds    = 5
	timedOverlayMaxPercent = 60
	timedSeekGapMs         = 2000
)

var (
	timedBorder = tcell.NewRGBColor(110, 118, 129)

	// The styles the select offers, in its order; a timed annotation's Placement is the one that is chosen.
	timedStyles = []struct{ label, placement string }{
		{"Centred (over the terminal)", libtrecs.TimedPlacementCentered},
		{"Bottom (left corner)", libtrecs.TimedPlacementBottom},
	}
	timedStyleDefault = 1 // the bottom: what a timed annotation was before there was a choice, and what none is
)

func timedStyleIndex(placement string) int {
	for i, s := range timedStyles {
		if s.placement == placement {
			return i
		}
	}
	return timedStyleDefault
}

// ---- time -----------------------------------------------------------------------------------------------------------------------

// parseClockText reads a time that was typed, to milliseconds: seconds (65), minutes and seconds (1:05, 01:05), or hours, minutes
// and seconds (1:02:05). The seconds are under 60, and the minutes of an hour too.
func parseClockText(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("a time is minutes and seconds, like 01:05")
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("a time is minutes and seconds, like 01:05")
	}
	var total int64
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 || p == "" || strings.ContainsAny(p, "+- ") {
			return 0, fmt.Errorf("a time is minutes and seconds, like 01:05")
		}
		if i > 0 && n >= 60 {
			return 0, fmt.Errorf("minutes and seconds are under 60")
		}
		total = total*60 + n
	}
	return total * 1000, nil
}

// ---- the box ----------------------------------------------------------------------------------------------------------------------

// timedOverlay draws a timed annotation over what is under it: centred, as a card is, or at the bottom left of the area it is
// given. Nothing is drawn while it has no text.
type timedOverlay struct {
	*tview.Box
	text     string
	centered bool
}

func newTimedOverlay() *timedOverlay { return &timedOverlay{Box: tview.NewBox()} }

type timedLayout struct {
	lines           []string
	w, h, left, top int
}

// layoutTimed is the box's size and place in an area: at the bottom left, no wider than 60% of it, its text wrapped.
func layoutTimed(text string, areaW, areaH int) timedLayout {
	const pad = 1
	if areaW < 16 || areaH < 4 {
		return timedLayout{}
	}
	maxW := min(areaW*timedOverlayMaxPercent/100, 76)
	if maxW < 16 {
		maxW = areaW - 2
	}
	lines := wrapCueLines(taggedAnnotationText(text), maxW-2-2*pad)
	if maxLines := areaH - 2 - 2; len(lines) > maxLines && maxLines >= 1 {
		lines = append(lines[:maxLines-1:maxLines-1], "…")
	}
	widest := 0
	for _, l := range lines {
		widest = max(widest, tview.TaggedStringWidth(l))
	}
	w, h := widest+2+2*pad, len(lines)+2
	return timedLayout{lines: lines, w: w, h: h, left: 1, top: max(0, areaH-h-1)}
}

func (t *timedOverlay) Draw(screen tcell.Screen) {
	if t.text == "" {
		return
	}
	x, y, w, h := t.GetRect()
	if t.centered { // the card's: its place, its green edge, its centred text - and its links as they are in a box, not buttons
		l := layoutTaggedCard(taggedAnnotationText(t.text), w, h)
		if l.w == 0 {
			return
		}
		left, top := x+l.left, y+l.top
		drawRoundedBox(screen, left, top, l.w, l.h, cueCardBorder)
		for i, line := range l.lines {
			if 1+1+i >= l.h-1 {
				break
			}
			tview.Print(screen, cueTextTag+line, left+1, top+1+1+i, l.w-2, tview.AlignCenter, tcell.ColorWhite)
		}
		return
	}
	l := layoutTimed(t.text, w, h)
	if l.w == 0 {
		return
	}
	left, top := x+l.left, y+l.top
	drawRoundedBox(screen, left, top, l.w, l.h, timedBorder)
	for i, line := range l.lines {
		if 1+i >= l.h-1 {
			break
		}
		tview.Print(screen, cueTextTag+line, left+2, top+1+i, l.w-4, tview.AlignLeft, tcell.ColorWhite)
	}
}

// ---- in playback ------------------------------------------------------------------------------------------------------------------

// timedList is every timed annotation, in the order of their times. The pointers are into the commands.
func (em *EditorMode) timedList() []*libtrecs.Annotation {
	var all []*libtrecs.Annotation
	for i := range em.commands {
		for j := range em.commands[i].TimedAnnotations {
			all = append(all, &em.commands[i].TimedAnnotations[j])
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp < all[j].Timestamp })
	return all
}

// fireTimed shows the timed annotation whose time playback has reached since the last look - the latest, if there are several -
// and is called with the position of playback, by the progress clock, while it plays. By position, and not by frame: a frame is
// delivered when the recording has something to show, and an annotation in a quiet stretch of it would wait for the next.
func (em *EditorMode) fireTimed(pos int64) {
	if pos < em.timedLast || pos-em.timedLast > timedSeekGapMs {
		// Playback has gone back - a replay - or a long way forward: a seek. Position moves smoothly while it plays, a tick at a
		// time, and what was passed over is not shown.
		em.timedLast = pos - 1
		return
	}
	var due *libtrecs.Annotation
	for _, a := range em.timedList() {
		if a.Timestamp > em.timedLast && a.Timestamp <= pos {
			due = a
		}
	}
	em.timedLast = pos
	if due != nil {
		em.showTimed(due)
	}
}

// resetTimed is for when playback is moved to posMs: nothing that is before it is shown, and what is up goes.
func (em *EditorMode) resetTimed(posMs int64) {
	em.hideTimed()
	em.timedLast = posMs - 1
}

func (em *EditorMode) showTimed(a *libtrecs.Annotation) {
	em.timedOverlay.text = a.Text
	em.timedOverlay.centered = a.Placement == libtrecs.TimedPlacementCentered
	em.playbackLayers.ShowPage("timed")
	em.timedShown = true
	em.timedGen++
	gen := em.timedGen
	if em.timedTimer != nil {
		em.timedTimer.Stop()
	}
	seconds := a.DurationSeconds
	if seconds < 1 {
		seconds = timedDefaultSeconds
	}
	em.timedTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		em.app.QueueUpdateDraw(func() {
			if gen == em.timedGen {
				em.hideTimed()
				em.setStatus(em.currentAnnotationHint())
			}
		})
	})
	em.setStatus(em.currentAnnotationHint())
}

func (em *EditorMode) hideTimed() {
	em.timedGen++
	if em.timedTimer != nil {
		em.timedTimer.Stop()
		em.timedTimer = nil
	}
	em.timedShown = false
	if em.timedOverlay != nil {
		em.timedOverlay.text = ""
		em.playbackLayers.HidePage("timed")
	}
}

// ---- the modal -----------------------------------------------------------------------------------------------------------------

type timedModal struct {
	frame      tview.Primitive
	box        *tview.Flex
	time       *tview.InputField
	style      *cueSelect // how it is shown: the same select as a cue card's Type
	text       *tview.TextArea
	duration   *tview.InputField
	message    *tview.TextView
	save       *tview.Button
	del        *tview.Button
	discard    *tview.Button
	buttons    *tview.Flex
	delEnabled bool              // there is an annotation at the second in the time field to delete
	order      []tview.Primitive // what Tab goes through
	wasPlaying bool              // playback was going on when it opened, and goes on if it is discarded
}

func (em *EditorMode) createTimedModal() {
	m := &timedModal{}
	em.timedModal = m

	m.time = tview.NewInputField()
	m.time.SetAcceptanceFunc(func(text string, ch rune) bool { return (ch >= '0' && ch <= '9' || ch == ':') && len(text) <= 8 })
	m.time.SetFieldBackgroundColor(tcell.ColorBlack).SetFieldTextColor(tcell.ColorWhite)
	m.time.SetBorder(true).SetTitle(" Time (m:ss or h:mm:ss) ")
	m.time.SetChangedFunc(func(string) { em.refreshTimedModal(false) })

	labels := make([]string, len(timedStyles))
	for i, s := range timedStyles {
		labels[i] = s.label
	}
	m.style = newCueSelect("Style: ", labels)

	m.text = tview.NewTextArea()
	m.text.SetWrap(true)
	m.text.SetBorder(true).SetTitle(" Annotation - **bold** *italic* " + tview.Escape("`code` [link](url)") + " ")

	m.duration = tview.NewInputField()
	m.duration.SetAcceptanceFunc(tview.InputFieldInteger)
	m.duration.SetFieldBackgroundColor(tcell.ColorBlack).SetFieldTextColor(tcell.ColorWhite)
	m.duration.SetBorder(true).SetTitle(" Display duration (seconds) ")

	m.message = tview.NewTextView().SetDynamicColors(true)

	button := func(label string, do func()) *tview.Button {
		b := tview.NewButton(label)
		b.SetStyle(tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite))
		b.SetActivatedStyle(tcell.StyleDefault.Background(cueCardBorder).Foreground(tcell.ColorBlack))
		b.SetSelectedFunc(do)
		return b
	}
	m.save = button("Save  ^S", em.saveTimed)
	m.del = button("Delete annotation", em.deleteTimed)
	m.discard = button("Discard  Esc", em.closeTimedModal)
	m.buttons = tview.NewFlex().SetDirection(tview.FlexColumn)
	em.layoutTimedButtons()

	m.box = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.time, 3, 0, false).
		AddItem(m.style, 2, 0, false). // the second row is where it shows its options when it is open
		AddItem(m.text, 8, 0, true).
		AddItem(m.duration, 3, 0, false).
		AddItem(m.message, 1, 0, false).
		AddItem(m.buttons, 1, 0, false)
	m.box.SetBorder(true).SetTitle(" Add annotation ").SetBorderPadding(0, 0, 1, 1)
	m.box.SetBackgroundColor(tcell.ColorBlack)

	m.order = []tview.Primitive{m.time, m.style, m.text, m.duration, m.save, m.del, m.discard}

	m.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(m.box, 64, 0, true).AddItem(nil, 0, 1, false), 21, 0, true).
		AddItem(nil, 0, 1, false)
}

func (em *EditorMode) layoutTimedButtons() {
	m := em.timedModal
	m.buttons.Clear()
	m.buttons.AddItem(nil, 0, 1, false).AddItem(m.save, 10, 0, false)
	if m.delEnabled {
		m.buttons.AddItem(nil, 1, 0, false).AddItem(m.del, 19, 0, false)
	}
	m.buttons.AddItem(nil, 1, 0, false).AddItem(m.discard, 14, 0, false).AddItem(nil, 0, 1, false)
}

// openTimedModal opens the modal at the time playback is at, which is not going on while it is open.
func (em *EditorMode) openTimedModal() {
	em.openTimedModalAt(em.player.Position())
}

func (em *EditorMode) openTimedModalAt(posMs int64) {
	if len(em.commands) == 0 {
		em.setStatus("There are no commands to put an annotation with.")
		return
	}
	m := em.timedModal
	// Playback was going on if it is, or if the list of them, which paused it, was opened from it.
	m.wasPlaying = em.player.GetPlaybackState() == libtrecs.PlaybackPlaying || em.timedListModal.held
	em.timedListModal.held = false
	if m.wasPlaying {
		em.player.Pause()
	}
	if em.pages.HasPage(timedNotePage) {
		em.pages.RemovePage(timedNotePage)
	}
	m.text.SetText("", true)
	m.duration.SetText(strconv.Itoa(timedDefaultSeconds))
	m.style.SetCurrentOption(timedStyleDefault)
	m.message.SetText("")
	m.time.SetText(libtrecs.ClockText(posMs)) // a second of it: the one that playback is in
	em.refreshTimedModal(true)                // and if there is an annotation at that second, it is what is shown
	em.pages.AddPage(timedNotePage, m.frame, true, true)
	em.app.SetFocus(m.text) // the time is where it is, and what is to be written is the next thing
}

func (em *EditorMode) closeTimedModal() {
	if em.pages.HasPage(timedNotePage) {
		em.pages.RemovePage(timedNotePage)
	}
	em.app.SetFocus(em.playbackView)
	if em.timedModal.wasPlaying {
		em.timedModal.wasPlaying = false
		em.player.Resume()
	}
}

// timedAtSecond is the timed annotation that is at a second - there is one at most: saving at a second that has one replaces it.
func (em *EditorMode) timedAtSecond(sec int64) *libtrecs.Annotation {
	for _, a := range em.timedList() {
		if a.Timestamp/1000 == sec {
			return a
		}
	}
	return nil
}

// refreshTimedModal says whether there is an annotation at the second in the time field - Delete is there if there is, and saving
// replaces it. With load, it is what the modal shows: its text and its duration.
func (em *EditorMode) refreshTimedModal(load bool) {
	m := em.timedModal
	var existing *libtrecs.Annotation
	if ms, err := parseClockText(m.time.GetText()); err == nil {
		existing = em.timedAtSecond(ms / 1000)
	}
	had := m.delEnabled
	m.delEnabled = existing != nil
	if had != m.delEnabled {
		em.layoutTimedButtons()
	}
	switch {
	case existing != nil && load:
		m.text.SetText(existing.Text, true)
		m.style.SetCurrentOption(timedStyleIndex(existing.Placement))
		if existing.DurationSeconds > 0 {
			m.duration.SetText(strconv.Itoa(existing.DurationSeconds))
		}
		m.message.SetText("[gray]Editing the annotation at this second.")
	case existing != nil:
		m.message.SetText("[gray]There is one at this second: saving replaces it.")
	default:
		m.message.SetText("")
	}
}

func (em *EditorMode) timedError(msg string, focus tview.Primitive) {
	em.timedModal.message.SetText("[red]" + tview.Escape(msg))
	em.app.SetFocus(focus)
}

// timedHost is the command a time is in: the last that had begun, or the first if the time is before them all.
func (em *EditorMode) timedHost(ms int64) int {
	host := 0
	for i := range em.commands {
		if em.commands[i].HasPrompt && em.commands[i].PromptFrame.Timestamp <= ms || !em.commands[i].HasPrompt && em.commands[i].StartTime <= ms {
			host = i
		} else {
			break
		}
	}
	return host
}

// removeTimedAtSecond takes the annotation at a second out of the command that holds it, and returns it.
func (em *EditorMode) removeTimedAtSecond(sec int64) *libtrecs.Annotation {
	for i := range em.commands {
		list := em.commands[i].TimedAnnotations
		for j := range list {
			if list[j].Timestamp/1000 == sec {
				removed := list[j]
				em.commands[i].TimedAnnotations = append(append([]libtrecs.Annotation(nil), list[:j]...), list[j+1:]...)
				em.editedCommands[i] = true
				return &removed
			}
		}
	}
	return nil
}

// saveTimed puts the annotation at the time, in the command that time is in, and writes the recording.
func (em *EditorMode) saveTimed() {
	m := em.timedModal
	ms, err := parseClockText(m.time.GetText())
	if err != nil {
		em.timedError("Time: "+err.Error()+".", m.time)
		return
	}
	if ms > em.totalDurationMs {
		em.timedError(fmt.Sprintf("The recording is only %s long.", libtrecs.ClockText(em.totalDurationMs)), m.time)
		return
	}
	text := strings.TrimSpace(m.text.GetText())
	if text == "" {
		em.timedError("Enter the text of the annotation.", m.text)
		return
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(m.duration.GetText()))
	if err != nil || seconds < 1 {
		em.timedError("The duration is a whole number of seconds, 1 or more.", m.duration)
		return
	}

	// One at a second: what is there is replaced, and keeps its id and its date, so that it is the same annotation to everything
	// that has read the file.
	styleIndex, _ := m.style.GetCurrentOption()
	a := libtrecs.Annotation{Timestamp: ms, Text: text, DurationSeconds: seconds, Kind: libtrecs.AnnotationKindTimed, Placement: timedStyles[styleIndex].placement}
	verb := "added at"
	if old := em.removeTimedAtSecond(ms / 1000); old != nil {
		a.ID, a.CreatedAt = old.ID, old.CreatedAt
		verb = "replaced at"
	}
	host := em.timedHost(ms)
	em.commands[host].TimedAnnotations = append(em.commands[host].TimedAnnotations, a)
	em.editedCommands[host] = true
	em.writeTimed(host, fmt.Sprintf("Annotation %s %s, in command %d.", verb, libtrecs.ClockText(ms), host+1))
}

// deleteTimed removes the annotation at the second in the time field, and writes the recording.
func (em *EditorMode) deleteTimed() {
	m := em.timedModal
	ms, err := parseClockText(m.time.GetText())
	if err != nil || !m.delEnabled {
		return
	}
	host := em.timedHost(ms)
	if old := em.timedAtSecond(ms / 1000); old != nil {
		host = em.timedHost(old.Timestamp)
	}
	if em.removeTimedAtSecond(ms/1000) == nil {
		return
	}
	em.writeTimed(host, fmt.Sprintf("Annotation at %s deleted.", libtrecs.ClockText(ms)))
}

// writeTimed writes the recording, which reloads it and puts playback at the command; if it cannot be written, the modal stays,
// with why.
func (em *EditorMode) writeTimed(host int, done string) {
	backup, err := em.writeRecording(host)
	if err != nil {
		em.timedModal.message.SetText("[red]Not saved: " + tview.Escape(err.Error()))
		return
	}
	if em.pages.HasPage(timedNotePage) {
		em.pages.RemovePage(timedNotePage)
	}
	em.timedModal.wasPlaying = false
	em.setStatus(done + " Recording saved; backup: " + backup)
}

func (em *EditorMode) cycleTimedFocus(back bool) {
	m := em.timedModal
	cur := em.app.GetFocus()
	at := 2 // the text
	for i, p := range m.order {
		if p == cur {
			at = i
		}
	}
	for range m.order {
		if back {
			at += len(m.order) - 1
		} else {
			at++
		}
		next := m.order[at%len(m.order)]
		if next == tview.Primitive(m.del) && !m.delEnabled {
			continue
		}
		em.app.SetFocus(next)
		return
	}
}

// handleTimedInput is handleInput's for the modal's page.
func (em *EditorMode) handleTimedInput(event *tcell.EventKey) *tcell.EventKey {
	listOpen := em.timedModal.style.IsOpen() // Esc and Tab are the list's, while it is
	switch event.Key() {
	case tcell.KeyCtrlS:
		em.saveTimed()
		return nil
	case tcell.KeyEscape:
		if listOpen {
			return event
		}
		em.closeTimedModal()
		return nil
	case tcell.KeyTab, tcell.KeyBacktab:
		if listOpen {
			return event
		}
		em.cycleTimedFocus(event.Key() == tcell.KeyBacktab)
		return nil
	}
	return event
}
