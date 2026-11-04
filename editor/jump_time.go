package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// Ctrl+J: jump to a time in the recording, typed as H:M:S. Left and Right on the playback page move playback back and forward by
// seekStepMs. Neither changes whether playback is going or paused.

const (
	jumpTimePage = "jumptime"
	jumpTimeKey  = tcell.KeyCtrlJ
	seekStepMs   = 10_000
)

type jumpTimeModal struct {
	frame      tview.Primitive
	box        *tview.Flex
	field      *tview.InputField
	message    *tview.TextView
	wasPlaying bool
}

func (em *EditorMode) createJumpTimeModal() {
	m := &jumpTimeModal{}
	em.jumpTime = m

	m.field = tview.NewInputField()
	m.field.SetFieldBackgroundColor(tcell.ColorBlack).SetFieldTextColor(tcell.ColorWhite)
	m.field.SetBorder(true).SetTitle(" Time (H:M:S) ")
	m.field.SetAcceptanceFunc(func(text string, _ rune) bool {
		return len(text) <= 12 && strings.Trim(text, "0123456789:.") == ""
	})
	m.field.SetChangedFunc(func(string) { m.message.SetText("") })

	m.message = tview.NewTextView().SetDynamicColors(true)
	m.message.SetBackgroundColor(timedModalBackground)

	hint := tview.NewTextView().SetDynamicColors(true).SetText("[gray]H:M:S, M:S or seconds. Enter jumps, Esc cancels")
	hint.SetBackgroundColor(timedModalBackground)

	m.box = opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).
		AddItem(m.field, 3, 0, true).
		AddItem(m.message, 2, 0, false).
		AddItem(hint, 1, 0, false)
	m.box.SetBorder(true).SetTitle(" Jump to a time ").SetBorderPadding(0, 0, 1, 1)
	m.box.SetBackgroundColor(timedModalBackground)

	m.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(m.box, 60, 0, true).AddItem(nil, 0, 1, false), 9, 0, true).
		AddItem(nil, 0, 1, false)
}

// parseJumpTime reads H:M:S, M:S or S (the last part may have a fraction) as milliseconds. Minutes and seconds under an hour or a
// minute must be below 60: 1:75 is a mistake, not 2:15.
func parseJumpTime(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("give a time, as H:M:S")
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("%q is not a time: write it as H:M:S", s)
	}
	var ms int64
	for i, p := range parts {
		last := i == len(parts)-1
		var unit int64
		switch len(parts) - 1 - i {
		case 0:
			unit = 1000
		case 1:
			unit = 60_000
		default:
			unit = 3_600_000
		}
		if p == "" {
			return 0, fmt.Errorf("%q is not a time: write it as H:M:S", s)
		}
		var v float64
		if last {
			f, err := strconv.ParseFloat(p, 64)
			if err != nil || strings.Trim(p, "0123456789.") != "" { // not a sign, an exponent, inf or nan
				return 0, fmt.Errorf("%q is not a time: write it as H:M:S", s)
			}
			v = f
		} else {
			n, err := strconv.ParseUint(p, 10, 32)
			if err != nil {
				return 0, fmt.Errorf("%q is not a time: write it as H:M:S", s)
			}
			v = float64(n)
		}
		if i > 0 && v >= 60 {
			return 0, fmt.Errorf("%q is not a time: minutes and seconds go up to 59", s)
		}
		ms += int64(v * float64(unit))
	}
	return ms, nil
}

// openJumpTime is what Ctrl+J does.
func (em *EditorMode) openJumpTime() {
	m := em.jumpTime
	m.wasPlaying = em.player.GetPlaybackState() == libtrecs.PlaybackPlaying
	if m.wasPlaying {
		em.player.Pause()
	}
	if em.pages.HasPage(jumpTimePage) {
		em.pages.RemovePage(jumpTimePage)
	}
	m.field.SetText("")
	m.message.SetText("")
	m.field.SetTitle(fmt.Sprintf(" Time, up to %s (H:M:S) ", clockHMS(em.totalDurationMs)))
	em.pages.AddPage(jumpTimePage, m.frame, true, true)
	em.app.SetFocus(m.field)
}

func (em *EditorMode) closeJumpTime() {
	if em.pages.HasPage(jumpTimePage) {
		em.pages.RemovePage(jumpTimePage)
	}
	em.app.SetFocus(em.playbackView)
	if em.jumpTime.wasPlaying {
		em.jumpTime.wasPlaying = false
		em.player.Resume()
	}
}

// handleJumpTimeInput is handleInput's for the modal's page.
func (em *EditorMode) handleJumpTimeInput(event *tcell.EventKey) *tcell.EventKey {
	m := em.jumpTime
	switch event.Key() {
	case jumpTimeKey, tcell.KeyEscape:
		em.closeJumpTime()
		return nil
	case tcell.KeyEnter:
		ms, err := parseJumpTime(m.field.GetText())
		if err == nil && ms > em.totalDurationMs {
			err = fmt.Errorf("the recording is %s long", clockHMS(em.totalDurationMs))
		}
		if err != nil {
			m.message.SetText("[red]" + tview.Escape(err.Error()))
			return nil
		}
		was := m.wasPlaying
		m.wasPlaying = false
		if em.pages.HasPage(jumpTimePage) {
			em.pages.RemovePage(jumpTimePage)
		}
		em.app.SetFocus(em.playbackView)
		em.seekToTime(ms, was)
		return nil
	}
	return event
}

// clockHMS is a time as H:MM:SS (M:SS under an hour).
func clockHMS(ms int64) string {
	sec := max(0, ms) / 1000
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// seekBy moves playback by delta milliseconds, within the recording.
func (em *EditorMode) seekBy(delta int64) {
	if em.annotationModalVisible || em.cueActive {
		em.setStatus("Dismiss what is showing first." + em.currentAnnotationHint())
		return
	}
	em.seekToTime(em.currentTimeMs()+delta, em.player.GetPlaybackState() == libtrecs.PlaybackPlaying)
}

// currentTimeMs is where playback is. A seek made while playback is paused is not applied by the player until it is resumed, so
// until then its position is still the old one: the time that was sought is what it is, for as long as the player has not moved
// from where it was when the seek was made.
func (em *EditorMode) currentTimeMs() int64 {
	pos := em.player.Position()
	if em.pausedSeek.set && em.player.GetPlaybackState() == libtrecs.PlaybackPaused && pos == em.pausedSeek.base {
		return em.pausedSeek.ms
	}
	return pos
}

// pausedSeek is a seek that was made while paused (see currentTimeMs).
type pausedSeek struct {
	set      bool
	ms, base int64
}

// clampSeek keeps a time within the recording.
func clampSeek(ms, total int64) int64 {
	return max(0, min(ms, max(total, 0)))
}

// seekToTime puts playback at a time, with the screen as it is at that time (made of the frames of the command it is in, as a search
// result is), and playing on from there if it was playing.
func (em *EditorMode) seekToTime(ms int64, resume bool) {
	ms = clampSeek(ms, em.totalDurationMs)
	em.cancelAnnotation()
	em.player.Pause() // first: a frame in flight is not fed to the screen that is rebuilt

	// The command it falls in: the last that began by then.
	idx := -1
	for i := range em.commands {
		c := &em.commands[i]
		start := c.StartTime
		if c.HasPrompt {
			start = c.PromptFrame.Timestamp
		}
		if start > ms {
			break
		}
		idx = i
	}

	// The frames with this time or before are on the screen; playback goes on with those after it.
	em.player.SeekTo(ms + 1)
	em.clearDisplayForJump()
	if idx >= 0 {
		for _, f := range libtrecs.CommandFrames(&em.commands[idx]) {
			if f.Timestamp > ms {
				break
			}
			em.display.Feed(f.Data)
		}
		em.currentCmdIdx = idx
	}
	em.playbackView.SetText(em.renderDisplay())
	em.playbackView.ScrollToEnd()

	em.stateMu.Lock()
	em.lastSeenCmdIdx = idx // inside the command: it did not begin here, and has no intro or note to show again
	em.stateMu.Unlock()
	em.progressBar.SetProgress(ms, em.totalDurationMs)
	em.resetTimed(ms)
	em.pausedSeek = pausedSeek{}
	if resume {
		em.player.Resume()
	} else {
		em.pausedSeek = pausedSeek{set: true, ms: ms, base: em.player.Position()}
	}
	state := "Paused at"
	if resume {
		state = "Playing from"
	}
	em.setStatus(fmt.Sprintf("%s %s of %s.", state, clockHMS(ms), clockHMS(em.totalDurationMs)) + em.currentAnnotationHint())
}
