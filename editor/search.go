package main

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// Search, on the playback page: "/" opens a box over what the commands were and what they printed - the web player's search (see
// lib/search.go), with the same two options: Regex (on to begin with) and Match case (off: case counts only if the term has a capital).
// Enter in the term searches; the matches are listed under their command, each with the line before and after it and the term
// highlighted; Enter on a match closes the box and puts playback there, paused, with the line on the screen. Esc closes the box and
// leaves playback paused where it was.

const searchPage = "search"

// The colours of the box are given as hex, and not as names: a name is the terminal's own idea of "gray" or "blue", which some themes
// make too dark to read. These keep a contrast of about 4:1 or better against black, white and the usual dark and light themes (grey
// 6.1:1 on black, 3.5:1 on white; blue 5.2:1 on black, 4.0:1 on white).
const (
	searchDim  = "#8a8a8a" // the lines around a match, and the hints
	searchTime = "#3d7fd9" // the time of a match, (mm:ss)
)

// searchTarget is a line of the results that Enter goes to.
type searchTarget struct {
	result libtrecs.SearchResult
	row    libtrecs.SearchRow
}

// searchLine is a line of the results as drawn. target is its place in the targets, or -1 for a heading or a gap.
type searchLine struct {
	plain, selected string // tagged text; selected is the same line as it is drawn when it has the focus
	target          int
}

// searchResults is the list of matches. It is a primitive of its own because it is made of lines that cannot be moved to: a command's
// heading, the lines of context, the gaps between blocks; Up and Down go from match to match.
type searchResults struct {
	*tview.Box
	lines    []searchLine
	targets  []searchTarget
	sel      int // the target in focus
	offset   int // the first line drawn
	onSelect func(searchTarget)
}

func newSearchResults() *searchResults {
	r := &searchResults{Box: tview.NewBox()}
	r.SetBackgroundColor(tcell.ColorBlack)
	return r
}

func (r *searchResults) set(out libtrecs.SearchOutcome) {
	r.lines, r.targets, r.sel, r.offset = nil, nil, 0, 0
	for _, res := range out.Results {
		r.lines = append(r.lines, searchLine{
			plain:  "[green]" + tview.Escape(res.Command) + "[" + searchDim + "]  (starts at [" + searchTime + "]" + libtrecs.ClockText(res.Time) + "[" + searchDim + "])",
			target: -1,
		})
		r.lines[len(r.lines)-1].selected = r.lines[len(r.lines)-1].plain
		for bi, b := range res.Blocks {
			if bi > 0 {
				r.lines = append(r.lines, searchLine{plain: "[" + searchDim + "]      …", selected: "[" + searchDim + "]      …", target: -1})
			}
			for _, row := range b.Rows {
				l := searchLine{target: -1}
				if row.Match {
					l.target = len(r.targets)
					r.targets = append(r.targets, searchTarget{result: res, row: row})
				}
				l.plain, l.selected = searchRowText(row, false), searchRowText(row, true)
				r.lines = append(r.lines, l)
			}
		}
	}
}

// searchRowText is a line of a result as tagged text: the time of a match, then the text, the matched stretches highlighted.
func searchRowText(row libtrecs.SearchRow, selected bool) string {
	var sb strings.Builder
	switch {
	case selected:
		sb.WriteString("[white:blue]")
	case row.Match:
		sb.WriteString("[" + searchTime + "]")
	}
	if row.Match {
		fmt.Fprintf(&sb, "%-8s", "("+libtrecs.ClockText(row.TS)+")")
	} else {
		sb.WriteString("        ")
	}
	sb.WriteString(" ")
	switch {
	case !row.Match:
		sb.WriteString("[" + searchDim + "]" + tview.Escape(row.Text))
	default:
		if selected {
			sb.WriteString("[white:blue]")
		} else {
			sb.WriteString("[white]")
		}
		for _, p := range row.Pieces {
			if p.Hit {
				sb.WriteString("[black:yellow]" + tview.Escape(p.T))
				if selected {
					sb.WriteString("[white:blue]")
				} else {
					sb.WriteString("[white:-]")
				}
			} else {
				sb.WriteString(tview.Escape(p.T))
			}
		}
	}
	return sb.String()
}

func (r *searchResults) lineOfTarget(t int) int {
	for i, l := range r.lines {
		if l.target == t {
			return i
		}
	}
	return 0
}

// move puts the focus on another match, and scrolls to keep it, and the heading above it when it is the first, in view.
func (r *searchResults) move(by int) {
	if len(r.targets) == 0 {
		return
	}
	r.sel = max(0, min(len(r.targets)-1, r.sel+by))
	r.scrollToSel()
}

func (r *searchResults) scrollToSel() {
	_, _, _, h := r.GetInnerRect()
	if h < 1 {
		return
	}
	at := r.lineOfTarget(r.sel)
	top := at
	if r.sel == 0 || (at > 0 && r.lines[at-1].target == -1) {
		for top > 0 && r.lines[top-1].target == -1 && at-top < 2 {
			top-- // the heading, and the gap, that belong to it
		}
	}
	if top < r.offset {
		r.offset = top
	}
	if at >= r.offset+h {
		r.offset = at - h + 1
	}
	r.offset = max(0, min(r.offset, max(0, len(r.lines)-h)))
}

func (r *searchResults) selected() (searchTarget, bool) {
	if r.sel < 0 || r.sel >= len(r.targets) {
		return searchTarget{}, false
	}
	return r.targets[r.sel], true
}

func (r *searchResults) Draw(screen tcell.Screen) {
	r.DrawForSubclass(screen, r)
	x, y, w, h := r.GetInnerRect()
	if w < 1 || h < 1 {
		return
	}
	r.scrollToSel()
	for i := 0; i < h && r.offset+i < len(r.lines); i++ {
		l := r.lines[r.offset+i]
		text := l.plain
		if l.target >= 0 && l.target == r.sel && r.HasFocus() {
			text = l.selected
			style := tcell.StyleDefault.Background(tcell.ColorBlue)
			for xx := x; xx < x+w; xx++ {
				screen.SetContent(xx, y+i, ' ', nil, style)
			}
		}
		tview.Print(screen, text, x, y+i, w, tview.AlignLeft, tcell.ColorWhite)
	}
}

func (r *searchResults) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return r.WrapInputHandler(func(event *tcell.EventKey, _ func(p tview.Primitive)) {
		_, _, _, h := r.GetInnerRect()
		switch event.Key() {
		case tcell.KeyUp:
			r.move(-1)
		case tcell.KeyDown:
			r.move(1)
		case tcell.KeyPgUp:
			r.move(-max(1, h/2))
		case tcell.KeyPgDn:
			r.move(max(1, h/2))
		case tcell.KeyHome:
			r.move(-len(r.targets))
		case tcell.KeyEnd:
			r.move(len(r.targets))
		case tcell.KeyEnter:
			if t, ok := r.selected(); ok && r.onSelect != nil {
				r.onSelect(t)
			}
		}
	})
}

// ---- the box -------------------------------------------------------------------------------------------------------------------

type searchModal struct {
	frame     tview.Primitive
	term      *tview.InputField
	matchCase *tview.Checkbox
	regex     *tview.Checkbox
	status    *tview.TextView
	results   *searchResults
	index     []libtrecs.SearchEntry
	stopped   bool // opening the box stopped playback
}

func (em *EditorMode) createSearchModal() {
	m := &searchModal{}
	em.searchModal = m

	m.term = tview.NewInputField().SetLabel("Find: ").SetFieldWidth(0)
	m.term.SetFieldBackgroundColor(tcell.ColorDarkSlateGray)
	m.term.SetBackgroundColor(tcell.ColorBlack)
	m.matchCase = tview.NewCheckbox().SetLabel("Match case (Alt+I) ")
	m.regex = tview.NewCheckbox().SetLabel("Regex (Alt+R) ").SetChecked(true)
	for _, c := range []*tview.Checkbox{m.matchCase, m.regex} {
		c.SetBackgroundColor(tcell.ColorBlack)
		c.SetLabelColor(tcell.ColorWhite)
		c.SetFieldBackgroundColor(tcell.ColorDarkSlateGray)
		c.SetFieldTextColor(tcell.ColorWhite)
	}
	m.status = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	m.status.SetBackgroundColor(tcell.ColorBlack)
	m.results = newSearchResults()
	m.results.onSelect = em.seekToSearchTarget
	hint := tview.NewTextView().SetDynamicColors(true).SetText("[" + searchDim + "]Enter search / go to the match   Tab move   Esc close")
	hint.SetBackgroundColor(tcell.ColorBlack)

	options := opaque(tview.NewFlex(), false).SetDirection(tview.FlexColumn).
		AddItem(m.matchCase, 22, 0, false).AddItem(nil, 2, 0, false).AddItem(m.regex, 18, 0, false).AddItem(nil, 0, 1, false)
	options.SetBackgroundColor(tcell.ColorBlack)

	box := opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).
		AddItem(m.term, 1, 0, true).
		AddItem(options, 1, 0, false).
		AddItem(m.status, 2, 0, false).
		AddItem(m.results, 0, 1, false).
		AddItem(hint, 1, 0, false)
	box.SetBorder(true).SetTitle(" Find in the recording ").SetBorderPadding(0, 0, 1, 1)
	box.SetBackgroundColor(tcell.ColorBlack)

	m.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(box, 0, 8, true).AddItem(nil, 0, 1, false), 0, 10, true).
		AddItem(nil, 0, 1, false)
	m.setStatus("idle", "")
}

// setStatus is the line under the options: what to do, what went wrong, or what was found.
func (m *searchModal) setStatus(kind, text string) {
	switch kind {
	case "error":
		m.status.SetText("[red]" + tview.Escape(text))
	case "idle":
		m.status.SetText(fmt.Sprintf("["+searchDim+"]At least %d characters. Regular expressions are supported; a capital letter makes it case-sensitive.", libtrecs.SearchMinTerm))
	default:
		m.status.SetText("[white]" + tview.Escape(text))
	}
}

// openSearch is "/" on the playback page.
func (em *EditorMode) openSearch() {
	if len(em.commands) == 0 {
		em.setStatus("There is nothing to search.")
		return
	}
	m := em.searchModal
	em.cancelAnnotation()
	m.stopped = em.player.GetPlaybackState() == libtrecs.PlaybackPlaying
	if m.stopped {
		em.player.Pause() // to read it, it has to hold still
	}
	frames, _, meta, err := libtrecs.LoadFrames(em.filePath)
	if err != nil {
		em.setStatus(fmt.Sprintf("Cannot search: %v", err))
		return
	}
	m.index = libtrecs.BuildSearchIndexFor(em.commands, frames, meta.Height)
	if em.pages.HasPage(searchPage) {
		em.pages.RemovePage(searchPage)
	}
	em.pages.AddPage(searchPage, m.frame, true, true)
	em.app.SetFocus(m.term)
	if m.term.GetText() != "" { // what was looked for last time is looked for again: the recording may have been edited since
		em.runSearch()
	}
}

func (em *EditorMode) closeSearch() {
	if em.pages.HasPage(searchPage) {
		em.pages.RemovePage(searchPage)
	}
	em.app.SetFocus(em.playbackView)
	if em.searchModal.stopped && em.player.GetPlaybackState() == libtrecs.PlaybackPaused {
		em.setStatus("Paused." + em.currentAnnotationHint())
	}
}

// runSearch searches for what is in the term, with the options, and lists the matches.
func (em *EditorMode) runSearch() {
	m := em.searchModal
	m.results.set(libtrecs.SearchOutcome{})
	re, err := libtrecs.CompileSearchPattern(m.term.GetText(), m.regex.IsChecked(), m.matchCase.IsChecked())
	if err != nil {
		m.setStatus("error", err.Error())
		return
	}
	out := libtrecs.RunSearch(m.index, re, 0)
	m.results.set(out)
	switch {
	case len(out.Results) == 0:
		m.setStatus("done", "No matches.")
		return
	default:
		plural := func(n int, one, many string) string {
			if n == 1 {
				return fmt.Sprintf("%d %s", n, one)
			}
			return fmt.Sprintf("%d %s", n, many)
		}
		text := fmt.Sprintf("%s matching in %s", plural(out.MatchedLines, "line", "lines"), plural(len(out.Results), "command", "commands"))
		if out.Truncated {
			text += fmt.Sprintf(" - showing the first %d lines", libtrecs.SearchMaxMatchLines)
		}
		m.setStatus("done", text)
	}
	em.app.SetFocus(m.results)
}

// seekToSearchTarget puts playback at a match: paused there, with the line on the screen, as the web player does.
func (em *EditorMode) seekToSearchTarget(t searchTarget) {
	if em.pages.HasPage(searchPage) {
		em.pages.RemovePage(searchPage)
	}
	em.app.SetFocus(em.playbackView)
	idx := t.result.Idx
	if idx < 0 || idx >= len(em.commands) {
		return
	}
	em.cancelAnnotation()
	em.player.Pause() // first: a frame in flight is not fed to the screen that is rebuilt
	if total := em.player.GetTotalFrames(); t.row.FI >= 0 && total > 0 {
		// The frame after the line's: playback, when it is resumed, goes on from there, and does not draw the line again.
		em.player.SeekToFrame(min(t.row.FI+1, total-1))
	} else {
		em.player.SeekTo(t.row.TS + 1)
	}

	em.clearDisplayForJump()
	seq := libtrecs.CommandFrames(&em.commands[idx])
	for _, f := range seq[:min(t.row.Upto, len(seq))] {
		em.display.Feed(f.Data)
	}
	em.playbackView.SetText(em.renderDisplay())
	em.playbackView.ScrollToEnd()

	em.currentCmdIdx = idx
	em.stateMu.Lock()
	em.lastSeenCmdIdx = idx // it is in the command: it did not begin here, and has no intro or note to show again
	em.stateMu.Unlock()
	em.progressBar.SetProgress(t.row.TS, em.totalDurationMs)
	em.resetTimed(t.row.TS)
	em.setStatus(fmt.Sprintf("Paused at %s, in command %d of %d. Space resumes.", libtrecs.ClockText(t.row.TS), idx+1, len(em.commands)) + em.currentAnnotationHint())
}

// handleSearchInput is handleInput's for the search page.
func (em *EditorMode) handleSearchInput(event *tcell.EventKey) *tcell.EventKey {
	m := em.searchModal
	order := []tview.Primitive{m.term, m.matchCase, m.regex, m.results}
	focus := func(by int) {
		cur := 0
		for i, p := range order {
			if p == em.app.GetFocus() {
				cur = i
			}
		}
		for n := 1; n <= len(order); n++ {
			next := order[(cur+by*n+len(order)*n)%len(order)]
			if next == m.results && len(m.results.targets) == 0 {
				continue // nothing to go to there
			}
			em.app.SetFocus(next)
			return
		}
	}
	switch event.Key() {
	case tcell.KeyEscape:
		em.closeSearch()
		return nil
	case tcell.KeyTab:
		focus(1)
		return nil
	case tcell.KeyBacktab:
		focus(-1)
		return nil
	case tcell.KeyEnter:
		if em.app.GetFocus() == m.term {
			em.runSearch()
			return nil
		}
	case tcell.KeyRune:
		if event.Modifiers()&tcell.ModAlt != 0 {
			switch event.Rune() {
			case 'i', 'I':
				m.matchCase.SetChecked(!m.matchCase.IsChecked())
				return nil
			case 'r', 'R':
				m.regex.SetChecked(!m.regex.IsChecked())
				return nil
			}
		}
	}
	return event
}
