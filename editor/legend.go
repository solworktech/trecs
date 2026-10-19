package main

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The legend: the line of keys at the top of a page. It is made of items, and is drawn with as many of them as the width of the
// terminal has room for - the most useful first (an item's prio: 1 is kept longest) - and always ends with the key that opens the
// full help menu, which lists everything the page does. The items that are shown keep the order they were given in, so the legend
// does not move about as the window is made wider or narrower; the others come back when there is room.

// legendGap is what separates the items.
const legendGap = "   "

type legendItem struct {
	key, label string
	prio       int // 1 is kept when there is little room
}

func (it legendItem) width() int {
	return utf8.RuneCountInString(it.key) + 1 + utf8.RuneCountInString(it.label)
}

func (it legendItem) tagged() string { return "[yellow]" + it.key + "[white] " + it.label }

// legendHelpText is the item that is always there: the key, and what it opens.
func legendHelpText(helpKey string) string { return helpKey + " full help menu" }

// fitLegend chooses the items that fit in width, with the help item; shown is in the order of items.
func fitLegend(items []legendItem, helpKey string, width int) (shown []legendItem, help bool) {
	helpW := utf8.RuneCountInString(legendHelpText(helpKey))
	if helpW > width {
		return nil, false
	}
	avail := width - helpW - len(legendGap)
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return items[order[a]].prio < items[order[b]].prio })
	keep := make([]bool, len(items))
	used, n := 0, 0
	for _, i := range order {
		need := items[i].width()
		if n > 0 {
			need += len(legendGap)
		}
		if used+need > avail {
			continue // this one does not fit; a shorter one that comes after it may
		}
		keep[i] = true
		used += need
		n++
	}
	for i, it := range items {
		if keep[i] {
			shown = append(shown, it)
		}
	}
	return shown, true
}

// legendText is the legend as tagged text, for the width.
func legendText(items []legendItem, helpKey string, width int) string {
	shown, help := fitLegend(items, helpKey, width)
	parts := make([]string, 0, len(shown)+1)
	for _, it := range shown {
		parts = append(parts, it.tagged())
	}
	if help {
		parts = append(parts, legendItem{key: helpKey, label: "full help menu"}.tagged())
	}
	return strings.Join(parts, legendGap)
}

// legendBar is the one line a page shows its legend in.
type legendBar struct {
	*tview.Box
	items   []legendItem
	helpKey string
}

func newLegendBar(helpKey string, items ...legendItem) *legendBar {
	return &legendBar{Box: tview.NewBox(), items: items, helpKey: helpKey}
}

func (l *legendBar) Draw(screen tcell.Screen) {
	l.DrawForSubclass(screen, l)
	x, y, w, h := l.GetInnerRect()
	if h < 1 || w < 1 {
		return
	}
	tview.Print(screen, legendText(l.items, l.helpKey, w), x, y, w, tview.AlignLeft, tcell.ColorWhite)
}

// ---- the legends ---------------------------------------------------------------------------------------------------------------------

func playbackLegendItems() []legendItem {
	return []legendItem{
		{"Space/P", "Pause/Resume", 1},
		{"^E", "Edit Current Command", 5},
		{"^A", "Timed Annotations", 3},
		{"^G", "Add Image", 4},
		{"^P/^N", "Prev/Next Command", 6},
		{"^L", "Browse Commands", 2},
		{"^F", "Full Screen", 7},
		{"Q", "Quit", 8},
	}
}

func editLegendItems() []legendItem {
	return []legendItem{
		{"^S", "Stage Change & Continue Editing", 3},
		{"^W", "Write & Resume Playback", 1},
		{"^P/^N", "Prev/Next Command", 5},
		{"^E", "Open in Editor", 6},
		{"Tab", "Toggle Fields", 4},
		{"^D", "Delete Command", 3},
		{"^X", "Exit Editor", 2},
	}
}

func commandListLegendItems() []legendItem {
	return []legendItem{
		{"Up/Down", "Navigate", 5},
		{"^P", "Jump to Playback", 1},
		{"^E", "Jump to Edit", 2},
		{"^A", "Add/Edit cue card", 4},
		{"^D", "Delete Image", 3},
		{"Esc", "Close", 1},
	}
}

// ---- the full help menu ----------------------------------------------------------------------------------------------------------------

const helpPage = "help"

type helpRow struct{ key, text string }

// helpRows is everything a page does with the keyboard.
func helpRows(page string) (title string, rows []helpRow) {
	switch page {
	case "edit":
		return "Editing a command", []helpRow{
			{"^S", "Stage the change and keep editing"},
			{"^W", "Write the recording and go back to playback"},
			{"^X", "Leave the editor, discarding what was not written"},
			{"^P / ^N", "Previous / next command"},
			{"Tab / Shift+Tab", "Move between the fields"},
			{"^E", "Open the field in $EDITOR"},
			{"^D", "Delete the command (it goes when the recording is written)"},
			{"^F", "Full screen"},
			{"F1", "This help"},
			{"^C", "Quit"},
		}
	case "commandlist":
		return "Commands", []helpRow{
			{"Up / Down", "Move through the commands and images"},
			{"^P", "Play from the one in focus"},
			{"^E", "Edit the command, or the image, in focus"},
			{"^A", "Add or edit the cue cards of the command in focus"},
			{"^D", "Delete the image in focus"},
			{"Esc / ^L", "Close the list"},
			{"?", "This help"},
			{"^C", "Quit"},
		}
	}
	return "Playback", []helpRow{
		{"Space / P", "Pause or resume (replay, at the end)"},
		{"^P / ^N", "Previous / next command"},
		{"^L", "Browse the commands and images, and jump to one"},
		{"^E", "Edit the command being played, or the image on show"},
		{"^A", "Timed annotations: list them, edit one, or add one at this time"},
		{"^G", "Add an image at this time"},
		{"^T", "Show or hide the note of the command being played"},
		{"^X", "Dismiss a note, a cue card or an annotation that is up"},
		{"^F / Esc", "Full screen on / off"},
		{"?", "This help"},
		{"Q / ^C", "Quit"},
	}
}

type helpModal struct {
	frame tview.Primitive
	text  *tview.TextView
	back  string // the page it was opened from
}

func (em *EditorMode) openHelp(page string) {
	title, rows := helpRows(page)
	width := 0
	for _, r := range rows {
		width = max(width, utf8.RuneCountInString(r.key))
	}
	var sb strings.Builder
	for _, r := range rows {
		sb.WriteString("[yellow]" + tview.Escape(r.key) + strings.Repeat(" ", width-utf8.RuneCountInString(r.key)) + "[white]  " + tview.Escape(r.text) + "\n")
	}
	sb.WriteString("\n[gray]Esc closes this")
	tv := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetText(sb.String())
	tv.SetBackgroundColor(tcell.ColorBlack)
	box := opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).AddItem(tv, 0, 1, true)
	box.SetBorder(true).SetTitle(" "+title+" - keys ").SetBorderPadding(0, 0, 1, 1)
	box.SetBackgroundColor(tcell.ColorBlack)
	height := len(rows) + 4
	frame := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(box, 84, 0, true).AddItem(nil, 0, 1, false), height, 0, true).
		AddItem(nil, 0, 2, false)
	em.help = &helpModal{frame: frame, text: tv, back: page}
	if em.pages.HasPage(helpPage) {
		em.pages.RemovePage(helpPage)
	}
	em.pages.AddPage(helpPage, frame, true, true)
	em.app.SetFocus(tv)
}

func (em *EditorMode) closeHelp() {
	if em.pages.HasPage(helpPage) {
		em.pages.RemovePage(helpPage)
	}
	if em.help == nil {
		return
	}
	switch em.help.back {
	case "commandlist":
		em.app.SetFocus(em.commandList)
	case "edit":
		em.app.SetFocus(em.inputField)
	default:
		em.app.SetFocus(em.playbackView)
	}
	em.help = nil
}

// handleHelpInput is handleInput's for the help page: it closes on the keys that opened it, Esc and Enter, and swallows the rest (but for
// scrolling, which the text view does).
func (em *EditorMode) handleHelpInput(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEscape, tcell.KeyEnter, tcell.KeyF1:
		em.closeHelp()
		return nil
	case tcell.KeyRune:
		switch event.Rune() {
		case '?', 'q', 'Q':
			em.closeHelp()
			return nil
		}
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
		return event
	}
	return nil
}
