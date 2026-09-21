package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	libtrecs "trecs/lib"
)

// EditorMode wraps playback with interactive editing
type EditorMode struct {
	player          libtrecs.TerminalPlayer
	commands        []libtrecs.Command
	frames          []libtrecs.TerminalFrame
	filePath        string
	app             *tview.Application
	pages           *tview.Pages
	currentCmd      *libtrecs.Command
	currentCmdIdx   int
	playbackView    *tview.TextView
	display         *libtrecs.DisplayBuffer
	statusView      *tview.TextView
	inputField      *tview.TextArea
	outputField     *tview.TextArea
	deletedCommands map[int]bool
	editedCommands  map[int]bool
}

// NewEditorMode creates a new editor mode
func NewEditorMode(player libtrecs.TerminalPlayer, commands []libtrecs.Command, filePath string) *EditorMode {
	return &EditorMode{
		player:          player,
		commands:        commands,
		filePath:        filePath,
		app:             tview.NewApplication(),
		deletedCommands: make(map[int]bool),
		editedCommands:  make(map[int]bool),
	}
}

// Run starts the interactive playback editor
func (em *EditorMode) Run() error {
	em.pages = tview.NewPages()

	playbackView := em.createPlaybackView()
	em.pages.AddPage("playback", playbackView, true, true)

	editView := em.createEditView()
	em.pages.AddPage("edit", editView, true, false)

	em.statusView = tview.NewTextView().SetDynamicColors(true)
	em.setStatus("Ready.")

	root := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(em.pages, 0, 1, true).
		AddItem(em.statusView, 1, 0, false)

	em.app.SetRoot(root, true)
	em.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return em.handleInput(event)
	})

	// Handle signals for cleanup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		<-sigChan
		em.player.Stop()
		em.app.Stop()
	}()

	go em.monitorPlayback()

	em.display = libtrecs.NewDisplayBuffer()

	// Feed frames through DisplayBuffer, which actually executes backspaces
	// (rather than discarding them like the old FilterForDisplay approach
	// did), and redraw the pane's full content each time - a TextView can't
	// selectively un-write a character once it's been appended, so this is
	// the only reliable way to make corrections during typing visually
	// disappear during editor playback the way they do in a real terminal.
	em.player.SetFrameCallback(func(frame libtrecs.TerminalFrame) {
		em.display.Feed(frame.Data)
		rendered := em.display.Render()
		em.app.QueueUpdateDraw(func() {
			em.playbackView.SetText(rendered)
			em.playbackView.ScrollToEnd()
		})
	})

	// Start playback without raw mode / stdout writes - tview owns the terminal
	if err := em.player.PlayWithoutRawMode(em.filePath); err != nil {
		return err
	}

	em.frames = make([]libtrecs.TerminalFrame, em.player.GetTotalFrames())

	defer func() {
		em.player.Stop()
		time.Sleep(200 * time.Millisecond)
		_ = em.player.RestoreTerminal()
		_ = os.Stdout.Sync()
		_ = os.Stderr.Sync()
	}()

	return em.app.Run()
}

func (em *EditorMode) createPlaybackView() tview.Primitive {
	info := tview.NewTextView().SetText("Playing... Press SPACE/E to pause, Q to quit")

	separator := tview.NewTextView().SetText("────────────────────────────────────────────────────────────────")

	em.playbackView = tview.NewTextView().SetText("\n")
	em.playbackView.SetDynamicColors(true)
	em.playbackView.SetScrollable(true)

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(info, 1, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(em.playbackView, 0, 1, false)

	return flex
}

func (em *EditorMode) createEditView() tview.Primitive {
	em.inputField = tview.NewTextArea()
	em.inputField.SetWrap(true)

	em.outputField = tview.NewTextArea()
	em.outputField.SetWrap(true)

	legend := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[yellow]Ctrl+R[white] Save Edit & Resume    [yellow]Ctrl+X[white] Discard & Resume    " +
			"[yellow]Ctrl+D[white] Delete Command    [yellow]Ctrl+S[white] Save Recording    " +
			"[yellow]Ctrl+P[white]/[yellow]Ctrl+N[white] Prev/Next Command    [yellow]Tab[white] Switch Field")

	inputLabel := tview.NewTextView().SetText("Input:")
	outputLabel := tview.NewTextView().SetText("Output:")

	resumeButton := tview.NewButton("Save Edit & Resume [Ctrl+R]")
	resumeButton.SetSelectedFunc(func() {
		em.saveEditAndResume()
	})

	cancelButton := tview.NewButton("Discard & Resume [Ctrl+X]")
	cancelButton.SetSelectedFunc(func() {
		em.discardAndResume()
	})

	deleteButton := tview.NewButton("Delete [Ctrl+D]")
	deleteButton.SetSelectedFunc(func() {
		em.deleteCurrentCommand()
	})

	saveButton := tview.NewButton("Save Recording [Ctrl+S]")
	saveButton.SetSelectedFunc(func() {
		em.saveRecording()
	})

	prevButton := tview.NewButton("< Prev [Ctrl+P]")
	prevButton.SetSelectedFunc(func() {
		em.jumpToCommand(em.currentCmdIdx - 1)
	})

	nextButton := tview.NewButton("Next > [Ctrl+N]")
	nextButton.SetSelectedFunc(func() {
		em.jumpToCommand(em.currentCmdIdx + 1)
	})

	buttonFlex := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(prevButton, 0, 1, false).
		AddItem(resumeButton, 0, 1, false).
		AddItem(cancelButton, 0, 1, false).
		AddItem(deleteButton, 0, 1, false).
		AddItem(saveButton, 0, 1, false).
		AddItem(nextButton, 0, 1, false)

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(legend, 1, 0, false).
		AddItem(inputLabel, 1, 0, false).
		AddItem(em.inputField, 5, 0, true).
		AddItem(outputLabel, 1, 0, false).
		AddItem(em.outputField, 8, 0, false).
		AddItem(buttonFlex, 1, 0, false)

	return flex
}

func (em *EditorMode) handleInput(event *tcell.EventKey) *tcell.EventKey {
	// Hard-quit works everywhere
	if event.Key() == tcell.KeyCtrlC {
		em.player.Stop()
		em.app.Stop()
		return nil
	}

	pageName, _ := em.pages.GetFrontPage()

	if pageName == "edit" {
		switch event.Key() {
		case tcell.KeyCtrlR:
			em.saveEditAndResume()
			return nil
		case tcell.KeyCtrlX:
			em.discardAndResume()
			return nil
		case tcell.KeyCtrlD:
			em.deleteCurrentCommand()
			return nil
		case tcell.KeyCtrlS:
			em.saveRecording()
			return nil
		case tcell.KeyCtrlP:
			em.jumpToCommand(em.currentCmdIdx - 1)
			return nil
		case tcell.KeyCtrlN:
			em.jumpToCommand(em.currentCmdIdx + 1)
			return nil
		case tcell.KeyTab:
			currentFocus := em.app.GetFocus()
			switch currentFocus {
			case em.inputField:
				em.app.SetFocus(em.outputField)
				return nil
			case em.outputField:
				em.app.SetFocus(em.inputField)
				return nil
			}
		}
		return event
	}

	// Playback mode controls
	if event.Key() == tcell.KeyRune {
		switch event.Rune() {
		case ' ', 'e', 'E':
			if em.player.GetPlaybackState() == libtrecs.PlaybackPlaying {
				em.player.Pause()
				em.currentCmdIdx = libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
				if em.currentCmdIdx >= 0 && em.currentCmdIdx < len(em.commands) {
					em.currentCmd = &em.commands[em.currentCmdIdx]
					em.inputField.SetText(em.currentCmd.InputText, false)
					em.outputField.SetText(em.currentCmd.OutputText, false)
				}

				em.setStatus(fmt.Sprintf("Editing command %d of %d.", em.currentCmdIdx+1, len(em.commands)))
				em.pages.SwitchToPage("edit")
				em.app.SetFocus(em.inputField)
				return nil
			}
		case 'q', 'Q':
			em.player.Stop()
			em.app.Stop()
			return nil
		}
	}

	return event
}

// setStatus updates the persistent status line. Safe to call directly from
// handlers running on the UI goroutine (button callbacks, handleInput); do
// not call this from the frame-callback goroutine - use QueueUpdateDraw there.
func (em *EditorMode) setStatus(msg string) {
	em.statusView.SetText(msg)
}

// jumpToCommand moves the edit view directly to another command without
// resuming and re-pausing playback. It also seeks the underlying player to
// that command's position, so if the user resumes afterwards, playback
// continues from there rather than from wherever it happened to be paused.
func (em *EditorMode) jumpToCommand(idx int) {
	if idx < 0 || idx >= len(em.commands) {
		return
	}

	em.currentCmdIdx = idx
	em.currentCmd = &em.commands[idx]
	em.inputField.SetText(em.currentCmd.InputText, false)
	em.outputField.SetText(em.currentCmd.OutputText, false)

	seekTarget := em.currentCmd.StartTime
	if em.currentCmd.HasPrompt {
		seekTarget = em.currentCmd.PromptFrame.Timestamp
	}
	em.player.SeekTo(seekTarget)

	em.setStatus(fmt.Sprintf("Editing command %d of %d.", idx+1, len(em.commands)))
}

func (em *EditorMode) saveEditAndResume() {
	em.updateCurrentCommand()
	em.setStatus(fmt.Sprintf("Command %d updated. Resuming playback.", em.currentCmdIdx+1))
	em.pages.SwitchToPage("playback")
	em.player.Resume()
}

func (em *EditorMode) discardAndResume() {
	em.setStatus("Edit discarded. Resuming playback.")
	em.pages.SwitchToPage("playback")
	em.player.Resume()
}

func (em *EditorMode) updateCurrentCommand() {
	if em.currentCmdIdx >= 0 && em.currentCmdIdx < len(em.commands) {
		newInput := em.inputField.GetText()
		newOutput := em.outputField.GetText()

		if newInput != em.commands[em.currentCmdIdx].InputText || newOutput != em.commands[em.currentCmdIdx].OutputText {
			em.commands[em.currentCmdIdx].InputText = newInput
			em.commands[em.currentCmdIdx].OutputText = newOutput
			em.editedCommands[em.currentCmdIdx] = true
		}
	}
}

func (em *EditorMode) deleteCurrentCommand() {
	if em.currentCmdIdx >= 0 && em.currentCmdIdx < len(em.commands) {
		em.deletedCommands[em.currentCmdIdx] = true
		em.setStatus(fmt.Sprintf("Command %d marked for deletion. Resuming playback.", em.currentCmdIdx+1))
		em.pages.SwitchToPage("playback")
		em.player.Resume()
	}
}

func (em *EditorMode) monitorPlayback() {
	for {
		if em.player.GetPlaybackState() == libtrecs.PlaybackStopped {
			em.app.Stop()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// saveRecording writes the current state of the recording to disk. It does
// not exit the editor: playback resumes immediately afterwards, same as
// discarding or deleting, so saving mid-session no longer interrupts review.
func (em *EditorMode) saveRecording() {
	em.updateCurrentCommand()

	// Filter out deleted commands
	var finalCommands []libtrecs.Command
	for i, cmd := range em.commands {
		if !em.deletedCommands[i] {
			finalCommands = append(finalCommands, cmd)
		}
	}

	if len(finalCommands) == 0 {
		em.setStatus("Cannot save: all commands would be deleted.")
		return
	}

	backupPath := em.filePath + ".backup"

	if err := libtrecs.RebuildRecording(em.filePath, backupPath, finalCommands); err != nil {
		em.setStatus(fmt.Sprintf("Failed to save: %v", err))
		return
	}

	em.setStatus(fmt.Sprintf("Recording saved. Backup: %s", backupPath))
	em.pages.SwitchToPage("playback")
	em.player.Resume()
}
