package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// Ctrl+Y: cut the recording's long pauses down to a length. The same as `trecs-edit -idle-trim 3s <file>`, from the playback page. It
// writes the recording at once (the recording as it was is kept as <file>.backup), and its sound with it.

const (
	idleTrimPage = "idletrim"
	idleTrimKey  = tcell.KeyCtrlY
)

const idleTrimExplanation = `Long pauses - time at the prompt, a command that took a while - are cut down to the length below, and everything after each moves earlier by what was cut. How the recording is paced inside a command is not changed, and nor is an image on show.

The recording is written now, with its sound if it has any (the recording as it was is kept as a .backup). Changes staged but not written must be written first.`

type idleTrimModal struct {
	frame      tview.Primitive
	box        *tview.Flex
	text       *tview.TextView
	limit      *tview.InputField
	message    *tview.TextView
	apply      *tview.Button
	cancel     *tview.Button
	order      []tview.Primitive
	wasPlaying bool
}

func (em *EditorMode) createIdleTrimModal() {
	m := &idleTrimModal{}
	em.idleTrim = m

	m.text = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true).SetText(idleTrimExplanation)
	m.text.SetBackgroundColor(timedModalBackground)

	m.limit = tview.NewInputField()
	m.limit.SetFieldBackgroundColor(tcell.ColorBlack).SetFieldTextColor(tcell.ColorWhite)
	m.limit.SetBorder(true).SetTitle(" Longest pause to keep (as 2s or 500ms; a number is seconds) ")
	m.limit.SetAcceptanceFunc(func(text string, _ rune) bool { return len(text) <= 12 })
	m.limit.SetChangedFunc(func(string) { m.message.SetText("") })

	m.message = tview.NewTextView().SetDynamicColors(true)
	m.message.SetBackgroundColor(timedModalBackground)

	button := func(label string, do func()) *tview.Button {
		b := tview.NewButton(label)
		b.SetStyle(tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite))
		b.SetActivatedStyle(tcell.StyleDefault.Background(timedModalMark).Foreground(tcell.ColorBlack))
		b.SetSelectedFunc(do)
		return b
	}
	m.apply = button("Trim  Enter", em.applyIdleTrim)
	m.cancel = button("Cancel  Esc", em.closeIdleTrim)
	buttons := opaque(tview.NewFlex().SetDirection(tview.FlexColumn), false)
	buttons.SetBackgroundColor(timedModalBackground)
	buttons.AddItem(nil, 0, 1, false).AddItem(m.apply, 13, 0, false).AddItem(nil, 1, 0, false).AddItem(m.cancel, 14, 0, false).AddItem(nil, 0, 1, false)

	m.box = opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).
		AddItem(m.text, 7, 0, false).
		AddItem(m.limit, 3, 0, true).
		AddItem(m.message, 2, 0, false).
		AddItem(buttons, 1, 0, false)
	m.box.SetBorder(true).SetTitle(" Trim long pauses ").SetBorderPadding(0, 0, 1, 1)
	m.box.SetBackgroundColor(timedModalBackground)

	m.order = []tview.Primitive{m.limit, m.apply, m.cancel}
	m.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(m.box, 76, 0, true).AddItem(nil, 0, 1, false), 17, 0, true).
		AddItem(nil, 0, 1, false)
}

// parseIdleLimit reads what is typed in the box: a duration (2s, 500ms, 1m30s), or a number, which is seconds.
func parseIdleLimit(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("give the longest pause to keep, as 2s or 500ms")
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(f * float64(time.Second)), libtrecs.CheckIdleTrim(time.Duration(f * float64(time.Second)))
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a length of time: write it as 2s or 500ms", s)
	}
	return d, libtrecs.CheckIdleTrim(d)
}

// openIdleTrim is what Ctrl+Y does.
func (em *EditorMode) openIdleTrim() {
	switch {
	case em.audioBusy.Load():
		em.setStatus("The sound of the last change is still being made: wait for it.")
		return
	case len(em.deletedCommands) > 0 || len(em.editedCommands) > 0:
		em.setStatus("There are changes that are not written: write them (^W in the editor) or leave the editor (^X) first.")
		return
	}
	m := em.idleTrim
	m.wasPlaying = em.player.GetPlaybackState() == libtrecs.PlaybackPlaying
	if m.wasPlaying {
		em.player.Pause()
	}
	if em.pages.HasPage(idleTrimPage) {
		em.pages.RemovePage(idleTrimPage)
	}
	m.limit.SetText(libtrecs.DefaultIdleTrim.String())
	m.message.SetText("")
	em.pages.AddPage(idleTrimPage, m.frame, true, true)
	em.app.SetFocus(m.limit)
}

func (em *EditorMode) closeIdleTrim() {
	if em.pages.HasPage(idleTrimPage) {
		em.pages.RemovePage(idleTrimPage)
	}
	em.app.SetFocus(em.playbackView)
	if em.idleTrim.wasPlaying {
		em.idleTrim.wasPlaying = false
		em.player.Resume()
	}
}

// handleIdleTrimInput is handleInput's for the modal's page.
func (em *EditorMode) handleIdleTrimInput(event *tcell.EventKey) *tcell.EventKey {
	m := em.idleTrim
	switch event.Key() {
	case idleTrimKey, tcell.KeyEscape:
		em.closeIdleTrim()
		return nil
	case tcell.KeyEnter:
		if f := em.app.GetFocus(); f == tview.Primitive(m.limit) || f == tview.Primitive(m.apply) {
			em.applyIdleTrim()
			return nil
		}
	case tcell.KeyTab, tcell.KeyBacktab:
		at := 0
		for i, p := range m.order {
			if p == em.app.GetFocus() {
				at = i
			}
		}
		step := 1
		if event.Key() == tcell.KeyBacktab {
			step = len(m.order) - 1
		}
		em.app.SetFocus(m.order[(at+step)%len(m.order)])
		return nil
	}
	return event
}

// applyIdleTrim cuts the pauses and writes the recording, in the background: the sound can take a while. When it is done the recording
// is read again, and playback is where it was (the same command, which is where it is in the shorter recording).
func (em *EditorMode) applyIdleTrim() {
	m := em.idleTrim
	limit, err := parseIdleLimit(m.limit.GetText())
	if err != nil {
		m.message.SetText("[red]" + tview.Escape(err.Error()))
		em.app.SetFocus(m.limit)
		return
	}
	path, backup, sound, at := em.filePath, em.filePath+".backup", em.audioFile(), em.currentCmdIdx
	wasPlaying := m.wasPlaying
	m.wasPlaying = false // it is decided below whether playback goes on
	if em.pages.HasPage(idleTrimPage) {
		em.pages.RemovePage(idleTrimPage)
	}
	em.app.SetFocus(em.playbackView)
	em.audioBusy.Store(true)
	em.setStatus("Cutting the long pauses...")
	go func() {
		res, err := libtrecs.TrimIdle(path, backup, sound, limit)
		em.app.QueueUpdateDraw(func() {
			em.audioBusy.Store(false)
			switch {
			case err != nil:
				em.setStatus("[red]" + tview.Escape(fmt.Sprintf("The pauses were not cut: %v", err)))
				if wasPlaying {
					em.player.Resume()
				}
			case res.Pauses == 0:
				em.setStatus(fmt.Sprintf("No pause is longer than %s: the recording is as it was.", limit))
				if wasPlaying {
					em.player.Resume()
				}
			default:
				em.reloadFromDisk(at, backup)
				em.player.Pause() // playback would write over what is said below with the next frame
				msg := fmt.Sprintf("Cut %s of long pauses: the recording is now %s (it was %s). Backup: %s",
					res.Removed.Round(time.Millisecond), res.After.Round(time.Millisecond), res.Before.Round(time.Millisecond), backup)
				if res.AudioBackup != "" {
					msg += "  The sound was cut to match; backup: " + res.AudioBackup
				}
				msg += "  Paused: Space to play."
				em.setStatus(msg)
			}
		})
	}()
}
