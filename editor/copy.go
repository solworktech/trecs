package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// Copying text with the mouse, when playback is not going on.
//
// While a recording plays the editor does not take the mouse: the terminal's own selection works, and it is the terminal's. While
// playback is paused (or has ended, or a box is open, which pauses it) the editor takes the mouse instead, and a drag over the playback
// screen, the results of a search or the help selects what is under it, and letting go copies it. The terminal's own selection is
// still there with Shift held. What is selected is read off the screen as it is drawn, so it is what the person sees: a line per row,
// with the spaces at the end of a row left out.
//
// The copy goes to the system clipboard by the program the machine has for it (wl-copy, xclip, xsel, pbcopy, clip.exe) and, whether or
// not that worked, to the terminal as an OSC 52 sequence - which is what reaches the clipboard of the machine the person is sitting at
// over ssh. Terminals that take no OSC 52 (the VTE ones, GNOME Terminal) need one of the programs.

// selection is a drag: where it began and where it is, within one region of the screen.
type selection struct {
	on       bool // there is something to show
	dragging bool // the button is still down
	region   image.Rectangle
	ax, ay   int // where the drag began
	bx, by   int // and where it is
}

// ordered gives the two ends in reading order.
func (s selection) ordered() (x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = s.ax, s.ay, s.bx, s.by
	if y1 < y0 || y1 == y0 && x1 < x0 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	return
}

func rectOf(p interface{ GetInnerRect() (int, int, int, int) }) image.Rectangle {
	x, y, w, h := p.GetInnerRect()
	return image.Rect(x, y, x+w, y+h)
}

// copyRegions are the places on the front page that can be selected from.
func (em *EditorMode) copyRegions() []image.Rectangle {
	page, _ := em.pages.GetFrontPage()
	var rs []image.Rectangle
	switch page {
	case "playback":
		rs = append(rs, rectOf(em.playbackView))
	case searchPage:
		rs = append(rs, rectOf(em.searchModal.results), rectOf(em.searchModal.status))
	case helpPage:
		if em.help != nil {
			rs = append(rs, rectOf(em.help.text))
		}
	}
	return rs
}

// syncMouse takes the mouse when playback is not going on, and gives it back when it is.
func (em *EditorMode) syncMouse(state libtrecs.PlaybackState) {
	want := state != libtrecs.PlaybackPlaying
	if want == em.mouseOn {
		return
	}
	em.mouseOn = want
	em.app.EnableMouse(want)
	if !want {
		em.sel = selection{}
	}
}

// handleMouse is the application's mouse capture: it keeps the drags that begin in a region, and leaves every other event to the widgets.
func (em *EditorMode) handleMouse(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
	x, y := event.Position()
	switch action {
	case tview.MouseLeftDown:
		em.sel = selection{}
		for _, r := range em.copyRegions() {
			if image.Pt(x, y).In(r) {
				em.sel = selection{on: true, dragging: true, region: r, ax: x, ay: y, bx: x, by: y}
				return nil, action
			}
		}
	case tview.MouseMove:
		if em.sel.dragging {
			em.sel.bx = max(em.sel.region.Min.X, min(em.sel.region.Max.X-1, x))
			em.sel.by = max(em.sel.region.Min.Y, min(em.sel.region.Max.Y-1, y))
			return nil, action
		}
	case tview.MouseLeftUp:
		if em.sel.dragging {
			em.sel.dragging = false
			em.sel.bx = max(em.sel.region.Min.X, min(em.sel.region.Max.X-1, x))
			em.sel.by = max(em.sel.region.Min.Y, min(em.sel.region.Max.Y-1, y))
			if em.sel.ax == em.sel.bx && em.sel.ay == em.sel.by { // a click, not a drag
				em.sel = selection{}
			} else if text := em.selectedText(); text != "" {
				em.setStatus(em.copyText(text))
			} else {
				em.sel = selection{} // nothing but spaces
			}
			return nil, action
		}
	}
	return event, action
}

// cellText is what is in a cell, and how many cells it takes.
func cellText(screen tcell.Screen, x, y int) (string, int) {
	r, comb, _, width := screen.GetContent(x, y)
	if r == 0 {
		r = ' '
	}
	return string(append([]rune{r}, comb...)), max(1, width)
}

// selectedText reads the selection off the screen.
func (em *EditorMode) selectedText() string {
	if !em.sel.on || em.screen == nil {
		return ""
	}
	x0, y0, x1, y1 := em.sel.ordered()
	r := em.sel.region
	var lines []string
	for y := y0; y <= y1; y++ {
		from, to := r.Min.X, r.Max.X-1
		if y == y0 {
			from = max(from, x0)
		}
		if y == y1 {
			to = min(to, x1)
		}
		var sb strings.Builder
		for x := from; x <= to; {
			s, w := cellText(em.screen, x, y)
			sb.WriteString(s)
			x += w
		}
		lines = append(lines, strings.TrimRight(sb.String(), "  "))
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// paintSelection shows the selection, in reverse video; it is tview's after-draw handler's, after the pictures.
func (em *EditorMode) paintSelection(screen tcell.Screen) {
	if !em.sel.on {
		return
	}
	x0, y0, x1, y1 := em.sel.ordered()
	r := em.sel.region
	for y := y0; y <= y1; y++ {
		from, to := r.Min.X, r.Max.X-1
		if y == y0 {
			from = max(from, x0)
		}
		if y == y1 {
			to = min(to, x1)
		}
		for x := from; x <= to; x++ {
			ch, comb, style, _ := screen.GetContent(x, y)
			screen.SetContent(x, y, ch, comb, style.Reverse(true))
		}
	}
}

// afterDraw is tview's after-draw handler.
func (em *EditorMode) afterDraw(screen tcell.Screen) {
	em.screen = screen
	em.paintPictures(screen)
	em.paintSelection(screen)
}

// ---- the clipboard -----------------------------------------------------------------------------------------------------------------

// clipboardTools are the programs that put text on the system clipboard, in the order they are tried.
var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard", "-in"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
	{"clip.exe"},
}

// copyViaTool puts text on the clipboard with the first program the machine has that does it. It reports whether one did.
func copyViaTool(text string) bool {
	for _, argv := range clipboardTools {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, path, argv[1:]...)
		cmd.Stdin = strings.NewReader(text)
		// (xclip and wl-copy stay running to serve the clipboard: they must not hold on to this terminal, or be waited for)
		cmd.Stdout, cmd.Stderr = nil, nil
		err = cmd.Start()
		done := make(chan error, 1)
		if err == nil {
			go func() { done <- cmd.Wait() }()
			select {
			case err = <-done:
			case <-time.After(300 * time.Millisecond):
				err = nil // still running, serving the selection: it has it
			}
		}
		cancel()
		if err == nil {
			return true
		}
	}
	return false
}

// maxOSC52 is the most text sent to the terminal as OSC 52: terminals refuse more, or a great deal less.
const maxOSC52 = 70000

// copyViaTerminal sends text to the terminal's clipboard as OSC 52.
func (em *EditorMode) copyViaTerminal(text string) bool {
	if em.screen == nil || len(text) > maxOSC52 {
		return false
	}
	tty, ok := em.screen.Tty()
	if !ok {
		return false
	}
	_, err := tty.Write([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"))
	return err == nil
}

// copyText puts text on the clipboard and says, for the status line, how it went.
func (em *EditorMode) copyText(text string) string {
	if em.copyFn != nil { // (tests)
		return em.copyFn(text)
	}
	n := utf8.RuneCountInString(text)
	viaTool := copyViaTool(text)
	viaTerm := em.copyViaTerminal(text)
	switch {
	case viaTool:
		return fmt.Sprintf("Copied %d characters.", n)
	case viaTerm:
		return fmt.Sprintf("Copied %d characters to the terminal's clipboard (OSC 52). If it did not arrive, the terminal does not take that: install wl-clipboard, xclip or xsel.", n)
	}
	return fmt.Sprintf("Could not copy %d characters: no clipboard program was found (wl-copy, xclip, xsel, pbcopy), and the terminal could not be written to.", n)
}

// shareClipboard makes the copy (Ctrl+Q) and cut (Ctrl+X) of a text area go to the system clipboard as well; paste stays its own.
func (em *EditorMode) shareClipboard(ta *tview.TextArea) {
	var held string
	ta.SetClipboard(func(text string) {
		held = text
		em.setStatus(em.copyText(text))
	}, func() string { return held })
}
