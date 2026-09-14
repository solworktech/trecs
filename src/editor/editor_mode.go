package main

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	libtrecs "trecs/lib"
)

type EditorMode struct {
	player     libtrecs.TerminalPlayer
	commands   []libtrecs.Command
	frames     []libtrecs.TerminalFrame
	filePath   string
	app        *tview.Application
	pages      *tview.Pages
	currentCmd *libtrecs.Command
}

// NewEditorMode creates a new editor mode
func NewEditorMode(player libtrecs.TerminalPlayer, commands []libtrecs.Command, filePath string) *EditorMode {
	return &EditorMode{
		player:   player,
		commands: commands,
		filePath: filePath,
		app:      tview.NewApplication(),
	}
}

// Run starts the interactive playback editor
func (em *EditorMode) Run() error {
	em.frames = make([]libtrecs.TerminalFrame, em.player.GetTotalFrames())

	em.pages = tview.NewPages()

	playbackView := em.createPlaybackView()
	em.pages.AddPage("playback", playbackView, true, true)

	editView := em.createEditView()
	em.pages.AddPage("edit", editView, true, false)

	em.app.SetRoot(em.pages, true)
	em.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return em.handleInput(event)
	})

	go em.monitorPlayback()

	return em.app.Run()
}

func (em *EditorMode) createPlaybackView() tview.Primitive {
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(tview.NewTextView().SetText("Playing... Press SPACE to pause, Q to quit\n\n"), 1, 0, false).
		AddItem(tview.NewTextView().SetText(""), 0, 1, false)

	return flex
}

func (em *EditorMode) createEditView() tview.Primitive {
	form := tview.NewForm().
		AddTextArea("Input:", "", 40, 5, 1000, func(text string) {}).
		AddTextArea("Output:", "", 40, 10, 1000, func(text string) {}).
		AddButton("Resume", func() {
			em.pages.SwitchToPage("playback")
			em.player.Resume()
		}).
		AddButton("Delete Command", func() {
			// TODO: Mark command for deletion
		}).
		AddButton("Save & Exit", func() {
			em.saveAndExit()
		}).
		SetTitle("Edit Command").
		SetBorder(true)

	return form
}

func (em *EditorMode) handleInput(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyCtrlC {
		em.app.Stop()
		return nil
	}
	if event.Key() == tcell.KeyRune {
		switch event.Rune() {
		case ' ':
			if em.player.GetPlaybackState() == libtrecs.PlaybackPlaying {
				em.player.Pause()
				em.currentCmd = libtrecs.GetCommandAtFrameIndex(em.player.GetCurrentFrameIndex(), em.commands)
				em.pages.SwitchToPage("edit")
				return nil
			}
		case 'q', 'Q':
			em.app.Stop()
			return nil
		}
	}
	return event
}

func (em *EditorMode) monitorPlayback() {
	// Monitor playback state
	for {
		if em.player.GetPlaybackState() == libtrecs.PlaybackStopped {
			em.app.Stop()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (em *EditorMode) saveAndExit() {
	// TODO: Implement save logic
	fmt.Println("TODO: Save edited recording")
	em.app.Stop()
}
