package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	libtrecs "trecs/lib"
)

// EditorMode wraps playback with interactive editing
type EditorMode struct {
	player          libtrecs.TerminalPlayer
	commands        []libtrecs.Command
	filePath        string
	app             *tview.Application
	pages           *tview.Pages
	previousPage    string
	currentCmd      *libtrecs.Command
	currentCmdIdx   int
	playbackView    *tview.TextView
	progressBar     *progressBar
	display         *libtrecs.DisplayBuffer
	totalDurationMs int64
	statusView      *tview.TextView
	inputField      *tview.TextArea
	outputField     *tview.TextArea
	// buttonFlex      *tview.Flex
	commandList     *tview.List
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

	commandListView := em.createCommandListView()
	em.pages.AddPage("commandlist", commandListView, true, false)

	em.previousPage = "playback"

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
	// (rather than discarding them), and redraw the pane's full content
	// each time - a TextView can't selectively un-write a character once
	// it's been appended. Also drives the elapsed-time/progress bar, since
	// every frame carries its own timestamp already.
	em.player.SetFrameCallback(func(frame libtrecs.TerminalFrame) {
		em.display.Feed(frame.Data)
		rendered := em.display.Render()
		em.app.QueueUpdateDraw(func() {
			em.playbackView.SetText(rendered)
			em.playbackView.ScrollToEnd()
			em.progressBar.SetProgress(frame.Timestamp, em.totalDurationMs)
		})
	})

	// Start playback without raw mode / stdout writes - tview owns the terminal
	if err := em.player.PlayWithoutRawMode(em.filePath); err != nil {
		return err
	}

	em.totalDurationMs = em.player.GetTotalDurationMs()
	em.progressBar.SetProgress(0, em.totalDurationMs)

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
	legend := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[yellow]Space[white]/[yellow]P[white] Pause/Resume    [yellow]E[white] Edit Current Command    " +
			"[yellow]^P[white]/[yellow]^N[white] Prev/Next Command    " +
			"[yellow]^L[white] Browse Commands    [yellow]Q[white] Quit")

	separator := newHorizontalRule()

	em.playbackView = tview.NewTextView().SetText("\n")
	em.playbackView.SetDynamicColors(true)
	em.playbackView.SetScrollable(true)

	margin := tview.NewBox()

	em.progressBar = newProgressBar()

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(legend, 1, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(em.playbackView, 0, 1, false).
		AddItem(margin, 1, 0, false).
		AddItem(em.progressBar, 1, 0, false)

	return flex
}

func (em *EditorMode) createEditView() tview.Primitive {
	em.inputField = tview.NewTextArea()
	em.inputField.SetWrap(true)
	em.inputField.SetBorder(true).SetTitle(" Input ")

	em.outputField = tview.NewTextArea()
	em.outputField.SetWrap(true)
	em.outputField.SetBorder(true).SetTitle(" Output ")

	legend := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[yellow]^S[white] Stage Change & Continue Editing   [yellow]^W[white] Write & Resume Playback   " +
			"[yellow]^P[white]/[yellow]^N[white] Prev/Next Command   [yellow]^E[white] Open in Editor   " +
			"[yellow]Tab[white] Toggle Fields   [yellow]^D[white] Delete Command   [yellow]^X[white] Exit Editor")

	spacer := tview.NewBox()

	/*saveButton := styleButton(tview.NewButton("Save Recording [Ctrl+S]"))
	saveButton.SetSelectedFunc(func() {
		em.saveRecording()
	})

	cancelButton := styleButton(tview.NewButton("Discard & Resume [Ctrl+X]"))
	cancelButton.SetSelectedFunc(func() {
		em.discardAndResume()
	})

	deleteButton := styleButton(tview.NewButton("Delete [Ctrl+D]"))
	deleteButton.SetSelectedFunc(func() {
		em.deleteCurrentCommand()
	})

	prevButton := styleButton(tview.NewButton("< Prev [Ctrl+P]"))
	prevButton.SetSelectedFunc(func() {
		em.jumpToCommand(em.currentCmdIdx - 1)
	})

	nextButton := styleButton(tview.NewButton("Next > [Ctrl+N]"))
	nextButton.SetSelectedFunc(func() {
		em.jumpToCommand(em.currentCmdIdx + 1)
	})

	em.buttonFlex = tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(saveButton, 0, 1, false).
		AddItem(prevButton, 0, 1, false).
		AddItem(cancelButton, 0, 1, false).
		AddItem(deleteButton, 0, 1, false).
		AddItem(nextButton, 0, 1, false)
	*/
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(legend, 1, 0, false).
		//AddItem(hr, 1, 0, false).
		AddItem(spacer, 1, 0, false).
		AddItem(em.inputField, 7, 0, true).
		AddItem(em.outputField, 10, 0, false)
		//AddItem(em.buttonFlex, 1, 0, false)

	return flex
}

// createCommandListView builds the Ctrl+L command browser: every command in
// the file with its start time relative to the whole recording, letting the
// user jump straight to any of them for playback or for editing.
func (em *EditorMode) createCommandListView() tview.Primitive {
	legend := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[yellow]Up[white]/[yellow]Down[white] Navigate    [yellow]^P[white] Jump to Playback    " +
			"[yellow]^E[white] Jump to Edit    [yellow]Esc[white] Close")

	em.commandList = tview.NewList().ShowSecondaryText(false)
	em.commandList.SetBorder(true).SetTitle(" Commands ")

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(legend, 1, 0, false).
		AddItem(em.commandList, 0, 1, true)

	return flex
}

// refreshCommandList repopulates the command browser from the current
// (possibly edited/deleted) command list.
func (em *EditorMode) refreshCommandList() {
	if em.commandList == nil {
		return
	}
	em.commandList.Clear()
	for i, cmd := range em.commands {
		label := fmt.Sprintf("%s   %s", formatDuration(cmd.StartTime), cmd.InputText)
		if em.deletedCommands[i] {
			label = "(deleted) " + label
		}
		em.commandList.AddItem(label, "", 0, nil)
	}
}

func (em *EditorMode) handleInput(event *tcell.EventKey) *tcell.EventKey {
	// Hard-quit works everywhere
	if event.Key() == tcell.KeyCtrlC {
		em.player.Stop()
		em.app.Stop()
		return nil
	}

	pageName, _ := em.pages.GetFrontPage()

	// Ctrl+L toggles the command browser from anywhere (except itself,
	// where Esc closes it instead).
	if event.Key() == tcell.KeyCtrlL && pageName != "commandlist" {
		em.previousPage = pageName
		em.refreshCommandList()
		em.pages.SwitchToPage("commandlist")
		em.app.SetFocus(em.commandList)
		return nil
	}

	if pageName == "commandlist" {
		switch event.Key() {
		case tcell.KeyEscape:
			em.pages.SwitchToPage(em.previousPage)
			return nil
		case tcell.KeyCtrlP:
			em.jumpToCommandForPlayback(em.commandList.GetCurrentItem())
			return nil
		case tcell.KeyCtrlE:
			em.jumpToCommandForEdit(em.commandList.GetCurrentItem())
			return nil
		}
		return event
	}

	if pageName == "edit" {
		switch event.Key() {
		case tcell.KeyCtrlS:
			em.saveEdit()
			return nil
		case tcell.KeyCtrlX:
			em.discardAndResume()
			return nil
		case tcell.KeyCtrlD:
			em.deleteCurrentCommand()
			return nil
		case tcell.KeyCtrlW:
			em.saveRecording()
			return nil
		case tcell.KeyCtrlP:
			em.jumpToCommand(em.currentCmdIdx - 1)
			return nil
		case tcell.KeyCtrlN:
			em.jumpToCommand(em.currentCmdIdx + 1)
			return nil
		case tcell.KeyCtrlE:
			em.editFocusedFieldExternally()
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
	if event.Key() == tcell.KeyCtrlP {
		cur := libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
		em.jumpToCommandForPlayback(cur - 1)
		return nil
	}
	if event.Key() == tcell.KeyCtrlN {
		cur := libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
		em.jumpToCommandForPlayback(cur + 1)
		return nil
	}

	if event.Key() == tcell.KeyRune {
		switch event.Rune() {
		case ' ', 'p', 'P':
			switch em.player.GetPlaybackState() {
			case libtrecs.PlaybackPlaying:
				em.player.Pause()
				em.setStatus("Paused.")
			case libtrecs.PlaybackPaused:
				em.player.Resume()
				em.setStatus("Resumed.")
			}
			return nil
		case 'e', 'E':
			if em.player.GetPlaybackState() == libtrecs.PlaybackPlaying {
				em.player.Pause()
			}
			em.currentCmdIdx = libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
			if em.currentCmdIdx >= 0 && em.currentCmdIdx < len(em.commands) {
				em.currentCmd = &em.commands[em.currentCmdIdx]
				em.inputField.SetText(em.currentCmd.InputText, false)
				em.outputField.SetText(em.currentCmd.OutputText, false)
			}

			em.setStatus(fmt.Sprintf("Editing command %d of %d.", em.currentCmdIdx+1, len(em.commands)))
			em.outputField.SetSize(30, 0)
			em.pages.SwitchToPage("edit")
			em.app.SetFocus(em.inputField)
			return nil
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
	em.progressBar.SetProgress(seekTarget, em.totalDurationMs)

	em.setStatus(fmt.Sprintf("Editing command %d of %d.", idx+1, len(em.commands)))
}

// jumpToCommandForEdit is used from the Ctrl+L command browser: pauses if
// necessary, jumps straight to editing the chosen command, and switches to
// the edit page.
func (em *EditorMode) jumpToCommandForEdit(idx int) {
	if idx < 0 || idx >= len(em.commands) {
		return
	}
	if em.player.GetPlaybackState() == libtrecs.PlaybackPlaying {
		em.player.Pause()
	}
	em.jumpToCommand(idx)
	em.pages.SwitchToPage("edit")
	em.app.SetFocus(em.inputField)
}

// jumpToCommandForPlayback is used both from the Ctrl+L command browser and
// from Ctrl+P/Ctrl+N during ordinary playback: seeks playback to the chosen
// command and resumes if it was paused, returning to the playback page.
func (em *EditorMode) jumpToCommandForPlayback(idx int) {
	if idx < 0 || idx >= len(em.commands) {
		return
	}
	cmd := em.commands[idx]
	seekTarget := cmd.StartTime
	if cmd.HasPrompt {
		seekTarget = cmd.PromptFrame.Timestamp
	}
	em.player.SeekTo(seekTarget)
	em.currentCmdIdx = idx
	em.progressBar.SetProgress(seekTarget, em.totalDurationMs)
	if em.player.GetPlaybackState() == libtrecs.PlaybackPaused {
		em.player.Resume()
	}
	em.pages.SwitchToPage("playback")
	em.setStatus(fmt.Sprintf("Jumped to command %d of %d.", idx+1, len(em.commands)))
}

func (em *EditorMode) saveEdit() {
	em.updateCurrentCommand()
	em.setStatus(fmt.Sprintf("Command %d updated. CTRL+W to write to disk and resume playback.", em.currentCmdIdx+1))
	//em.pages.SwitchToPage("playback")
	//em.player.Resume()
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
		em.setStatus(fmt.Sprintf("Command %d marked for deletion. CTRL+W to write to disk and resume playback.", em.currentCmdIdx+1))
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

// saveRecording writes the current state of the recording to disk, then
// reloads the file fresh from disk and repositions playback at the command
// that was being edited (mapped through any commands deleted before it).
// Reloading rather than continuing to play the in-memory state means what's
// shown afterwards is guaranteed to match what actually got written -
// including the frames RebuildRecording regenerates for edited text - and
// resets the deleted/edited tracking to a clean slate matching the file.
func (em *EditorMode) saveRecording() {
	em.updateCurrentCommand()

	savedCmdIdx := em.currentCmdIdx
	newIdxForSaved := -1
	var finalCommands []libtrecs.Command
	for i, cmd := range em.commands {
		if em.deletedCommands[i] {
			continue
		}
		if i == savedCmdIdx {
			newIdxForSaved = len(finalCommands)
		}
		finalCommands = append(finalCommands, cmd)
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

	em.reloadFromDisk(newIdxForSaved, backupPath)
}

// reloadFromDisk re-parses the recording file from scratch, replaces the
// in-memory commands and player with fresh ones built from it, and - if
// targetIdx is a valid index into the newly-parsed commands - seeks
// playback to that command's position and resumes there.
func (em *EditorMode) reloadFromDisk(targetIdx int, backupPath string) {
	newCommands, err := libtrecs.ParseRecording(em.filePath)
	if err != nil {
		em.setStatus(fmt.Sprintf("Saved, but failed to reload: %v", err))
		return
	}

	em.player.Stop()

	newPlayer := libtrecs.NewTerminalPlayer()
	em.display = libtrecs.NewDisplayBuffer()
	em.playbackView.Clear()

	newPlayer.SetFrameCallback(func(frame libtrecs.TerminalFrame) {
		em.display.Feed(frame.Data)
		rendered := em.display.Render()
		em.app.QueueUpdateDraw(func() {
			em.playbackView.SetText(rendered)
			em.playbackView.ScrollToEnd()
			em.progressBar.SetProgress(frame.Timestamp, em.totalDurationMs)
		})
	})

	em.commands = newCommands
	em.deletedCommands = make(map[int]bool)
	em.editedCommands = make(map[int]bool)
	em.currentCmdIdx = -1
	em.currentCmd = nil

	if err := newPlayer.PlayWithoutRawMode(em.filePath); err != nil {
		em.setStatus(fmt.Sprintf("Saved, but failed to restart playback: %v", err))
		return
	}
	em.player = newPlayer
	em.totalDurationMs = newPlayer.GetTotalDurationMs()

	if targetIdx >= 0 && targetIdx < len(newCommands) {
		cmd := newCommands[targetIdx]
		seekTarget := cmd.StartTime
		if cmd.HasPrompt {
			seekTarget = cmd.PromptFrame.Timestamp
		}
		newPlayer.SeekTo(seekTarget)
		em.currentCmdIdx = targetIdx
		em.progressBar.SetProgress(seekTarget, em.totalDurationMs)
	}

	em.pages.SwitchToPage("playback")
	em.setStatus(fmt.Sprintf("Recording saved and reloaded. Backup: %s", backupPath))
}

// resolveExternalEditor picks the editor to shell out to: $EDITOR if set,
// else /etc/alternatives/editor if present, else a plain "vi" as a last
// resort so the feature still works on a minimal system.
func resolveExternalEditor() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if info, err := os.Stat("/etc/alternatives/editor"); err == nil && !info.IsDir() {
		return "/etc/alternatives/editor"
	}
	return "vi"
}

// openInExternalEditor writes initial to a temp file, suspends the tview
// application (handing the real terminal back so a full-screen editor like
// vim or nano can take it over), runs the resolved editor on that file, and
// returns its contents once the editor exits. tview.Application.Suspend is
// exactly the mechanism that makes this "embedded" rather than requiring a
// separate terminal.
func (em *EditorMode) openInExternalEditor(initial string) (string, error) {
	tmpFile, err := os.CreateTemp("", "trecs-edit-*.txt")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.WriteString(initial); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	editorCmd := resolveExternalEditor()

	var runErr error
	suspended := em.app.Suspend(func() {
		// Run via sh -c so a $EDITOR containing arguments (e.g. "code
		// --wait") is honoured, not treated as one literal binary name.
		cmd := exec.Command("sh", "-c", editorCmd+" \""+tmpPath+"\"")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		runErr = cmd.Run()
	})
	if !suspended {
		return "", fmt.Errorf("could not suspend the editor UI")
	}
	if runErr != nil {
		return "", fmt.Errorf("%s exited with an error: %w", editorCmd, runErr)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", fmt.Errorf("failed to read back edited file: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// editFocusedFieldExternally opens whichever of Input/Output currently has
// focus in the external editor, then loads the result back into that field.
func (em *EditorMode) editFocusedFieldExternally() {
	field := em.inputField
	label := "Input"
	if em.app.GetFocus() == em.outputField {
		field = em.outputField
		label = "Output"
	}

	edited, err := em.openInExternalEditor(field.GetText())
	if err != nil {
		em.setStatus(fmt.Sprintf("External editor failed: %v", err))
		return
	}
	field.SetText(edited, false)
	em.setStatus(fmt.Sprintf("%s updated from external editor.", label))
}

// styleButton overrides tview's default button colours (a bright cyan
// background with white label text when focused, which is very low
// contrast and hard to read) with a scheme that stays legible in both the
// unfocused and focused/activated state.
/*func styleButton(b *tview.Button) *tview.Button {
	b.SetLabelColor(tcell.ColorGreen)
	b.SetBackgroundColor(tcell.ColorYellow)
	b.SetLabelColorActivated(tcell.ColorBlack)
	b.SetBackgroundColorActivated(tcell.ColorWhite)
	return b
}*/

// newHorizontalRule returns a Box that draws a single-line horizontal rule
// spanning its full width, whatever that happens to be - unlike a fixed-
// length string of "─" characters, this adapts to the actual terminal size
// rather than falling short (or wrapping) on anything but one exact width.
func newHorizontalRule() *tview.Box {
	box := tview.NewBox()
	box.SetDrawFunc(func(screen tcell.Screen, x, y, width, height int) (int, int, int, int) {
		for i := 0; i < width; i++ {
			screen.SetContent(x+i, y, '─', nil, tcell.StyleDefault)
		}
		return x, y, width, height
	})
	return box
}

// progressBar is a small custom widget that draws a playback-position bar
// spanning its full width, redrawn from live elapsed/total values each time
// tview asks it to draw. Like newHorizontalRule, this stays correct at any
// terminal size rather than being rendered once at a fixed character width.
type progressBar struct {
	*tview.Box
	elapsedMs int64
	totalMs   int64
}

func newProgressBar() *progressBar {
	pb := &progressBar{Box: tview.NewBox()}
	pb.SetDrawFunc(func(screen tcell.Screen, x, y, width, height int) (int, int, int, int) {
		pb.draw(screen, x, y, width)
		return x, y, width, height
	})
	return pb
}

// SetProgress updates the bar's data. The next time tview redraws the
// screen (e.g. via the app.QueueUpdateDraw calls already happening on every
// incoming frame) the bar picks up the new values automatically.
func (pb *progressBar) SetProgress(elapsedMs, totalMs int64) {
	pb.elapsedMs = elapsedMs
	pb.totalMs = totalMs
}

func (pb *progressBar) draw(screen tcell.Screen, x, y, width int) {
	prefix := formatDuration(pb.elapsedMs) + " ["
	suffix := "] " + formatDuration(pb.totalMs)
	barWidth := width - len(prefix) - len(suffix)
	if barWidth < 1 {
		barWidth = 1
	}

	safeTotal := pb.totalMs
	if safeTotal <= 0 {
		safeTotal = 1
	}
	frac := float64(pb.elapsedMs) / float64(safeTotal)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * float64(barWidth))

	line := prefix + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + suffix
	tview.Print(screen, line, x, y, width, tview.AlignLeft, tcell.ColorYellow)
}

// formatDuration renders a millisecond timestamp as mm:ss.
func formatDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	totalSeconds := ms / 1000
	m := totalSeconds / 60
	s := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}
