package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// Cue cards: a card of text shown centred over the terminal, before a command (an intro) or after it (a post), added
// from the command list with Ctrl+A. They are annotations of another kind (see libtrecs.Annotation.Kind), kept beside the
// command's own note, which is the one shown as a panel at the top or bottom.
//
// In playback a card is shown where the recording passes from one command to the next, and always over what is on the screen -
// nothing is cleared for it, and nothing waits for it:
//
//   - The first command's intro is the exception: it is shown before anything is drawn, with the first frame held back, so
//     that it is what the recording begins with.
//   - Every other card is shown as soon as the prompt of the command that begins has rendered (the first frame of a command
//     is its prompt): playback is paused before the next frame, the post card of the command that has ended is shown, then
//     the intro card of the one that begins, each for its own duration, and playback resumes.
//   - A command that also has a note that is previewed has the note first, as it always has (see autoShowAnnotation), and
//     the cards when its prompt has been rendered after it.
//   - The post card of the last command is shown when playback reaches the end of the recording, and stays until playback is
//     replayed or moved - a jump through the command list, which is how both are done.

const (
	cueCardPage           = "cuecard" // the modal's page
	cueCardDefaultSeconds = 5
)

var (
	// The demo's card (the player's .trecs-demo__caption): near-black, a green edge, bold white text.
	cueCardBackground = tcell.NewRGBColor(16, 18, 22)
	cueCardBorder     = tcell.NewRGBColor(76, 175, 80)
)

// ---- the card, drawn ---------------------------------------------------------------------------------------------

// A link in a card (see libtrecs.ParseCueText) is a button here: its label, padded, in the card's green. The one that Tab has
// reached is lighter and underlined; Enter opens it (see openFocusedCTA). The label is joined with no-break spaces, so that a
// button is never cut across two lines.
// The text is regular weight - the demo's card is semibold throughout, but here bold is something the author asks for, and has to
// show - with bold and italic as they are written (**bold**, *italic*; see libtrecs.ParseCueText).
//
// Code is blue: a light blue, #5fafff - the terminal's own blue is dark, and hard to read on this background - and in bold or in
// italics if it is in them. A link in a text that cannot be clicked - a timed annotation - is its label, in green, and its address
// after it. (None of these is underlined: tview 0.42 loses an underline that is set by a tag, as it applies the attributes.)
const (
	cueTextTag           = "[white:#101216:-]"
	cueBoldTag           = "[white:#101216:b]"
	cueItalicTag         = "[white:#101216:i]"
	cueBoldItalicTag     = "[white:#101216:bi]"
	cueCodeTag           = "[#5fafff:#101216:-]"
	cueCodeBoldTag       = "[#5fafff:#101216:b]"
	cueCodeItalicTag     = "[#5fafff:#101216:i]"
	cueCodeBoldItalicTag = "[#5fafff:#101216:bi]"
	cueLinkTag           = "[#6fdc7b:#101216:-]"
	cueAddressTag        = "[#8b949e:#101216:-]"
	cueButtonTag         = "[black:#4caf50:b]"
	cueButtonFocusTag    = "[black:#c8f7cb:b]"
)

var cueStyleTags = []string{
	cueTextTag, cueBoldTag, cueItalicTag, cueBoldItalicTag, cueCodeTag, cueCodeBoldTag, cueCodeItalicTag, cueCodeBoldItalicTag,
	cueLinkTag, cueAddressTag, cueButtonTag, cueButtonFocusTag,
}

// taggedCueText is the text of a card as tview prints it: what was typed is text, not colour tags ([red] is what it says), bold
// and italics are bold and italic, code is blue, and each link is a button, the one numbered focus as the focused one.
func taggedCueText(text string, focus int) string { return taggedMarkdown(text, focus, true) }

// taggedAnnotationText is the same for a timed annotation, whose links cannot be clicked: each is its label and its address.
func taggedAnnotationText(text string) string { return taggedMarkdown(text, -1, false) }

func taggedMarkdown(text string, focus int, buttons bool) string {
	var sb strings.Builder
	n := 0
	for _, seg := range libtrecs.ParseCueText(text) {
		switch {
		case seg.URL != "" && buttons:
			tag := cueButtonTag
			if n == focus {
				tag = cueButtonFocusTag
			}
			n++
			label := strings.ReplaceAll(seg.Text, " ", "\u00a0")
			sb.WriteString(tag + "\u00a0" + tview.Escape(label) + "\u00a0" + cueTextTag)
		case seg.URL != "":
			sb.WriteString(cueLinkTag + tview.Escape(seg.Text) + cueTextTag + " " + cueAddressTag + "(" + tview.Escape(seg.URL) + ")" + cueTextTag)
		case seg.Code:
			tag := cueCodeTag
			switch {
			case seg.Bold && seg.Italic:
				tag = cueCodeBoldItalicTag
			case seg.Bold:
				tag = cueCodeBoldTag
			case seg.Italic:
				tag = cueCodeItalicTag
			}
			sb.WriteString(tag + tview.Escape(seg.Text) + cueTextTag)
		case seg.Bold && seg.Italic:
			sb.WriteString(cueBoldItalicTag + tview.Escape(seg.Text) + cueTextTag)
		case seg.Bold:
			sb.WriteString(cueBoldTag + tview.Escape(seg.Text) + cueTextTag)
		case seg.Italic:
			sb.WriteString(cueItalicTag + tview.Escape(seg.Text) + cueTextTag)
		default:
			sb.WriteString(tview.Escape(seg.Text))
		}
	}
	return sb.String()
}

// wrapCueLines wraps tagged text to a width: each of its lines is wrapped on its own, and a style that runs on from one line to
// the next is carried onto it.
func wrapCueLines(tagged string, width int) []string {
	var lines []string
	for _, paragraph := range strings.Split(strings.Trim(tagged, "\n"), "\n") {
		paragraph = strings.TrimRight(paragraph, " \t\r")
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, tview.WordWrap(paragraph, width)...)
	}
	return carryCueStyles(lines)
}

// cueOverlay draws the card centred in the area it is given, over what is under it. Nothing is drawn while it has no text.
type cueOverlay struct {
	*tview.Box
	text  string
	links []libtrecs.CueSegment // the links in it: its buttons
	focus int                   // the button that has the focus
}

func newCueOverlay() *cueOverlay { return &cueOverlay{Box: tview.NewBox()} }

// setText is what is shown now: "" for nothing.
func (c *cueOverlay) setText(text string) {
	c.text, c.focus = text, 0
	c.links = libtrecs.CueLinks(text)
}

// moveFocus goes on to the next button, or back to the one before, round.
func (c *cueOverlay) moveFocus(by int) {
	if n := len(c.links); n > 0 {
		c.focus = ((c.focus+by)%n + n) % n
	}
}

// focused is the link of the button that has the focus.
func (c *cueOverlay) focused() (libtrecs.CueSegment, bool) {
	if c.focus < 0 || c.focus >= len(c.links) {
		return libtrecs.CueSegment{}, false
	}
	return c.links[c.focus], true
}

type cueLayout struct {
	lines           []string // wrapped, and tagged for tview
	w, h, left, top int      // the card: its size, and its place in the area
}

// layoutCueCard is the card's size and place in an area, and its text wrapped to fit: no wider than 80% of the area, as the
// demo's is, and centred in it. focus is the button to draw as the focused one.
func layoutCueCard(text string, areaW, areaH, focus int) cueLayout {
	return layoutTaggedCard(taggedCueText(text, focus), areaW, areaH)
}

// layoutTaggedCard is the card's size and place - centred, no wider than 80% of the area - for text that is tagged already.
func layoutTaggedCard(tagged string, areaW, areaH int) cueLayout {
	const padX, padY = 2, 1
	if areaW < 12 || areaH < 5 {
		return cueLayout{}
	}
	maxW := areaW * 80 / 100
	if maxW < 12 {
		maxW = areaW
	}
	maxText := maxW - 2 - 2*padX

	lines := wrapCueLines(tagged, maxText)
	if maxLines := areaH - 2 - 2*padY; len(lines) > maxLines && maxLines >= 1 { // as tall as the area, and no more
		lines = append(lines[:maxLines-1:maxLines-1], "…")
	}
	widest := 0
	for _, l := range lines {
		if w := tview.TaggedStringWidth(l); w > widest {
			widest = w
		}
	}
	w, h := widest+2+2*padX, len(lines)+2+2*padY
	if h > areaH {
		h = areaH
	}
	return cueLayout{lines: lines, w: w, h: h, left: (areaW - w) / 2, top: (areaH - h) / 2}
}

// carryCueStyles starts each line in the style that the line before it ended in. A line is printed on its own, so a bold run
// that wraps - or has a line break in it - would otherwise be bold on its first line only.
func carryCueStyles(lines []string) []string {
	active := ""
	for i, l := range lines {
		best, at := "", -1
		for _, tag := range cueStyleTags {
			if k := strings.LastIndex(l, tag); k > at {
				best, at = tag, k
			}
		}
		if active != "" && l != "" {
			lines[i] = active + l
		}
		if best != "" {
			active = best
			if best == cueTextTag {
				active = ""
			}
		}
	}
	return lines
}

// drawRoundedBox fills a box on the card's background and draws its border, in a colour.
func drawRoundedBox(screen tcell.Screen, left, top, w, h int, border tcell.Color) {
	style := tcell.StyleDefault.Background(cueCardBackground).Foreground(border)
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			r := ' '
			top0, bottom, first, last := row == 0, row == h-1, col == 0, col == w-1
			switch {
			case top0 && first:
				r = '╭'
			case top0 && last:
				r = '╮'
			case bottom && first:
				r = '╰'
			case bottom && last:
				r = '╯'
			case top0 || bottom:
				r = '─'
			case first || last:
				r = '│'
			}
			screen.SetContent(left+col, top+row, r, nil, style)
		}
	}
}

func (c *cueOverlay) Draw(screen tcell.Screen) {
	if c.text == "" {
		return
	}
	x, y, w, h := c.GetRect()
	l := layoutCueCard(c.text, w, h, c.focus)
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
}

// showCueCard puts a card on the screen, and hideCueCard takes it off. Playback's page is a stack: the playback layout, and
// the card over it.
func (em *EditorMode) showCueCard(text string) {
	em.cueOverlay.setText(text)
	em.playbackLayers.ShowPage("card")
}

func (em *EditorMode) hideCueCard() {
	em.cueOverlay.setText("")
	em.playbackLayers.HidePage("card")
}

// ---- the card's buttons ------------------------------------------------------------------------------------------

// ctaHint is what the status line says while a card with buttons is up: where Enter goes, and how to reach the others. "" if it has none.
func (em *EditorMode) ctaHint() string {
	if em.cueOverlay == nil || len(em.cueOverlay.links) == 0 {
		return ""
	}
	link, _ := em.cueOverlay.focused()
	hint := "   [yellow]Enter[white] Open " + tview.Escape(link.URL)
	if len(em.cueOverlay.links) > 1 {
		hint += "   [yellow]Tab[white] Next link"
	}
	return hint
}

// buttonStatus is the status line while a card with buttons is up: what it said before - at the end of the recording, that it has
// finished - and the buttons' hint.
func (em *EditorMode) buttonStatus() string {
	base := ""
	if em.cueFinal {
		base = "Finished. Space/P to replay from the start."
	}
	return base + em.currentAnnotationHint()
}

// handleCueButtonKey is for the keys of a card that has buttons while it is up - timed, or the last one, which stays: Enter
// opens the one that has the focus and Tab goes on to the next. True if the key was one of them and has been dealt with.
func (em *EditorMode) handleCueButtonKey(event *tcell.EventKey) bool {
	if em.cueOverlay == nil || len(em.cueOverlay.links) == 0 {
		return false
	}
	switch event.Key() {
	case tcell.KeyEnter:
		em.openFocusedCTA()
	case tcell.KeyTab:
		em.cueOverlay.moveFocus(1)
		em.setStatus(em.buttonStatus())
	case tcell.KeyBacktab:
		em.cueOverlay.moveFocus(-1)
		em.setStatus(em.buttonStatus())
	default:
		return false
	}
	return true
}

// openFocusedCTA opens the link of the button that has the focus. The card stays, and goes on as it was: a timed one is still
// timed. Only web links are ever opened (the parser makes buttons of no others; this is the second check, at the door).
func (em *EditorMode) openFocusedCTA() {
	link, ok := em.cueOverlay.focused()
	if !ok {
		return
	}
	if !strings.HasPrefix(link.URL, "https://") && !strings.HasPrefix(link.URL, "http://") {
		return
	}
	// The address is in the status line either way: if there is no browser to open it - over SSH, say - it can be copied.
	if err := em.openURL(link.URL); err != nil {
		em.setStatus("Could not open the link: " + tview.Escape(err.Error()) + em.currentAnnotationHint())
		return
	}
	em.setStatus("Opened " + tview.Escape(link.URL) + em.currentAnnotationHint())
}

// openInBrowser opens an address in the desktop's own browser, without a shell: the address is an argument, and the program's
// output does not reach the terminal that the editor is drawing on.
func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // not left a zombie
	return nil
}

// ---- the card, in playback ---------------------------------------------------------------------------------------

// firstIntroWanted says whether the intro of the first command is to be shown, before its first frame (see above).
func (em *EditorMode) firstIntroWanted(newIdx int) bool {
	return newIdx == 0 && len(em.commands) > 0 && em.commands[0].HasIntroCard
}

// cardsAfter is the cards to be shown once the prompt of command newIdx has rendered, when playback has passed from command
// prevIdx (-1 after a jump: nothing has ended): the post card of the command that has ended, then the intro card of the one
// that begins - which, for the first command, is not one of them (firstIntroWanted).
func (em *EditorMode) cardsAfter(prevIdx, newIdx int) []*libtrecs.Annotation {
	var cards []*libtrecs.Annotation
	if prevIdx >= 0 && prevIdx < len(em.commands) && newIdx > prevIdx && em.commands[prevIdx].HasPostCard {
		cards = append(cards, &em.commands[prevIdx].PostCard)
	}
	if newIdx > 0 && newIdx < len(em.commands) && em.commands[newIdx].HasIntroCard {
		cards = append(cards, &em.commands[newIdx].IntroCard)
	}
	return cards
}

// cardsAfterWanted is whether there are any. It reads nothing but the commands, so that it can be asked from the player's
// goroutine, as the frame callback does: the display belongs to the UI's.
func (em *EditorMode) cardsAfterWanted(prevIdx, newIdx int) bool {
	return len(em.cardsAfter(prevIdx, newIdx)) > 0
}

// runCues shows the cards one after the other, each for its own duration, over whatever is on the screen, and then calls
// done - with skipped set if Ctrl+X ended it. The UI goroutine's. Playback is paused for the whole of it (the caller has
// seen to that), and the same guard as for a note that is previewed keeps other bindings out (see handleInput).
func (em *EditorMode) runCues(cards []*libtrecs.Annotation, done func(skipped bool)) {
	em.annotationModalVisible = true
	em.annotationModalIsTop = true
	em.cueGen++
	em.cueActive = true
	em.cueSteps = cards
	em.cueDone = done
	em.setStatus(em.currentAnnotationHint())
	em.advanceCues(em.cueGen)
}

// advanceCues shows the next card, or ends it all; gen is the sequence it belongs to, so that a timer that fires after the
// sequence has been ended (a jump, a skip) does nothing.
func (em *EditorMode) advanceCues(gen int) {
	if gen != em.cueGen || !em.cueActive {
		return
	}
	if len(em.cueSteps) == 0 {
		em.endCues(false)
		return
	}
	card := em.cueSteps[0]
	em.cueSteps = em.cueSteps[1:]
	em.showCueCard(card.Text)
	em.setStatus(em.currentAnnotationHint()) // with the buttons' hint, if the card has them
	wait := time.Duration(card.DurationSeconds) * time.Second
	if wait <= 0 {
		wait = cueCardDefaultSeconds * time.Second
	}
	em.annotationTimer = time.AfterFunc(wait, func() {
		em.app.QueueUpdateDraw(func() { em.advanceCues(gen) })
	})
}

// endCues takes the card off and hands over to what was waiting for it.
func (em *EditorMode) endCues(skipped bool) {
	done := em.cueDone
	em.cueGen++
	em.cueActive = false
	em.cueSteps = nil
	em.cueDone = nil
	em.hideCueCard()
	if em.annotationTimer != nil {
		em.annotationTimer.Stop()
		em.annotationTimer = nil
	}
	em.annotationModalVisible = false
	if done != nil {
		done(skipped)
	}
}

// beginInterlude runs on the UI goroutine with the first frame of command newIdx held back and playback paused (see
// makeFrameCallback): the first command's intro is shown, if it has one, then the note's own preview takes over if the command
// has one, and the held frame goes out. cardsAfter says whether cards follow, once that frame - the prompt - has been drawn.
func (em *EditorMode) beginInterlude(prevIdx, newIdx int, frame libtrecs.TerminalFrame, cardsAfter bool) {
	em.wasPlayingBeforeAnnotation = true
	held := frame
	em.pendingAnnotationFrame = &held
	em.cueAfter = nil
	if cardsAfter {
		em.cueAfter = &cueAfter{prev: prevIdx, idx: newIdx}
	}
	var intro []*libtrecs.Annotation
	if em.firstIntroWanted(newIdx) {
		intro = []*libtrecs.Annotation{&em.commands[0].IntroCard}
	}
	em.cueTarget = newIdx
	em.runCues(intro, func(skipped bool) {
		idx := em.cueTarget
		if !skipped && idx >= 0 && idx < len(em.commands) && em.commands[idx].HasAnnotation && em.commands[idx].Annotation.DurationSeconds > 0 {
			// The note's own preview takes it from here, with the frame that is held (and the cards that are to follow it).
			em.autoShowAnnotation(idx, *em.pendingAnnotationFrame)
			return
		}
		em.deliverHeldFrame()
	})
}

// deliverHeldFrame puts the frame that was held back on the screen as it is, and goes on: with the cards that follow it, or
// with playback.
func (em *EditorMode) deliverHeldFrame() {
	if em.pendingAnnotationFrame != nil {
		frame := *em.pendingAnnotationFrame
		em.pendingAnnotationFrame = nil
		em.display.Feed(frame.Data)
		em.playbackView.SetText(em.renderDisplay())
		em.playbackView.ScrollToEnd()
		em.progressBar.SetProgress(frame.Timestamp, em.totalDurationMs)
	}
	em.resumeOrCards()
}

// resumeOrCards is what follows a frame that was held back and has now been drawn: the cards that wait for it, if there are
// any, and otherwise playback.
func (em *EditorMode) resumeOrCards() {
	if a := em.cueAfter; a != nil {
		em.cueAfter = nil
		if em.beginCards(a.prev, a.idx) {
			return
		}
	}
	em.player.Resume()
	em.setStatus(em.currentAnnotationHint())
}

// beginCards shows the cards that go with the prompt of command newIdx, which is on the screen, and resumes playback when they
// are done. False, and nothing done, if there are none.
func (em *EditorMode) beginCards(prevIdx, newIdx int) bool {
	cards := em.cardsAfter(prevIdx, newIdx)
	if len(cards) == 0 {
		return false
	}
	em.runCues(cards, func(bool) {
		em.player.Resume()
		em.setStatus(em.currentAnnotationHint())
	})
	return true
}

// skipCues is Ctrl+X while cards are being shown: they end, and what was waiting for them goes on.
func (em *EditorMode) skipCues() {
	em.endCues(true)
}

// cancelCue takes whatever card is up off the screen and ends any sequence without handing over, for a caller that is moving
// playback elsewhere (see cancelAnnotation).
func (em *EditorMode) cancelCue() {
	em.cueGen++
	em.cueActive = false
	em.cueSteps = nil
	em.cueDone = nil
	em.cueAfter = nil
	em.cueFinal = false
	if em.cueOverlay != nil {
		em.hideCueCard()
	}
	em.hideTimed() // a seek takes a timed annotation away too
}

// showFinalCard is called when playback has reached the end of the recording: the post card of the last command goes up and
// stays, without a timer, until playback is replayed or moved (cancelCue).
func (em *EditorMode) showFinalCard(idx int) {
	if idx != len(em.commands)-1 || !em.commands[idx].HasPostCard {
		return
	}
	em.showCueCard(em.commands[idx].PostCard.Text)
	em.cueFinal = true
}

// cueAfter: cards that wait for a held frame - the prompt of command idx, after command prev - to be drawn.
type cueAfter struct{ prev, idx int }

// ---- the select ----------------------------------------------------------------------------------------------------

// cueSelect chooses between a few options, and opens to show them - as tview's drop-down does, but without the two things that
// make it wrong in a small modal: it shows the chosen option in its field and again at the head of the list, and the list is
// drawn over whatever is below it. This one shows the list in place of the field, in a row of its own that nothing else uses,
// so each option is on the screen once, and none of the modal is covered. It is as tall as its options (it does not draw them
// until it is open).
type cueSelect struct {
	*tview.Box
	label    string
	options  []string
	current  int // the chosen option
	cursor   int // the one marked while it is open
	open     bool
	selected func(index int)
	mark     tcell.Color // the colour of the option that is marked; the green of a card's edge, if none is set
}

func newCueSelect(label string, options []string) *cueSelect {
	return &cueSelect{Box: tview.NewBox(), label: label, options: options}
}

// SetMarkColor sets the background of the marked option, for a select that is on a green of its own.
func (s *cueSelect) SetMarkColor(c tcell.Color) *cueSelect {
	s.mark = c
	return s
}

func (s *cueSelect) SetSelectedFunc(f func(index int)) *cueSelect {
	s.selected = f
	return s
}

// SetCurrentOption chooses an option, and says so to the function that was set.
func (s *cueSelect) SetCurrentOption(index int) *cueSelect {
	if index < 0 || index >= len(s.options) {
		return s
	}
	s.current, s.cursor = index, index
	if s.selected != nil {
		s.selected(index)
	}
	return s
}

func (s *cueSelect) GetCurrentOption() (int, string) { return s.current, s.options[s.current] }
func (s *cueSelect) IsOpen() bool                    { return s.open }

// Blur closes it: it is not left open behind the focus.
func (s *cueSelect) Blur() {
	s.open = false
	s.Box.Blur()
}

func (s *cueSelect) Draw(screen tcell.Screen) {
	s.DrawForSubclass(screen, s)
	x, y, w, _ := s.GetInnerRect()
	labelWidth := tview.TaggedStringWidth(s.label)
	tview.Print(screen, s.label, x, y, w, tview.AlignLeft, tcell.ColorWhite)
	x += labelWidth
	w -= labelWidth

	optionWidth := 0
	for _, o := range s.options {
		if ow := tview.TaggedStringWidth(o); ow > optionWidth {
			optionWidth = ow
		}
	}
	plain := tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite)
	markColor := cueCardBorder
	if s.mark != tcell.ColorDefault {
		markColor = s.mark
	}
	marked := tcell.StyleDefault.Background(markColor).Foreground(tcell.ColorBlack)
	draw := func(row int, text string, style tcell.Style) {
		for col := 0; col < optionWidth+2 && col < w; col++ {
			screen.SetContent(x+col, y+row, ' ', nil, style)
		}
		for col, r := range []rune(" " + text) {
			if col < w {
				screen.SetContent(x+col, y+row, r, nil, style)
			}
		}
	}
	if !s.open {
		style := plain
		if s.HasFocus() {
			style = marked
		}
		draw(0, s.options[s.current]+" ▾", style)
		return
	}
	for i, o := range s.options {
		style := plain
		if i == s.cursor {
			style = marked
		}
		draw(i, o, style)
	}
}

func (s *cueSelect) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return s.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		if !s.open {
			if event.Key() == tcell.KeyEnter || event.Key() == tcell.KeyDown || (event.Key() == tcell.KeyRune && event.Rune() == ' ') {
				s.open, s.cursor = true, s.current
			}
			return
		}
		switch event.Key() {
		case tcell.KeyUp:
			if s.cursor > 0 {
				s.cursor--
			}
		case tcell.KeyDown:
			if s.cursor < len(s.options)-1 {
				s.cursor++
			}
		case tcell.KeyEnter:
			s.open = false
			s.SetCurrentOption(s.cursor)
		case tcell.KeyRune:
			if event.Rune() == ' ' {
				s.open = false
				s.SetCurrentOption(s.cursor)
			}
		case tcell.KeyEscape:
			s.open = false
		}
	})
}

// ---- the modal ---------------------------------------------------------------------------------------------------

type cueModal struct {
	frame      tview.Primitive // the modal, centred: what is added as a page
	box        *tview.Flex
	command    *tview.TextView
	kind       *cueSelect
	text       *tview.TextArea
	duration   *tview.InputField
	message    *tview.TextView
	save       *tview.Button
	del        *tview.Button
	discard    *tview.Button
	buttons    *tview.Flex       // the row they are in: Delete is in it only when there is a card to delete
	delEnabled bool              // the command has a card of the chosen type to delete
	order      []tview.Primitive // what Tab goes through
	idx        int               // the command it is for
}

var cueKinds = []string{"Intro (before command)", "Post (after command)"}

func cuePlacement(kindIndex int) string {
	if kindIndex == 1 {
		return libtrecs.CuePlacementPost
	}
	return libtrecs.CuePlacementIntro
}

func (em *EditorMode) createCueCardModal() {
	m := &cueModal{}
	em.cueModal = m

	m.command = tview.NewTextView().SetDynamicColors(true)

	m.kind = newCueSelect("Type: ", cueKinds).SetSelectedFunc(em.loadCueFields)

	m.text = tview.NewTextArea()
	m.text.SetWrap(true)
	m.text.SetBorder(true).SetTitle(" Content - **bold** *italic* " + tview.Escape("[link](https://url)") + " ")

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
	m.save = button("Save  ^S", em.saveCueCard)
	m.del = button("Delete cue card", em.deleteCueCard)
	m.discard = button("Discard  Esc", em.closeCueCardModal)

	m.buttons = opaque(tview.NewFlex(), false).SetDirection(tview.FlexColumn)
	em.layoutCueButtons()

	m.box = opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).
		AddItem(m.command, 1, 0, false).
		AddItem(m.kind, 2, 0, true). // the second row is where it shows its options when it is open
		AddItem(m.text, 8, 0, false).
		AddItem(m.duration, 3, 0, false).
		AddItem(m.message, 1, 0, false).
		AddItem(m.buttons, 1, 0, false)
	m.box.SetBorder(true).SetTitle(" Add cue card ").SetBorderPadding(0, 0, 1, 1)
	m.box.SetBackgroundColor(tcell.ColorBlack)

	m.order = []tview.Primitive{m.kind, m.text, m.duration, m.save, m.del, m.discard}

	// Centred on the editor, as the demo's card is on its player.
	m.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(m.box, 62, 0, true).AddItem(nil, 0, 1, false), 19, 0, true).
		AddItem(nil, 0, 1, false)
}

// layoutCueButtons puts the buttons in their row: Delete is among them when there is a card to delete, and is not there at all
// when there is none.
func (em *EditorMode) layoutCueButtons() {
	m := em.cueModal
	m.buttons.Clear()
	m.buttons.AddItem(nil, 0, 1, false).AddItem(m.save, 10, 0, false)
	if m.delEnabled {
		m.buttons.AddItem(nil, 1, 0, false).AddItem(m.del, 17, 0, false)
	}
	m.buttons.AddItem(nil, 1, 0, false).AddItem(m.discard, 14, 0, false).AddItem(nil, 0, 1, false)
}

// openCueCardModal opens the modal for the command in focus in the command list. If the command has a card of one kind only,
// that is the kind it opens on, and what it shows.
func (em *EditorMode) openCueCardModal() {
	if r, ok := em.currentListRow(); ok && r.img != "" {
		em.setStatus("Cue cards go with a command, not an image: choose the command.")
		return
	}
	idx := em.currentListCommand()
	if idx < 0 || idx >= len(em.commands) {
		return
	}
	if em.deletedCommands[idx] {
		em.setStatus(fmt.Sprintf("Command %d is marked for deletion: it cannot have a cue card.", idx+1))
		return
	}
	m := em.cueModal
	m.idx = idx
	m.command.SetText(fmt.Sprintf("[yellow]Command %d of %d:[white] %s", idx+1, len(em.commands), tview.Escape(libtrecs.CollapseCommand(em.commands[idx].InputText))))
	m.message.SetText("")
	if em.pages.HasPage(cueCardPage) {
		em.pages.RemovePage(cueCardPage)
	}
	m.text.SetText("", true)
	m.duration.SetText(strconv.Itoa(cueCardDefaultSeconds))
	kind := 0
	if em.commands[idx].HasPostCard && !em.commands[idx].HasIntroCard {
		kind = 1
	}
	m.kind.SetCurrentOption(kind) // which loads that card
	em.pages.AddPage(cueCardPage, m.frame, true, true)
	em.app.SetFocus(m.kind)
}

func (em *EditorMode) closeCueCardModal() {
	if em.pages.HasPage(cueCardPage) {
		em.pages.RemovePage(cueCardPage)
	}
	em.app.SetFocus(em.commandList)
}

// existingCard is the command's card of a placement, if it has one.
func (em *EditorMode) existingCard(idx int, placement string) (*libtrecs.Annotation, bool) {
	cmd := &em.commands[idx]
	if placement == libtrecs.CuePlacementPost {
		return &cmd.PostCard, cmd.HasPostCard
	}
	return &cmd.IntroCard, cmd.HasIntroCard
}

// loadCueFields is called when the type is chosen: if the command already has a card of that type, that is what the
// modal shows, to be changed or deleted; if it has not, what has been typed is left as it is.
func (em *EditorMode) loadCueFields(kindIndex int) {
	m := em.cueModal
	if m.idx < 0 || m.idx >= len(em.commands) {
		return
	}
	placement := cuePlacement(kindIndex)
	card, had := em.existingCard(m.idx, placement)
	m.delEnabled = had
	em.layoutCueButtons()
	if had {
		m.text.SetText(card.Text, true)
		if card.DurationSeconds > 0 {
			m.duration.SetText(strconv.Itoa(card.DurationSeconds))
		}
		m.message.SetText("[gray]Editing the card.")
		return
	}
	if placement == libtrecs.CuePlacementPost && m.idx == len(em.commands)-1 {
		m.message.SetText("[gray]Stays on screen until playback is replayed or moved.")
		return
	}
	m.message.SetText("")
}

func (em *EditorMode) cueError(msg string, focus tview.Primitive) {
	em.cueModal.message.SetText("[red]" + tview.Escape(msg))
	em.app.SetFocus(focus)
}

// saveCueCard puts the card on the command and writes the recording: it is in the file when this returns, and playback is at
// the command, to be seen.
func (em *EditorMode) saveCueCard() {
	m := em.cueModal
	idx := m.idx
	if idx < 0 || idx >= len(em.commands) {
		em.closeCueCardModal()
		return
	}
	kindIndex, _ := m.kind.GetCurrentOption()
	placement := cuePlacement(kindIndex)
	text := strings.TrimSpace(m.text.GetText())
	if text == "" {
		em.cueError("Enter the text of the card.", m.text)
		return
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(m.duration.GetText()))
	if err != nil || seconds < 1 {
		em.cueError("The duration is a whole number of seconds, 1 or more.", m.duration)
		return
	}

	// What it is replacing keeps its id and its date, so that it is the same card to everything that has read the file.
	card, had := em.existingCard(idx, placement)
	updated := libtrecs.Annotation{ID: card.ID, CreatedAt: card.CreatedAt, Text: text, DurationSeconds: seconds, Kind: libtrecs.AnnotationKindCue, Placement: placement}
	if placement == libtrecs.CuePlacementPost {
		em.commands[idx].PostCard, em.commands[idx].HasPostCard = updated, true
	} else {
		em.commands[idx].IntroCard, em.commands[idx].HasIntroCard = updated, true
	}
	em.editedCommands[idx] = true
	verb := "added to"
	if had {
		verb = "updated on"
	}
	em.writeCueCards(fmt.Sprintf("Cue card (%s) %s command %d.", placement, verb, idx+1))
}

// deleteCueCard removes the card of the chosen type from the command, and writes the recording.
func (em *EditorMode) deleteCueCard() {
	m := em.cueModal
	idx := m.idx
	if idx < 0 || idx >= len(em.commands) || !m.delEnabled {
		return
	}
	kindIndex, _ := m.kind.GetCurrentOption()
	placement := cuePlacement(kindIndex)
	if placement == libtrecs.CuePlacementPost {
		em.commands[idx].PostCard, em.commands[idx].HasPostCard = libtrecs.Annotation{}, false
	} else {
		em.commands[idx].IntroCard, em.commands[idx].HasIntroCard = libtrecs.Annotation{}, false
	}
	em.editedCommands[idx] = true
	em.writeCueCards(fmt.Sprintf("Cue card (%s) deleted from command %d.", placement, idx+1))
}

// writeCueCards writes the recording, with the cards as they now are. It reloads the recording and puts playback at the
// command, which is what takes the modal's page off the screen; if it cannot be written, the modal stays, with why.
func (em *EditorMode) writeCueCards(done string) {
	backup, err := em.writeRecording(em.cueModal.idx)
	if err != nil {
		em.cueModal.message.SetText("[red]Not saved: " + tview.Escape(err.Error()))
		return
	}
	if em.pages.HasPage(cueCardPage) {
		em.pages.RemovePage(cueCardPage)
	}
	em.setStatus(done + " Recording saved; backup: " + backup)
}

// cycleCueFocus moves Tab (or Shift+Tab) on through the modal's elements - all but a button that cannot be used.
func (em *EditorMode) cycleCueFocus(back bool) {
	m := em.cueModal
	cur := em.app.GetFocus()
	at := 0
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

// handleCueCardInput is handleInput's for the modal's page. The type's list, while it is open, has Esc and Tab to itself.
func (em *EditorMode) handleCueCardInput(event *tcell.EventKey) *tcell.EventKey {
	listOpen := em.cueModal.kind.IsOpen()
	switch event.Key() {
	case tcell.KeyCtrlS:
		em.saveCueCard()
		return nil
	case tcell.KeyEscape:
		if listOpen {
			return event
		}
		em.closeCueCardModal()
		return nil
	case tcell.KeyTab, tcell.KeyBacktab:
		if listOpen {
			return event
		}
		em.cycleCueFocus(event.Key() == tcell.KeyBacktab)
		return nil
	}
	return event
}
