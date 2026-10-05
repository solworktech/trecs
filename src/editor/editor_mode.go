package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	libtrecs "trecs/lib"
)

// EditorMode wraps playback with interactive editing
type EditorMode struct {
	player                     libtrecs.TerminalPlayer
	commands                   []libtrecs.Command
	filePath                   string
	app                        *tview.Application
	pages                      *tview.Pages
	previousPage               string
	currentCmd                 *libtrecs.Command
	currentCmdIdx              int
	rootFlex                   *tview.Flex
	playbackFlex               *tview.Flex
	playbackLegend             *tview.TextView
	playbackSeparator          *tview.Box
	playbackMargin             *tview.Box
	playbackView               *tview.TextView
	progressBar                *progressBar
	display                    libtrecs.Screen
	displayCols, displayRows   int
	totalDurationMs            int64
	statusView                 *tview.TextView
	inputField                 *tview.TextArea
	outputField                *tview.TextArea
	annotationField            *tview.TextArea
	annotationDurationField    *tview.InputField
	annotationModalView        *tview.TextView
	annotationModalTopView     *tview.TextView
	annotationModalIsTop       bool
	annotationModalVisible     bool
	wasPlayingBeforeAnnotation bool
	// lastSeenCmdIdx tracks the command index the frame callback last saw,
	// so an auto-shown annotation triggers exactly once per transition
	// into a new command rather than on every frame within it.
	//
	// stateMu protects lastSeenCmdIdx, which is read and written from two
	// different goroutines: the frame callback runs on the player's own
	// background goroutine, while jumps and reloads run on the UI
	// goroutine.
	//
	// There is deliberately no "already shown" set alongside it. An
	// annotation previews whenever playback transitions into its command,
	// and playback only transitions into a command once per pass through
	// the timeline: a seek resets lastSeenCmdIdx, so everything from the
	// seek point onward (not just the seek target) previews again as it's
	// reached, and the player guarantees no stale pre-seek frame is
	// delivered afterwards to cause a spurious transition. A remembered
	// set added nothing to that except suppressing previews after a seek.
	stateMu        sync.Mutex
	lastSeenCmdIdx int
	// displayGeneration increments every time em.display is reset/rebuilt
	// (a jump, a reload, an annotation clearing the screen). A frame
	// callback captures the generation current when it received its frame
	// and discards its update if that no longer matches by the time it
	// actually runs - see makeFrameCallback - which is what stops a frame
	// that was already in flight from a moment before a rebuild from
	// landing in the wrong (newly rebuilt) buffer afterwards. atomic.Int64
	// deliberately, not a plain int: it's read on the player's background
	// goroutine and written on the UI goroutine, and an unsynchronized
	// plain int would itself be exactly the same class of bug this field
	// exists to fix.
	displayGeneration atomic.Int64
	debugLog          *os.File
	// pendingAnnotationFrame holds a command's first frame when its
	// annotation is being auto-previewed: that frame is deliberately not
	// fed to the display yet ("show the annotation before rendering the
	// command"), and is delivered once the preview ends.
	pendingAnnotationFrame *libtrecs.TerminalFrame
	annotationTimer        *time.Timer
	fullScreen             bool
	// buttonFlex      *tview.Flex
	commandList     *tview.List
	deletedCommands map[int]bool
	editedCommands  map[int]bool
}

// NewEditorMode creates a new editor mode
func NewEditorMode(player libtrecs.TerminalPlayer, commands []libtrecs.Command, filePath string) *EditorMode {
	// Opt-in debug log (TRECS_DEBUG=1) of playback transitions, jumps and
	// annotation decisions, written to /tmp/trecs-debug.log. Off by default.
	var logFile *os.File
	if os.Getenv("TRECS_DEBUG") != "" {
		logFile, _ = os.OpenFile("/tmp/trecs-debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	}

	return &EditorMode{
		player:          player,
		commands:        commands,
		filePath:        filePath,
		app:             tview.NewApplication(),
		deletedCommands: make(map[int]bool),
		editedCommands:  make(map[int]bool),
		lastSeenCmdIdx:  -1,
		debugLog:        logFile,
	}
}

// debugf writes a timestamped line to the debug log, if one is open.
// Safe to call from either goroutine, since os.File writes are safe for
// concurrent use.
func (em *EditorMode) debugf(format string, args ...interface{}) {
	if em.debugLog == nil {
		return
	}
	_, _ = fmt.Fprintf(em.debugLog, "[%s] "+format+"\n", append([]interface{}{time.Now().Format("15:04:05.000")}, args...)...)
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
	em.rootFlex = root

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

	em.measureDisplaySize()
	em.display = em.newDisplay()

	em.player.SetFrameCallback(em.makeFrameCallback())

	// Start playback without raw mode / stdout writes - tview owns the terminal
	if err := em.player.PlayWithoutRawMode(em.filePath); err != nil {
		return err
	}

	em.totalDurationMs = em.player.GetTotalDurationMs()
	em.progressBar.SetProgress(0, em.totalDurationMs)

	clockDone := make(chan struct{})
	defer close(clockDone)
	go em.runProgressClock(clockDone)

	defer func() {
		em.player.Stop()
		time.Sleep(200 * time.Millisecond)
		_ = em.player.RestoreTerminal()
		_ = os.Stdout.Sync()
		_ = os.Stderr.Sync()
	}()

	return em.app.Run()
}

// measureDisplaySize sizes the playback display to the recording's actual
// terminal usage (see MeasureSize) and caches the result in
// displayCols/displayRows for newDisplay to use. Frame data is loaded
// independently here (rather than reusing the player's own, internally
// loaded frames) because this runs before the player has loaded anything
// (Run) or after a save has changed the file out from under it
// (reloadFromDisk); a LoadFrames failure here isn't fatal - it just leaves
// whatever size was already cached (the minimum, the first time) rather
// than blocking playback.
// measureDisplaySize sizes the playback display to the recording's actual
// terminal usage and caches the result in displayCols/displayRows for
// newDisplay to use. It prefers the size the recorder itself captured at
// record time (RecordingMeta, written once as the first line of the file -
// see terminal_recorder.go); MeasureSize's replay-based estimate (see its
// own doc comment) is only a fallback for a recording made before that
// existed, or the rare one where the recorder genuinely couldn't read the
// terminal size. The persisted size is authoritative when present - it's
// what the terminal really was, not an estimate from what happened to get
// drawn - so there's no reason to fall back to measuring even if it seems
// implausibly small or large.
//
// Frame data is loaded independently here (rather than reusing the
// player's own, internally loaded frames) because this runs before the
// player has loaded anything (Run) or after a save has changed the file
// out from under it (reloadFromDisk); a LoadFrames failure here isn't
// fatal - it just leaves whatever size was already cached (the minimum,
// the first time) rather than blocking playback.
func (em *EditorMode) measureDisplaySize() {
	frames, _, meta, err := libtrecs.LoadFrames(em.filePath)
	if err != nil {
		em.debugf("measureDisplaySize: %v (keeping previous size)", err)
		return
	}
	if meta.Width > 0 && meta.Height > 0 {
		em.displayCols, em.displayRows = meta.Width, meta.Height
		em.debugf("measureDisplaySize: %dx%d (from recording metadata)", em.displayCols, em.displayRows)
		return
	}
	em.displayCols, em.displayRows = libtrecs.MeasureSize(frames)
	em.debugf("measureDisplaySize: %dx%d (estimated: no recording metadata)", em.displayCols, em.displayRows)
}

// newDisplay creates an empty display sized by the most recent
// measureDisplaySize call (or the minimum size, if that has never run).
func (em *EditorMode) newDisplay() libtrecs.Screen {
	return libtrecs.NewVTScreenSized(em.displayCols, em.displayRows)
}

// makeFrameCallback builds the per-frame handler shared by Run() and
// reloadFromDisk(): normally it feeds the frame to the display buffer and
// updates the screen/progress bar/status as before, but when playback is
// about to cross into a new command whose annotation has an auto-preview
// duration, it instead holds that frame back, pauses, and shows the
// annotation - the frame is delivered once the preview ends (see
// hideAnnotationModal).
func (em *EditorMode) makeFrameCallback() func(libtrecs.TerminalFrame) {
	return func(frame libtrecs.TerminalFrame) {
		em.stateMu.Lock()
		rawIdx := em.player.GetCurrentFrameIndex()
		newIdx := libtrecs.GetCommandIndexAtFrame(rawIdx, em.commands)
		shouldTrigger := false
		if newIdx != em.lastSeenCmdIdx {
			prevSeen := em.lastSeenCmdIdx
			em.lastSeenCmdIdx = newIdx
			var reason string
			eligible := false
			if newIdx >= 0 && newIdx < len(em.commands) {
				cmd := em.commands[newIdx]
				switch {
				case !cmd.HasAnnotation:
					reason = "HasAnnotation=false"
				case cmd.Annotation.DurationSeconds <= 0:
					reason = fmt.Sprintf("DurationSeconds=%d", cmd.Annotation.DurationSeconds)
				default:
					reason = "eligible"
					eligible = true
				}
			} else {
				reason = "index out of range"
			}
			em.debugf("TRANSITION rawIdx=%d prevSeen=%d newIdx=%d reason=%s", rawIdx, prevSeen, newIdx, reason)
			if eligible {
				shouldTrigger = true
			}
		}
		em.stateMu.Unlock()

		if shouldTrigger {
			em.debugf("AUTO-PREVIEW TRIGGERING for command %d", newIdx)
			em.player.Pause()
			frameCopy := frame
			gen := em.displayGeneration.Load()
			em.app.QueueUpdateDraw(func() {
				if em.displayGeneration.Load() != gen {
					em.debugf("AUTO-PREVIEW for command %d discarded as stale (gen mismatch)", newIdx)
					return // a rebuild happened after this frame was produced - stale, discard
				}
				em.autoShowAnnotation(newIdx, frameCopy)
			})
			return
		}

		// Feed/Render deliberately happen inside QueueUpdateDraw, not out
		// here: this closure runs on the player's own background
		// goroutine, and em.display can be reassigned (rebuilt) from the
		// UI goroutine at any time (a jump) with no lock between the two -
		// mutating it out here was a genuine data race with that
		// reassignment, which is what actually let a frame from whatever
		// was playing before a jump land in the freshly rebuilt buffer.
		gen := em.displayGeneration.Load()
		isLastFrame := rawIdx == em.player.GetTotalFrames()-1
		em.app.QueueUpdateDraw(func() {
			if em.displayGeneration.Load() != gen {
				return
			}
			em.display.Feed(frame.Data)
			rendered := em.display.Render()
			em.playbackView.SetText(rendered)
			em.playbackView.ScrollToEnd()
			em.progressBar.SetProgress(frame.Timestamp, em.totalDurationMs)
			if isLastFrame {
				// playbackLoop deliberately keeps running (idling) rather
				// than exiting once it reaches the last frame - see its
				// own comment - specifically so a later seek can still
				// resume it. Nothing else announces that this happened,
				// so this is done here, synchronously with the same
				// per-frame update the ordinary status line uses, rather
				// than from a separate poller: a poller checking
				// GetPlaybackState() on its own timer could just as
				// easily run right after this same closure and
				// overwrite this message with the ordinary
				// currentAnnotationHint() the very next moment.
				em.setStatus("Finished. Space/P to replay from the start.")
			} else {
				em.statusView.SetText(em.currentAnnotationHint())
			}
		})
	}
}

// shouldAutoShowAnnotation reports whether the command at idx has an
// annotation configured to preview automatically (a positive
// DurationSeconds).
func (em *EditorMode) shouldAutoShowAnnotation(idx int) bool {
	if idx < 0 || idx >= len(em.commands) {
		return false
	}
	cmd := em.commands[idx]
	return cmd.HasAnnotation && cmd.Annotation.DurationSeconds > 0
}

// annotationLingerDelay is how long the previous command's final output
// stays on screen, paused, before it's cleared to reveal an auto-previewed
// annotation - clearing the instant playback transitions into the new
// command felt abrupt, as if the just-finished command had been cut off
// mid-view rather than given a moment to be read.
const annotationLingerDelay = 3 * time.Second

// autoShowAnnotation begins an auto-preview for the command at idx: it
// pauses playback and holds frame back (it belongs to that command, and
// would otherwise render before the annotation had a chance to appear),
// but leaves whatever's currently on screen untouched for
// annotationLingerDelay before actually clearing and revealing the
// annotation (revealAutoAnnotation) - unless the screen is already blank
// (e.g. straight after a jump), in which case there's nothing to linger
// over and it reveals at once. Must be called on the UI goroutine (from
// within QueueUpdateDraw).
func (em *EditorMode) autoShowAnnotation(idx int, frame libtrecs.TerminalFrame) {
	em.wasPlayingBeforeAnnotation = true // we only ever get here from an active, playing frame callback
	frameCopy := frame
	em.pendingAnnotationFrame = &frameCopy

	// Marked visible (and thus guarded in handleInput) for this whole
	// sequence, linger included - the player is paused and a frame is
	// being held back throughout, not just once the panel is actually
	// showing, so other bindings need to stay blocked from the start; see
	// the modal-guard comment in handleInput for why.
	em.annotationModalVisible = true
	em.annotationModalIsTop = true

	// The linger exists so the previous command's output isn't wiped the
	// instant playback moves on. With nothing on screen - right after a
	// jump has cleared it, or when the very first command is annotated -
	// there's nothing to linger over, so reveal immediately.
	if em.display.IsBlank() {
		em.debugf("AUTO-PREVIEW for command %d: screen blank, skipping linger", idx)
		em.revealAutoAnnotation(idx)
		return
	}

	em.annotationTimer = time.AfterFunc(annotationLingerDelay, func() {
		em.app.QueueUpdateDraw(func() {
			em.revealAutoAnnotation(idx)
		})
	})
}

// revealAutoAnnotation clears the screen and raises the annotation panel,
// once annotationLingerDelay has given the previous command's output a
// moment to be read. Starts the timer for the annotation's own configured
// preview duration, at the end of which hideAnnotationModal delivers the
// held-back frame.
func (em *EditorMode) revealAutoAnnotation(idx int) {
	cmd := em.commands[idx]

	// Clear now, before the panel goes up - otherwise the previous
	// command's content is still sitting there underneath/around it,
	// making it look like that command was the annotated one rather than
	// the one about to play. The held-back frame is fed into another
	// fresh buffer when the preview ends (hideAnnotationModal), since
	// nothing should be fed into this now-cleared one in the meantime.
	em.display = em.newDisplay()
	em.displayGeneration.Add(1)
	em.playbackView.SetText("")

	em.annotationModalTopView.SetText(cmd.Annotation.Text)
	em.playbackFlex.ResizeItem(em.annotationModalTopView, 5, 0)
	em.setStatus(em.currentAnnotationHint())

	duration := time.Duration(cmd.Annotation.DurationSeconds) * time.Second
	em.annotationTimer = time.AfterFunc(duration, func() {
		em.app.QueueUpdateDraw(func() {
			em.hideAnnotationModal()
		})
	})
}

// setFullScreen shows or hides the playback page's chrome (legend,
// separator, margin, progress bar) and the shared status bar, maximising
// the terminal output area. All key bindings continue to work either way -
// this only changes what's drawn, not what handleInput accepts.
func (em *EditorMode) setFullScreen(on bool) {
	em.fullScreen = on
	chromeSize := 1
	statusSize := 1
	if on {
		chromeSize = 0
		statusSize = 0
	}
	em.playbackFlex.ResizeItem(em.playbackLegend, chromeSize, 0)
	em.playbackFlex.ResizeItem(em.playbackSeparator, chromeSize, 0)
	em.playbackFlex.ResizeItem(em.playbackMargin, chromeSize, 0)
	em.playbackFlex.ResizeItem(em.progressBar, chromeSize, 0)
	em.rootFlex.ResizeItem(em.statusView, statusSize, 0)
}

func (em *EditorMode) toggleFullScreen() {
	em.setFullScreen(!em.fullScreen)
}

func (em *EditorMode) createPlaybackView() tview.Primitive {
	em.playbackLegend = tview.NewTextView().
		SetDynamicColors(true).
		SetText("[yellow]Space[white]/[yellow]P[white] Pause/Resume    [yellow]E[white] Edit Current Command    " +
			"[yellow]^P[white]/[yellow]^N[white] Prev/Next Command    " +
			"[yellow]^L[white] Browse Commands    [yellow]^F[white] Full Screen    [yellow]Q[white] Quit")

	em.playbackSeparator = newHorizontalRule()

	em.playbackView = tview.NewTextView().SetText("\n")
	em.playbackView.SetDynamicColors(true)
	em.playbackView.SetScrollable(true)

	// Two separate panels, each hidden by default (0 rows), rather than
	// one repositioned widget: an auto-preview ("before rendering the
	// command") shows at the top, just under the legend, while a manually
	// requested one (Ctrl+A on already-playing content) shows at the
	// bottom, just above the progress bar - each stays fixed in the
	// layout, and only one is ever visible at a time (see
	// annotationModalIsTop). ColorNavy, not ColorBlue - ColorBlue renders
	// closer to cyan and makes the white foreground hard to read.
	em.annotationModalTopView = newAnnotationModalView()
	em.annotationModalView = newAnnotationModalView()

	em.playbackMargin = tview.NewBox()

	em.progressBar = newProgressBar()

	em.playbackFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(em.playbackLegend, 1, 0, false).
		AddItem(em.playbackSeparator, 1, 0, false).
		AddItem(em.annotationModalTopView, 0, 0, false).
		AddItem(em.playbackView, 0, 1, false).
		AddItem(em.annotationModalView, 0, 0, false).
		AddItem(em.playbackMargin, 1, 0, false).
		AddItem(em.progressBar, 1, 0, false)

	return em.playbackFlex
}

func (em *EditorMode) createEditView() tview.Primitive {
	em.inputField = tview.NewTextArea()
	em.inputField.SetWrap(true)
	em.inputField.SetBorder(true).SetTitle(" Input ")

	em.outputField = tview.NewTextArea()
	em.outputField.SetWrap(true)
	em.outputField.SetBorder(true).SetTitle(" Output ")

	em.annotationField = tview.NewTextArea()
	em.annotationField.SetWrap(true)
	em.annotationField.SetBorder(true).SetTitle(" Annotation ")

	em.annotationDurationField = tview.NewInputField()
	em.annotationDurationField.SetAcceptanceFunc(tview.InputFieldInteger)
	// tview.InputField defaults its field (typed-text) background to a
	// contrasting blue distinct from the surrounding Box background, which
	// has the same white-on-unreadable-blue problem as ColorBlue elsewhere.
	// Black, matching the other fields.
	em.annotationDurationField.SetFieldBackgroundColor(tcell.ColorBlack)
	em.annotationDurationField.SetFieldTextColor(tcell.ColorWhite)
	em.annotationDurationField.SetBorder(true).SetTitle(" Annotation Duration (seconds, 0 = off) ")

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
		AddItem(em.outputField, 10, 0, false).
		AddItem(em.annotationField, 5, 0, false).
		AddItem(em.annotationDurationField, 3, 0, false)
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
		em.quit()
		return nil
	}

	// Full-screen toggles from anywhere; every other binding still works
	// the same regardless of which state it's in - this only changes what
	// chrome is drawn.
	if event.Key() == tcell.KeyCtrlF {
		em.toggleFullScreen()
		return nil
	}

	pageName, _ := em.pages.GetFrontPage()

	// On the playback page specifically, Esc also exits full screen (the
	// command browser page already has its own Esc behaviour - closing
	// itself - which takes precedence there instead).
	if event.Key() == tcell.KeyEscape && pageName == "playback" && em.fullScreen {
		em.setFullScreen(false)
		return nil
	}

	// Ctrl+L toggles the command browser from anywhere (except itself,
	// where Esc closes it instead). It deliberately leaves an in-progress
	// annotation alone: merely opening a browser isn't a reason to destroy
	// a preview, and cancelling it here left the player paused for an
	// annotation that no longer existed, with nothing left to say so. It
	// finishes on its own, or is still showing on Esc. Whatever actually
	// leaves the playback page (a jump, the editor) cancels it itself.
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
			em.cancelAnnotation()
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
				em.app.SetFocus(em.annotationField)
				return nil
			case em.annotationField:
				em.app.SetFocus(em.annotationDurationField)
				return nil
			case em.annotationDurationField:
				em.app.SetFocus(em.inputField)
				return nil
			}
		}
		return event
	}

	// Playback mode controls

	if event.Key() == tcell.KeyCtrlA {
		if em.annotationModalVisible {
			em.hideAnnotationModal()
		} else {
			em.showAnnotationModal()
		}
		return nil
	}

	if event.Key() == tcell.KeyCtrlP {
		cur := libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
		em.cancelAnnotation()
		em.jumpToCommandForPlayback(cur - 1)
		return nil
	}
	if event.Key() == tcell.KeyCtrlN {
		cur := libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
		em.cancelAnnotation()
		em.jumpToCommandForPlayback(cur + 1)
		return nil
	}

	if event.Key() == tcell.KeyRune {
		switch event.Rune() {
		case ' ', 'p', 'P':
			if em.annotationModalVisible {
				// No-op, not "same as Ctrl+A": playback is already paused
				// for the annotation regardless of phase (linger or
				// actually showing), and during the linger phase there's
				// no visible indication anything's even happening, so
				// treating a pause attempt as "skip the annotation" here
				// is just confusing - it looks like pressing pause cleared
				// the screen. Ctrl+A remains the only explicit dismiss.
				return nil
			}
			switch em.player.GetPlaybackState() {
			case libtrecs.PlaybackPlaying:
				em.player.Pause()
				em.setStatus("Paused." + em.currentAnnotationHint())
			case libtrecs.PlaybackPaused:
				em.player.Resume()
				em.setStatus("Resumed." + em.currentAnnotationHint())
			case libtrecs.PlaybackStopped:
				// Playback reached the end on its own (see playbackLoop's
				// own comment on why the player stays alive rather than
				// exiting at that point) - treat the same key that
				// pauses/resumes as "replay from the start" here, since
				// there's nothing left to pause or resume. Reuses the
				// exact jump machinery ^P/^N use for any other command
				// jump, just targeting the first one, so it gets the same
				// display-clearing and annotation-retriggering behaviour
				// a jump to command 0 would.
				em.jumpToCommandForPlayback(0)
			}
			return nil
		case 'e', 'E':
			em.cancelAnnotation()
			if em.player.GetPlaybackState() == libtrecs.PlaybackPlaying {
				em.player.Pause()
			}
			em.currentCmdIdx = libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
			if em.currentCmdIdx >= 0 && em.currentCmdIdx < len(em.commands) {
				em.currentCmd = &em.commands[em.currentCmdIdx]
				em.inputField.SetText(em.currentCmd.InputText, false)
				em.outputField.SetText(em.currentCmd.OutputText, false)
				em.annotationField.SetText(em.currentCmd.Annotation.Text, false)
				em.annotationDurationField.SetText(strconv.Itoa(em.currentCmd.Annotation.DurationSeconds))
			}

			em.setStatus(fmt.Sprintf("Editing command %d of %d.", em.currentCmdIdx+1, len(em.commands)))
			em.outputField.SetSize(30, 0)
			em.pages.SwitchToPage("edit")
			em.app.SetFocus(em.inputField)
			return nil
		case 'q', 'Q':
			em.quit()
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

// currentAnnotationHint returns the text to append to the status line
// reflecting whether the command at the player's current position has an
// annotation - "" if not, so callers can just concatenate it onto
// whatever else they're setting the status to.
func (em *EditorMode) currentAnnotationHint() string {
	if em.annotationModalVisible {
		return "   [yellow]^A[white] Close Annotation"
	}
	idx := libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
	if idx < 0 || idx >= len(em.commands) || !em.commands[idx].HasAnnotation {
		return ""
	}
	return "   [yellow]^A[white] Display Annotation"
}

// showAnnotationModal pauses playback (if playing) and expands the
// annotation panel above the progress bar to show the current command's
// annotation, with a blue background / white foreground per spec. Does
// nothing if the current command has none.
func (em *EditorMode) showAnnotationModal() {
	idx := libtrecs.GetCommandIndexAtFrame(em.player.GetCurrentFrameIndex(), em.commands)
	if idx < 0 || idx >= len(em.commands) || !em.commands[idx].HasAnnotation {
		return
	}

	em.wasPlayingBeforeAnnotation = em.player.GetPlaybackState() == libtrecs.PlaybackPlaying
	if em.wasPlayingBeforeAnnotation {
		em.player.Pause()
	}

	em.annotationModalVisible = true
	em.annotationModalIsTop = false
	em.annotationModalView.SetText(em.commands[idx].Annotation.Text)
	em.playbackFlex.ResizeItem(em.annotationModalView, 5, 0)
	em.setStatus(em.currentAnnotationHint())
}

// hideAnnotationModal collapses the annotation panel back to hidden.
// For a manually-opened annotation (Ctrl+A on already-rendered content) it
// just resumes playback, if it was actually playing (as opposed to already
// paused by the person) before the annotation interrupted it. For an
// auto-previewed one (whether ended naturally via its timer or dismissed
// early with Ctrl+A) it additionally clears the screen and delivers the
// command's held-back first frame before resuming, so the command starts
// rendering fresh rather than resuming mid-stream into stale content.
func (em *EditorMode) hideAnnotationModal() {
	em.annotationModalVisible = false
	if em.annotationModalIsTop {
		em.playbackFlex.ResizeItem(em.annotationModalTopView, 0, 0)
	} else {
		em.playbackFlex.ResizeItem(em.annotationModalView, 0, 0)
	}

	if em.annotationTimer != nil {
		em.annotationTimer.Stop()
		em.annotationTimer = nil
	}

	if em.pendingAnnotationFrame != nil {
		frame := *em.pendingAnnotationFrame
		em.pendingAnnotationFrame = nil
		em.display = em.newDisplay()
		em.displayGeneration.Add(1)
		em.display.Feed(frame.Data)
		em.playbackView.SetText(em.display.Render())
		em.playbackView.ScrollToEnd()
		em.progressBar.SetProgress(frame.Timestamp, em.totalDurationMs)
		em.player.Resume()
	} else if em.wasPlayingBeforeAnnotation {
		em.player.Resume()
	}

	em.setStatus(em.currentAnnotationHint())
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
	em.annotationField.SetText(em.currentCmd.Annotation.Text, false)
	em.annotationDurationField.SetText(strconv.Itoa(em.currentCmd.Annotation.DurationSeconds))

	seekTarget := em.currentCmd.StartTime
	if em.currentCmd.HasPrompt {
		seekTarget = em.currentCmd.PromptFrame.Timestamp
	}
	em.player.SeekToFrame(em.currentCmd.FirstRawFrameIndex)
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
	// The browser no longer cancels an in-progress annotation on open, so
	// do it here: otherwise its timer could fire while the editor is up and
	// resume playback underneath it.
	em.cancelAnnotation()
	if em.player.GetPlaybackState() == libtrecs.PlaybackPlaying {
		em.player.Pause()
	}
	em.jumpToCommand(idx)
	em.pages.SwitchToPage("edit")
	em.app.SetFocus(em.inputField)
}

// jumpToCommandForPlayback is used both from the Ctrl+L command browser and
// from Ctrl+P/Ctrl+N during ordinary playback: seeks playback to the chosen
// command and resumes playback from there, returning to the playback page.
// It always resumes: whether the player happens to be paused at this point
// says nothing about intent - it may be paused only because an annotation
// preview paused it, which is exactly what left ^L then ^P seeking without
// ever playing.
func (em *EditorMode) jumpToCommandForPlayback(idx int) {
	if idx < 0 || idx >= len(em.commands) {
		return
	}
	em.debugf("JUMP requested: idx=%d", idx)

	// Always pause first, even if already paused: if playback is actively
	// running, its background goroutine can otherwise keep feeding frames
	// from wherever it currently is - not yet aware a seek is coming -
	// straight into the buffer clearDisplayForJump is about to replace,
	// for as long as it takes to notice the seek. Pausing first bounds
	// that to at most one frame already in flight, which the generation
	// counter in makeFrameCallback discards regardless.
	em.player.Pause()

	cmd := em.commands[idx]
	seekTarget := cmd.StartTime
	if cmd.HasPrompt {
		seekTarget = cmd.PromptFrame.Timestamp
	}
	// By frame index, not timestamp: a command's last output frame and the
	// next command's prompt commonly share a millisecond, and a timestamp
	// seek lands on the first of them - i.e. on the previous command's
	// frame, which would preview that command's annotation ahead of the one
	// actually jumped to.
	em.player.SeekToFrame(cmd.FirstRawFrameIndex)
	em.debugf("JUMP: SeekToFrame(%d) for target idx=%d", cmd.FirstRawFrameIndex, idx)
	em.clearDisplayForJump()
	em.currentCmdIdx = idx
	// Deliberately reset rather than set to idx: SeekTo lands exactly on
	// the target's own prompt frame, which the *next* frame callback
	// invocation will process as playback resumes below. Setting this to
	// idx here would make that callback's newIdx == lastSeenCmdIdx already
	// match, silently pre-empting its own transition detection - and with
	// it, the annotation auto-preview trigger for the very command being
	// jumped to. Resetting to the "unknown" sentinel guarantees that
	// callback sees a fresh transition instead.
	em.stateMu.Lock()
	prevLastSeen := em.lastSeenCmdIdx
	em.lastSeenCmdIdx = -1
	em.stateMu.Unlock()
	em.debugf("JUMP: reset lastSeenCmdIdx %d -> -1", prevLastSeen)
	em.progressBar.SetProgress(seekTarget, em.totalDurationMs)
	em.player.Resume()
	em.pages.SwitchToPage("playback")
	em.setStatus(fmt.Sprintf("Jumped to command %d of %d.", idx+1, len(em.commands)) + em.currentAnnotationHint())
}

// clearDisplayForJump wipes the screen ahead of a jump to a different
// command: a jump seeks exactly to that command's own content (its
// annotation, if any, then its input and output), not a replay of every
// command that happened to come before it. SeekTo lands the player's
// position exactly on the target's own prompt frame, so the normal resumed
// playback immediately following this call feeds it - and everything
// after - next; nothing needs to be fed here.
func (em *EditorMode) clearDisplayForJump() {
	em.display = em.newDisplay()
	em.displayGeneration.Add(1)
	em.playbackView.SetText("")
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
		newAnnotation := em.annotationField.GetText()
		newDuration, err := strconv.Atoi(em.annotationDurationField.GetText())
		if err != nil {
			newDuration = 0
		}

		cmd := &em.commands[em.currentCmdIdx]
		if newInput != cmd.InputText || newOutput != cmd.OutputText || newAnnotation != cmd.Annotation.Text || newDuration != cmd.Annotation.DurationSeconds {
			cmd.InputText = newInput
			cmd.OutputText = newOutput
			cmd.Annotation.Text = newAnnotation
			cmd.Annotation.DurationSeconds = newDuration
			cmd.HasAnnotation = strings.TrimSpace(newAnnotation) != ""
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

// quit stops playback and the application - shared by every quit path
// (Ctrl+C, Q on the playback page, Q while the annotation modal is
// showing) so they can't drift out of sync with each other.
func (em *EditorMode) quit() {
	em.player.Stop()
	em.app.Stop()
}

// cancelAnnotation stops any in-progress annotation sequence (auto-preview
// linger/display, or a manually-opened one) and discards its held-back
// frame without delivering it, without touching playback state itself -
// callers decide what happens next (seeking elsewhere, resuming, switching
// pages). Discarding rather than delivering is correct here specifically
// because the caller is about to move the display to a different point in
// the timeline anyway (clearDisplayForJump or similar); delivering a
// frame that belongs to the position being left would be the out-of-order
// mistake, not the discard. Safe to call even when no annotation is
// active.
func (em *EditorMode) cancelAnnotation() {
	if em.annotationTimer != nil {
		em.annotationTimer.Stop()
		em.annotationTimer = nil
	}
	if em.annotationModalVisible {
		if em.annotationModalIsTop {
			em.playbackFlex.ResizeItem(em.annotationModalTopView, 0, 0)
		} else {
			em.playbackFlex.ResizeItem(em.annotationModalView, 0, 0)
		}
	}
	em.annotationModalVisible = false
	em.pendingAnnotationFrame = nil
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

	// Ripple-delete semantics: every surviving command's timestamps are
	// shifted so no dead time remains where a deleted command used to be,
	// rather than writing finalCommands as-is with their original
	// (now gappy) timestamps.
	compressedCommands := libtrecs.CompressTimestamps(em.commands, em.deletedCommands)

	if err := libtrecs.RebuildRecording(em.filePath, backupPath, compressedCommands); err != nil {
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
	em.measureDisplaySize()
	em.display = em.newDisplay()
	em.displayGeneration.Add(1)
	em.playbackView.Clear()

	newPlayer.SetFrameCallback(em.makeFrameCallback())

	em.commands = newCommands
	em.deletedCommands = make(map[int]bool)
	em.editedCommands = make(map[int]bool)
	em.stateMu.Lock()
	em.lastSeenCmdIdx = -1
	em.stateMu.Unlock()
	em.currentCmdIdx = -1
	em.currentCmd = nil

	em.player = newPlayer
	if err := newPlayer.PlayWithoutRawMode(em.filePath); err != nil {
		em.setStatus(fmt.Sprintf("Saved, but failed to restart playback: %v", err))
		return
	}
	em.totalDurationMs = newPlayer.GetTotalDurationMs()

	if targetIdx >= 0 && targetIdx < len(newCommands) {
		cmd := newCommands[targetIdx]
		seekTarget := cmd.StartTime
		if cmd.HasPrompt {
			seekTarget = cmd.PromptFrame.Timestamp
		}
		newPlayer.SeekToFrame(cmd.FirstRawFrameIndex)
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
// newAnnotationModalView creates one annotation display panel - the
// top-positioned (auto-preview) and bottom-positioned (manual Ctrl+A)
// panels are separate widgets sharing this same styling, per
// createPlaybackView. ColorNavy, not ColorBlue - ColorBlue renders closer
// to cyan and makes the white foreground hard to read.
func newAnnotationModalView() *tview.TextView {
	v := tview.NewTextView().SetDynamicColors(true)
	v.SetWrap(true)
	v.SetBackgroundColor(tcell.ColorNavy)
	v.SetTextColor(tcell.ColorWhite)
	v.SetBorder(true).SetTitle(" Annotation ")
	v.SetBorderColor(tcell.ColorWhite)
	return v
}

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

// runProgressClock keeps the progress bar moving between frames. The frame
// callback only moves it when a frame is delivered, so a long silence in the
// recording - a command waiting on the network, say - left the bar standing
// still as if playback had hung. The update runs on the UI goroutine (so it
// reads em.player where it is also replaced, with no race), and only while
// playing: paused, the bar must hold still.
func (em *EditorMode) runProgressClock(done <-chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		select {
		case <-done:
			return
		default:
		}
		em.app.QueueUpdateDraw(func() {
			if p := em.player; p != nil && p.GetPlaybackState() == libtrecs.PlaybackPlaying {
				em.progressBar.SetProgress(p.Position(), em.totalDurationMs)
			}
		})
	}
}
