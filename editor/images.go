package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// Images: a picture shown over the terminal at a time of the recording, for a number of seconds (see lib/images.go for how it is
// kept, and why it takes time of its own). Ctrl+G on the playback screen adds one at the time playback is at: a modal with the time,
// the file (typed, or chosen in a file picker, Ctrl+O) and for how long. Everything after it moves later by that long, and the sound,
// if the recording has any, gets that much silence. In the list of commands (Ctrl+L) an image is a row of its own, "display image:
// NAME", and has what a command has there: Ctrl+P plays from it, Ctrl+E edits it - the modal, with its time, file and duration to
// change and Delete - and Ctrl+D deletes it.
//
// An image is on show while playback is in [its time, its time + its duration): which is worked out from the position, over and over
// (the progress clock), and not started and stopped with a timer, so it is also on show when playback is moved into it, or paused in
// it. It is drawn as a picture where the terminal can do sixel graphics (the same painter as the recording's own pictures, see
// sixel.go), and as a card with the details of the file where it cannot - which is also what a WebP is, that the editor has no decoder
// for. The web player shows the real picture.

const (
	imageModalPage  = "imagemodal"
	imagePickerPage = "imagepicker"

	// imageKey adds an image. Ctrl+M would have been the mnemonic, but a terminal cannot tell it from Enter (they are the same byte), as it
	// cannot tell Ctrl+I from Tab.
	imageKey = tcell.KeyCtrlG

	imageOverlayBackground = tcell.ColorBlack
)

// ---- the list of commands ------------------------------------------------------------------------------------------------------

// listRow is a row of the list of commands: a command, or - img is the id - an image.
type listRow struct {
	cmd int
	img string
}

// commandRows are the rows of the list: each command, and after it the images it holds, in the order of their times.
func (em *EditorMode) commandRows() (rows []listRow, labels []string) {
	for i, cmd := range em.commands {
		label := fmt.Sprintf("%s   %s", formatDuration(cmd.StartTime), libtrecs.CollapseCommand(cmd.InputText))
		if em.deletedCommands[i] {
			label = "(deleted) " + label
		}
		rows = append(rows, listRow{cmd: i})
		labels = append(labels, label)
		imgs := append([]libtrecs.Annotation(nil), cmd.Images...)
		sort.SliceStable(imgs, func(a, b int) bool { return imgs[a].Timestamp < imgs[b].Timestamp })
		for _, im := range imgs {
			label := fmt.Sprintf("%s   %s", formatDuration(im.Timestamp), tview.Escape(libtrecs.ImageCaption(imageLabelName(im))))
			if em.deletedCommands[i] {
				label = "(deleted) " + label
			}
			rows = append(rows, listRow{cmd: i, img: im.ID})
			labels = append(labels, label)
		}
	}
	return rows, labels
}

func imageLabelName(a libtrecs.Annotation) string {
	if a.ImageName != "" {
		return a.ImageName
	}
	return a.Image
}

// currentListRow is the row in focus in the list of commands.
func (em *EditorMode) currentListRow() (listRow, bool) {
	at := em.commandList.GetCurrentItem()
	if at < 0 || at >= len(em.listRows) {
		return listRow{}, false
	}
	return em.listRows[at], true
}

// currentListCommand is the command in focus in the list - the command of the row, which for an image is the one that holds it.
func (em *EditorMode) currentListCommand() int {
	if r, ok := em.currentListRow(); ok {
		return r.cmd
	}
	return -1
}

func (em *EditorMode) imageByID(id string) (libtrecs.Annotation, bool) {
	a, _, ok := libtrecs.FindImage(em.commands, id)
	return a, ok
}

// ---- the picture on show -----------------------------------------------------------------------------------------------------

// shownImage is the image that is on show, with what is needed to draw it. Its file is read once, when it is first shown.
type shownImage struct {
	ann  libtrecs.Annotation
	data []byte
	info libtrecs.ImageInfo
	err  string

	fit    *libtrecs.SixelImage // the picture scaled to the box it was last drawn in
	fitW   int
	fitH   int
	fitKey string
	rect   imageRect // where the painter is to draw it, set by Draw: cols == 0 when it is not drawn as a picture
}

type imageOverlay struct {
	*tview.Box
	em   *EditorMode
	show *shownImage
}

func newImageOverlay(em *EditorMode) *imageOverlay {
	return &imageOverlay{Box: tview.NewBox(), em: em}
}

// updateImage is called with the position of playback, by the progress clock: it shows the image whose time that is in, and takes it
// away when there is none.
func (em *EditorMode) updateImage(pos int64) {
	a, ok := libtrecs.ImageAt(libtrecs.RecordingImages(em.commands), pos)
	if !ok {
		if em.imageOverlay.show != nil {
			em.imageOverlay.show = nil
			em.playbackLayers.HidePage("image")
		}
		return
	}
	if cur := em.imageOverlay.show; cur != nil && cur.ann.ID == a.ID && cur.ann.Image == a.Image {
		cur.ann = a // its time or duration may have been changed
		return
	}
	s := &shownImage{ann: a}
	if p, err := libtrecs.ImageFilePath(em.filePath, a.Image); err != nil {
		s.err = err.Error()
	} else if data, info, err := libtrecs.ReadImageFile(p); err != nil {
		s.err = "the image file is not there: " + filepath.Join(libtrecs.ImagesDir, a.Image)
		if !os.IsNotExist(err) {
			s.err = err.Error()
		}
	} else {
		s.data, s.info = data, info
	}
	em.imageOverlay.show = s
	em.playbackLayers.ShowPage("image")
}

// imageCaption is the line under the picture.
func imageCaption(a libtrecs.Annotation) string {
	return fmt.Sprintf("%s    %s - %s (%d s)", libtrecs.ImageCaption(imageLabelName(a)), libtrecs.ClockText(a.Timestamp), libtrecs.ClockText(libtrecs.ImageEnd(a)), libtrecs.ImageDurationMs(a)/1000)
}

func (o *imageOverlay) Draw(screen tcell.Screen) {
	s := o.show
	if s == nil {
		return
	}
	s.rect = imageRect{}
	x, y, w, h := o.em.playbackView.GetRect() // the terminal's place: the picture covers that, and not the legend or the progress bar
	if w < 8 || h < 3 {
		return
	}
	bg := tcell.StyleDefault.Background(imageOverlayBackground)
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			screen.SetContent(xx, yy, ' ', nil, bg)
		}
	}
	tview.Print(screen, "[gray]"+tview.Escape(imageCaption(s.ann)), x, y+h-1, w, tview.AlignCenter, tcell.ColorGray)
	h-- // the caption's row

	reason := s.err
	if s.err == "" {
		if o.drawPicture(screen, s, x, y, w, h) {
			return
		}
		switch {
		case s.info.MIME == "image/webp":
			reason = "A WebP is shown in the web player; this editor cannot draw one."
		case !o.em.sixel.canDraw():
			reason = "This terminal does not show pictures (sixel); the web player does."
		default:
			reason = "Not shown here: the picture could not be drawn at this size."
		}
	}
	o.drawCard(screen, s, reason, x, y, w, h)
}

// drawPicture works out where the picture goes, if the terminal can show it, and leaves the cells for the painter. It reports whether it
// will be drawn.
func (o *imageOverlay) drawPicture(screen tcell.Screen, s *shownImage, x, y, w, h int) bool {
	em := o.em
	if !em.sixel.canDraw() || s.info.MIME == "image/webp" {
		return false
	}
	cw, ch := em.sixel.cellSize(screen)
	if cw <= 0 || ch <= 0 {
		return false
	}
	boxW, boxH := w*cw, h*ch
	tw, th := libtrecs.FitSize(s.info.W, s.info.H, boxW, boxH)
	key := fmt.Sprintf("image:%s:%dx%d", s.ann.Image, tw, th)
	if s.fit == nil || s.fitKey != key {
		fit, err := libtrecs.DecodeImageFit(s.data, boxW, boxH)
		if err != nil {
			s.fit = nil
			return false
		}
		s.fit, s.fitW, s.fitH, s.fitKey = fit, fit.W, fit.H, key
	}
	em.sixel.preload(key, s.fit)
	cols, rows := (s.fitW+cw-1)/cw, (s.fitH+ch-1)/ch
	cols, rows = min(cols, w), min(rows, h)
	s.rect = imageRect{
		dcs: key, pxW: s.fitW, pxH: s.fitH,
		col: x + (w-cols)/2, row: y + (h-rows)/2, cols: cols, rows: rows,
		recCellW: cw, recCellH: ch, cellW: cw, cellH: ch,
	}
	return true
}

// drawCard is what is shown where the picture cannot be: its name, what the file is, when it is on show, and why it is not drawn.
func (o *imageOverlay) drawCard(screen tcell.Screen, s *shownImage, reason string, x, y, w, h int) {
	lines := []string{"[::b]" + tview.Escape(libtrecs.ImageCaption(imageLabelName(s.ann))) + "[::-]"}
	if s.err == "" {
		lines = append(lines, fmt.Sprintf("%s  %dx%d  %s", strings.ToUpper(strings.TrimPrefix(s.info.MIME, "image/")), s.info.W, s.info.H, libtrecs.ByteSize(s.info.Size)))
	}
	lines = append(lines, fmt.Sprintf("%s - %s  (%d s)", libtrecs.ClockText(s.ann.Timestamp), libtrecs.ClockText(libtrecs.ImageEnd(s.ann)), libtrecs.ImageDurationMs(s.ann)/1000), "")
	lines = append(lines, wrapCueLines("[gray]"+tview.Escape(reason), max(10, min(w-8, 60)))...)
	cardW := 0
	for _, l := range lines {
		cardW = max(cardW, tview.TaggedStringWidth(l))
	}
	cardW = min(cardW+6, w)
	cardH := min(len(lines)+2, h)
	left, top := x+(w-cardW)/2, y+(h-cardH)/2
	drawRoundedBox(screen, left, top, cardW, cardH, timedBorder)
	for i, l := range lines {
		if 1+i >= cardH-1 {
			break
		}
		tview.Print(screen, cueTextTag+l, left+1, top+1+i, cardW-2, tview.AlignCenter, tcell.ColorWhite)
	}
}

// shownImageRect is where the picture on show is to be painted, if it is a picture and nothing is laid over it.
func (em *EditorMode) shownImageRect() (imageRect, bool) {
	if em.imageOverlay == nil || em.imageOverlay.show == nil || em.imageOverlay.show.rect.cols == 0 || !em.playbackInFront() {
		return imageRect{}, false
	}
	return em.imageOverlay.show.rect, true
}

// ---- the modal -------------------------------------------------------------------------------------------------------------------

type chosenFile struct {
	data []byte
	info libtrecs.ImageInfo
}

type imageModal struct {
	frame tview.Primitive
	box   *tview.Flex

	time, file, duration *tview.InputField
	info, message        *tview.TextView
	browse, save, del    *tview.Button
	discard              *tview.Button
	buttons              *tview.Flex
	order                []tview.Primitive

	editID       string               // the id of the image being edited: "" when one is being added
	keep         *libtrecs.Annotation // and as it is: its file stands while the file field is as it was
	keepText     string               // what the field was
	origTs       int64                // and its time, to the millisecond: it stays so while the time field is as it was
	origTimeText string               //
	chosen       *chosenFile          // the file in the field, when it has been read
	chosenFor    string               // and the text of the field it was read for
	wasPlaying   bool                 // playback was going on when it opened, and goes on if it is discarded
	picker       *imagePicker
}

func (em *EditorMode) createImageModal() {
	m := &imageModal{}
	em.imageModal = m

	numbers := func(f *tview.InputField, title string) {
		f.SetFieldBackgroundColor(tcell.ColorBlack).SetFieldTextColor(tcell.ColorWhite)
		f.SetBorder(true).SetTitle(title)
	}
	m.time = tview.NewInputField()
	m.time.SetAcceptanceFunc(func(text string, ch rune) bool { return (ch >= '0' && ch <= '9' || ch == ':') && len(text) <= 8 })
	numbers(m.time, " Show at (m:ss or h:mm:ss) ")

	m.file = tview.NewInputField()
	numbers(m.file, " Image file (PNG, JPEG, GIF or WebP, up to 2 MB) - ^O to browse ")
	m.file.SetChangedFunc(func(string) { em.imageFileEdited() })

	m.duration = tview.NewInputField()
	m.duration.SetAcceptanceFunc(tview.InputFieldInteger)
	numbers(m.duration, " Show for (seconds) ")

	m.info = tview.NewTextView().SetDynamicColors(true)
	m.info.SetBackgroundColor(timedModalBackground)
	m.message = tview.NewTextView().SetDynamicColors(true)
	m.message.SetBackgroundColor(timedModalBackground)

	button := func(label string, do func()) *tview.Button {
		b := tview.NewButton(label)
		b.SetStyle(tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite))
		b.SetActivatedStyle(tcell.StyleDefault.Background(timedModalMark).Foreground(tcell.ColorBlack))
		b.SetSelectedFunc(do)
		return b
	}
	m.browse = button("Browse  ^O", em.openImagePicker)
	m.save = button("Save  ^S", em.saveImage)
	m.del = button("Delete image  ^D", em.deleteImageFromModal)
	m.discard = button("Discard  Esc", em.closeImageModal)
	m.buttons = opaque(tview.NewFlex().SetDirection(tview.FlexColumn), false)
	m.buttons.SetBackgroundColor(timedModalBackground)
	em.layoutImageButtons(false)

	m.box = opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).
		AddItem(m.time, 3, 0, true).
		AddItem(m.file, 3, 0, false).
		AddItem(m.info, 2, 0, false).
		AddItem(m.duration, 3, 0, false).
		AddItem(m.message, 2, 0, false).
		AddItem(m.buttons, 1, 0, false)
	m.box.SetBorder(true).SetTitle(" Add image ").SetBorderPadding(0, 0, 1, 1)
	m.box.SetBackgroundColor(timedModalBackground)

	m.order = []tview.Primitive{m.time, m.file, m.duration, m.browse, m.save, m.del, m.discard}

	m.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(m.box, 76, 0, true).AddItem(nil, 0, 1, false), 16, 0, true).
		AddItem(nil, 0, 1, false)
}

func (em *EditorMode) layoutImageButtons(withDelete bool) {
	m := em.imageModal
	m.buttons.Clear()
	m.buttons.AddItem(nil, 0, 1, false).AddItem(m.browse, 13, 0, false).AddItem(nil, 1, 0, false).AddItem(m.save, 10, 0, false)
	if withDelete {
		m.buttons.AddItem(nil, 1, 0, false).AddItem(m.del, 18, 0, false)
	}
	m.buttons.AddItem(nil, 1, 0, false).AddItem(m.discard, 14, 0, false).AddItem(nil, 0, 1, false)
}

// openImageEntry is what Ctrl+G does: adds an image at the time playback is at - or, if one is on show, edits that one.
func (em *EditorMode) openImageEntry() {
	if len(em.commands) == 0 {
		em.setStatus("There are no commands to put an image with.")
		return
	}
	if s := em.imageOverlay.show; s != nil {
		if a, ok := em.imageByID(s.ann.ID); ok {
			em.setStatus("Editing the image that is on show.")
			em.openImageModalFor(a)
			return
		}
	}
	em.openImageModalAt(em.player.Position(), nil)
}

// openImageModalFor opens the modal on an image that is there already.
func (em *EditorMode) openImageModalFor(a libtrecs.Annotation) { em.openImageModalAt(a.Timestamp, &a) }

func (em *EditorMode) openImageModalAt(posMs int64, existing *libtrecs.Annotation) {
	m := em.imageModal
	m.wasPlaying = em.player.GetPlaybackState() == libtrecs.PlaybackPlaying
	if m.wasPlaying {
		em.player.Pause()
	}
	if name, _ := em.pages.GetFrontPage(); name != "playback" {
		em.cancelAnnotation()
		em.pages.SwitchToPage("playback") // from the list of commands: the modal is over the playback
	}
	for _, p := range []string{imageModalPage, imagePickerPage} {
		if em.pages.HasPage(p) {
			em.pages.RemovePage(p)
		}
	}
	m.chosen, m.chosenFor, m.keep, m.editID = nil, "", nil, ""
	m.message.SetText("")
	m.info.SetText("")
	if existing == nil {
		m.box.SetTitle(" Add image ")
		m.time.SetText(libtrecs.ClockText(posMs))
		m.origTimeText, m.origTs = "", 0
		m.file.SetText("")
		m.duration.SetText(strconv.Itoa(libtrecs.ImageDefaultSeconds))
		em.layoutImageButtons(false)
		em.pages.AddPage(imageModalPage, m.frame, true, true)
		em.app.SetFocus(m.file)
		return
	}
	keep := *existing
	m.keep, m.editID = &keep, keep.ID
	m.box.SetTitle(" Edit image ")
	m.origTs, m.origTimeText = keep.Timestamp, libtrecs.ClockText(keep.Timestamp)
	m.time.SetText(m.origTimeText)
	m.keepText = imageLabelName(keep)
	m.file.SetText(m.keepText)
	m.duration.SetText(strconv.Itoa(int(libtrecs.ImageDurationMs(keep) / 1000)))
	em.layoutImageButtons(true)
	em.imageFileEdited()
	em.pages.AddPage(imageModalPage, m.frame, true, true)
	em.app.SetFocus(m.file)
}

func (em *EditorMode) closeImageModal() {
	for _, p := range []string{imageModalPage, imagePickerPage} {
		if em.pages.HasPage(p) {
			em.pages.RemovePage(p)
		}
	}
	em.app.SetFocus(em.playbackView)
	if em.imageModal.wasPlaying {
		em.imageModal.wasPlaying = false
		em.player.Resume()
	}
}

func (em *EditorMode) imageError(msg string, focus tview.Primitive) {
	em.imageModal.message.SetText("[red]" + tview.Escape(msg))
	if focus != nil {
		em.app.SetFocus(focus)
	}
}

// expandPath reads a path that was typed: ~ is the home directory, and a relative path is relative to where the editor was started.
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// imageFileEdited is called when the file field changes: what was known of the file in it no longer is, until it is read again.
func (em *EditorMode) imageFileEdited() {
	m := em.imageModal
	text := m.file.GetText()
	if m.keep != nil && text == m.keepText {
		a := *m.keep
		m.info.SetText(fmt.Sprintf("[gray]as it is now: %s", tview.Escape(imageLabelName(a))))
		return
	}
	if m.chosen != nil && m.chosenFor != text {
		m.chosen = nil
	}
	m.info.SetText("")
}

// checkImageFile reads the file in the field, and says what it is, or why it is not taken. It reports whether the field is good.
func (em *EditorMode) checkImageFile() bool {
	m := em.imageModal
	text := m.file.GetText()
	if m.keep != nil && text == m.keepText {
		return true
	}
	if m.chosen != nil && m.chosenFor == text {
		return true
	}
	path := expandPath(text)
	if path == "" {
		m.info.SetText("[red]Choose an image file.")
		return false
	}
	data, info, err := libtrecs.ReadImageFile(path)
	if err != nil {
		m.chosen = nil
		m.info.SetText("[red]" + tview.Escape(fileErrorText(path, err)))
		return false
	}
	m.chosen, m.chosenFor = &chosenFile{data: data, info: info}, text
	m.info.SetText(fmt.Sprintf("[green]%s  %s %dx%d  %s", tview.Escape(info.Name), strings.ToUpper(strings.TrimPrefix(info.MIME, "image/")), info.W, info.H, libtrecs.ByteSize(info.Size)))
	return true
}

// fileErrorText says why a file cannot be used, short enough for the line that shows it: the file's name and the reason, without the
// operation or the directory the system puts in front.
func fileErrorText(path string, err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		reason := pe.Err.Error()
		if os.IsNotExist(pe) {
			reason = "no such file or directory"
		}
		return filepath.Base(path) + ": " + reason
	}
	return err.Error()
}

// saveImage adds the image (or changes it), keeps its file beside the recording, and writes the recording.
func (em *EditorMode) saveImage() {
	m := em.imageModal
	if em.audioBusy.Load() {
		em.imageError("The sound of the last change is still being made: wait for it.", nil)
		return
	}
	if len(em.deletedCommands) > 0 { // the sound would be changed for a recording that is not the one it goes with
		em.imageError("Commands are deleted and not saved yet: save (Ctrl+W) before changing an image.", nil)
		return
	}
	var ms int64
	if timeText := m.time.GetText(); m.editID != "" && timeText == m.origTimeText {
		ms = m.origTs
	} else {
		var err error
		if ms, err = parseClockText(timeText); err != nil {
			em.imageError("Time: "+err.Error()+".", m.time)
			return
		}
	}
	if !em.checkImageFile() {
		em.imageError("Choose an image file that can be used.", m.file)
		return
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(m.duration.GetText()))
	if err != nil || seconds < 1 || seconds > libtrecs.ImageMaxSeconds {
		em.imageError(fmt.Sprintf("The duration is a whole number of seconds, from 1 to %d.", libtrecs.ImageMaxSeconds), m.duration)
		return
	}

	img := libtrecs.Annotation{Timestamp: ms, DurationSeconds: seconds}
	if m.chosen != nil && (m.keep == nil || m.file.GetText() != m.keepText) {
		img.Image, img.ImageName = m.chosen.info.Stored, m.chosen.info.Name
	} else {
		img.Image, img.ImageName = m.keep.Image, m.keep.ImageName
	}

	var (
		out   []libtrecs.Command
		audio []libtrecs.AudioEdit
		done  string
	)
	if m.editID == "" {
		out, err = libtrecs.AddImage(em.commands, img)
		audio = []libtrecs.AudioEdit{{At: ms, Dur: int64(seconds) * 1000}}
		done = fmt.Sprintf("Image added at %s for %d s.", libtrecs.ClockText(ms), seconds)
	} else {
		old, _ := em.imageByID(m.editID)
		out, err = libtrecs.ReplaceImage(em.commands, m.editID, img)
		if err == nil {
			now, _, _ := libtrecs.FindImage(out, m.editID)
			audio = libtrecs.AudioEditsForChange(old, now)
			ms = now.Timestamp
		}
		done = fmt.Sprintf("Image changed: %s for %d s.", libtrecs.ClockText(ms), seconds)
	}
	if err != nil {
		em.imageError(err.Error(), m.time)
		return
	}
	if m.chosen != nil && m.chosen.info.Stored == img.Image {
		if err := libtrecs.SaveImage(em.filePath, m.chosen.data, m.chosen.info); err != nil {
			em.imageError("The image could not be kept beside the recording: "+err.Error(), nil)
			return
		}
	}
	host := 0
	for i := range out {
		for _, a := range out[i].Images {
			if (m.editID != "" && a.ID == m.editID) || (m.editID == "" && a.Image == img.Image && a.Timestamp == img.Timestamp) {
				host = i
			}
		}
	}
	em.commitImageChange(out, host, ms, done, audio, true)
}

// commitImageChange makes the new commands the recording's, writes it, and then does what the sound needs. If the recording cannot be
// written everything stays as it was, and the modal says why.
func (em *EditorMode) commitImageChange(out []libtrecs.Command, host int, ts int64, done string, audio []libtrecs.AudioEdit, show bool) {
	prev := em.commands
	em.commands = out
	em.editedCommands[host] = true
	backup, err := em.writeRecording(host)
	if err != nil {
		em.commands = prev
		if em.pages.HasPage(imageModalPage) {
			em.imageError("Not saved: "+err.Error(), nil)
		} else {
			em.setStatus("Not saved: " + err.Error())
		}
		return
	}
	for _, p := range []string{imageModalPage, imagePickerPage} {
		if em.pages.HasPage(p) {
			em.pages.RemovePage(p)
		}
	}
	em.imageModal.wasPlaying = false
	if show {
		em.jumpToTime(ts) // to see it
	}
	em.app.SetFocus(em.playbackView)
	em.changeSound(done+" Recording saved; backup: "+backup, audio)
}

// deleteImageFromModal deletes the image the modal is editing.
func (em *EditorMode) deleteImageFromModal() {
	if id := em.imageModal.editID; id != "" {
		em.deleteImage(id)
	}
}

// deleteImage takes an image out, and the time it took, and writes the recording.
func (em *EditorMode) deleteImage(id string) {
	if em.audioBusy.Load() {
		em.setStatus("The sound of the last change is still being made: wait for it.")
		return
	}
	if len(em.deletedCommands) > 0 {
		em.setStatus("Commands are deleted and not saved yet: save (Ctrl+W) before deleting an image.")
		return
	}
	old, host, ok := libtrecs.FindImage(em.commands, id)
	if !ok {
		return
	}
	out, _, err := libtrecs.RemoveImage(em.commands, id)
	if err != nil {
		em.setStatus(err.Error())
		return
	}
	wasPlaying := em.player.GetPlaybackState() == libtrecs.PlaybackPlaying
	if wasPlaying {
		em.player.Pause()
	}
	em.commitImageChange(out, host, 0, fmt.Sprintf("Image %s (at %s) deleted.", imageLabelName(old), libtrecs.ClockText(old.Timestamp)),
		[]libtrecs.AudioEdit{{Remove: true, At: old.Timestamp, Dur: libtrecs.ImageDurationMs(old)}}, false)
	if !em.pages.HasPage(imageModalPage) {
		em.app.SetFocus(em.playbackView)
	}
}

// handleImageModalInput is handleInput's for the modal's page.
func (em *EditorMode) handleImageModalInput(event *tcell.EventKey) *tcell.EventKey {
	m := em.imageModal
	switch event.Key() {
	case tcell.KeyCtrlS:
		em.saveImage()
		return nil
	case tcell.KeyEscape:
		em.closeImageModal()
		return nil
	case tcell.KeyCtrlO:
		em.openImagePicker()
		return nil
	case tcell.KeyCtrlD:
		if m.editID != "" {
			em.deleteImageFromModal()
		}
		return nil
	case tcell.KeyTab, tcell.KeyBacktab:
		if em.app.GetFocus() == tview.Primitive(m.file) {
			em.checkImageFile() // it is looked at when it is left
		}
		em.cycleImageFocus(event.Key() == tcell.KeyBacktab)
		return nil
	case tcell.KeyEnter:
		if em.app.GetFocus() == tview.Primitive(m.file) {
			em.checkImageFile()
			em.app.SetFocus(m.duration)
			return nil
		}
	}
	return event
}

func (em *EditorMode) cycleImageFocus(back bool) {
	m := em.imageModal
	cur := em.app.GetFocus()
	at := 1 // the file
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
		if next == tview.Primitive(m.del) && m.editID == "" {
			continue
		}
		em.app.SetFocus(next)
		return
	}
}

// ---- the file picker ---------------------------------------------------------------------------------------------------------------

type pickerEntry struct {
	name string
	dir  bool
	size int64
}

type imagePicker struct {
	frame      tview.Primitive
	box        *tview.Flex
	list       *tview.List
	where      *tview.TextView // the directory
	dir        string
	entries    []pickerEntry
	showHidden bool
}

var pickerExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

func (em *EditorMode) createImagePicker() {
	p := &imagePicker{}
	em.imageModal.picker = p
	p.list = tview.NewList().ShowSecondaryText(false)
	p.list.SetHighlightFullLine(true)
	p.list.SetBackgroundColor(tcell.ColorBlack)
	hint := tview.NewTextView().SetDynamicColors(true).SetText("[yellow]Enter[white] open/choose   [yellow]Backspace[white] up   [yellow].[white] hidden   [yellow]~[white] home   [yellow]Esc[white] back")
	hint.SetBackgroundColor(timedModalBackground)
	p.where = tview.NewTextView().SetDynamicColors(true)
	p.where.SetBackgroundColor(timedModalBackground)
	p.box = opaque(tview.NewFlex(), true).SetDirection(tview.FlexRow).AddItem(p.where, 1, 0, false).AddItem(p.list, 0, 1, true).AddItem(hint, 1, 0, false)
	p.box.SetBorder(true).SetTitle(" Choose an image ").SetBorderPadding(0, 0, 1, 1)
	p.box.SetBackgroundColor(timedModalBackground)
	p.frame = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(p.box, 76, 0, true).AddItem(nil, 0, 1, false), 20, 0, true).
		AddItem(nil, 0, 1, false)
}

// openImagePicker opens the picker over the modal, in the directory of the file in the field if there is one, else where the last one
// was chosen from, else where the editor was started.
func (em *EditorMode) openImagePicker() {
	m := em.imageModal
	if m.picker == nil {
		em.createImagePicker()
	}
	p := m.picker
	dir := ""
	if text := expandPath(m.file.GetText()); text != "" && (m.keep == nil || m.file.GetText() != m.keepText) {
		if fi, err := os.Stat(text); err == nil && fi.IsDir() {
			dir = text
		} else if fi, err := os.Stat(filepath.Dir(text)); err == nil && fi.IsDir() {
			dir = filepath.Dir(text)
		}
	}
	if dir == "" {
		dir = em.lastImageDir
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	em.pickerGo(dir)
	if em.pages.HasPage(imagePickerPage) {
		em.pages.RemovePage(imagePickerPage)
	}
	em.pages.AddPage(imagePickerPage, p.frame, true, true)
	em.app.SetFocus(p.list)
}

// pickerGo lists a directory: the way up, the directories, and the images in it.
func (em *EditorMode) pickerGo(dir string) {
	p := em.imageModal.picker
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	des, err := os.ReadDir(abs)
	if err != nil {
		em.imageModal.message.SetText("[red]" + tview.Escape(err.Error()))
		return
	}
	em.imageModal.message.SetText("")
	p.dir = abs
	p.entries = p.entries[:0]
	for _, de := range des {
		name := de.Name()
		if !p.showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		isDir := de.IsDir()
		if info.Mode()&os.ModeSymlink != 0 { // a link to a directory is one
			if st, err := os.Stat(filepath.Join(abs, name)); err == nil {
				info, isDir = st, st.IsDir()
			}
		}
		if !isDir && (!pickerExt[strings.ToLower(filepath.Ext(name))] || !info.Mode().IsRegular()) {
			continue
		}
		p.entries = append(p.entries, pickerEntry{name: name, dir: isDir, size: info.Size()})
	}
	sort.SliceStable(p.entries, func(a, b int) bool {
		x, y := p.entries[a], p.entries[b]
		if x.dir != y.dir {
			return x.dir
		}
		return strings.ToLower(x.name) < strings.ToLower(y.name)
	})
	shown := abs
	if r := []rune(shown); len(r) > 70 { // the end of a long path is what tells where
		shown = "…" + string(r[len(r)-69:])
	}
	p.where.SetText("[yellow]" + tview.Escape(shown))
	p.list.Clear()
	if filepath.Dir(abs) != abs {
		p.list.AddItem("../", "", 0, nil)
	}
	for _, e := range p.entries {
		switch {
		case e.dir:
			p.list.AddItem(tview.Escape(e.name)+"/", "", 0, nil)
		case e.size > libtrecs.MaxImageBytes:
			p.list.AddItem(fmt.Sprintf("%s   [red]%s: too large[-]", tview.Escape(e.name), libtrecs.ByteSize(int(e.size))), "", 0, nil)
		default:
			p.list.AddItem(fmt.Sprintf("%s   [gray]%s[-]", tview.Escape(e.name), libtrecs.ByteSize(int(e.size))), "", 0, nil)
		}
	}
	p.list.SetCurrentItem(0)
	if len(p.entries) == 0 {
		p.list.AddItem("[gray](no images here)", "", 0, nil)
	}
}

// pickerEntryAt is the entry for a row of the list: nil for the way up (up is set), and for a row that is no entry.
func (p *imagePicker) entryAt(row int) (e *pickerEntry, up bool) {
	if filepath.Dir(p.dir) != p.dir {
		if row == 0 {
			return nil, true
		}
		row--
	}
	if row < 0 || row >= len(p.entries) {
		return nil, false
	}
	return &p.entries[row], false
}

func (em *EditorMode) pickerChoose() {
	p := em.imageModal.picker
	e, up := p.entryAt(p.list.GetCurrentItem())
	switch {
	case up:
		prev := filepath.Base(p.dir)
		em.pickerGo(filepath.Dir(p.dir))
		em.pickerSelect(prev + "/")
	case e == nil:
	case e.dir:
		em.pickerGo(filepath.Join(p.dir, e.name))
	default:
		path := filepath.Join(p.dir, e.name)
		em.lastImageDir = p.dir
		em.closeImagePicker()
		m := em.imageModal
		m.chosen = nil
		m.file.SetText(path)
		em.checkImageFile()
		em.app.SetFocus(m.duration)
	}
}

// pickerSelect puts the cursor on the row with a name (the directory just left, when going up).
func (em *EditorMode) pickerSelect(label string) {
	p := em.imageModal.picker
	for i := 0; i < p.list.GetItemCount(); i++ {
		if main, _ := p.list.GetItemText(i); main == tview.Escape(strings.TrimSuffix(label, "/"))+"/" {
			p.list.SetCurrentItem(i)
			return
		}
	}
}

func (em *EditorMode) closeImagePicker() {
	if em.pages.HasPage(imagePickerPage) {
		em.pages.RemovePage(imagePickerPage)
	}
	em.app.SetFocus(em.imageModal.file)
}

// handleImagePickerInput is handleInput's for the picker's page.
func (em *EditorMode) handleImagePickerInput(event *tcell.EventKey) *tcell.EventKey {
	p := em.imageModal.picker
	switch event.Key() {
	case tcell.KeyEscape:
		em.closeImagePicker()
		return nil
	case tcell.KeyEnter, tcell.KeyRight:
		if event.Key() == tcell.KeyRight {
			if e, _ := p.entryAt(p.list.GetCurrentItem()); e == nil || !e.dir {
				return nil
			}
		}
		em.pickerChoose()
		return nil
	case tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyLeft:
		if filepath.Dir(p.dir) != p.dir {
			prev := filepath.Base(p.dir)
			em.pickerGo(filepath.Dir(p.dir))
			em.pickerSelect(prev)
		}
		return nil
	case tcell.KeyRune:
		switch r := event.Rune(); r {
		case '.':
			p.showHidden = !p.showHidden
			em.pickerGo(p.dir)
			return nil
		case '~':
			if home, err := os.UserHomeDir(); err == nil {
				em.pickerGo(home)
			}
			return nil
		default:
			// a letter goes to the next name that begins with it
			n := p.list.GetItemCount()
			for i := 1; i <= n; i++ {
				at := (p.list.GetCurrentItem() + i) % n
				if main, _ := p.list.GetItemText(at); strings.HasPrefix(strings.ToLower(tview.Unescape(main)), strings.ToLower(string(r))) {
					p.list.SetCurrentItem(at)
					break
				}
			}
			return nil
		}
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
		return event
	}
	return nil
}

// ---- the list of commands -------------------------------------------------------------------------------------------------------

// jumpToListRow is Ctrl+P in the list: playback from the command or the image in focus.
func (em *EditorMode) jumpToListRow(r listRow) {
	if r.img == "" {
		em.cancelAnnotation()
		em.jumpToCommandForPlayback(r.cmd)
		return
	}
	if a, ok := em.imageByID(r.img); ok {
		em.cancelAnnotation()
		em.jumpToTime(a.Timestamp)
	}
}

// editListRow is Ctrl+E in the list.
func (em *EditorMode) editListRow(r listRow) {
	if r.img == "" {
		em.jumpToCommandForEdit(r.cmd)
		return
	}
	if a, ok := em.imageByID(r.img); ok {
		em.openImageModalFor(a)
	}
}

// deleteListRow is Ctrl+D in the list: an image goes; a command is deleted where it is edited (Ctrl+E, then Ctrl+D).
func (em *EditorMode) deleteListRow(r listRow) {
	if r.img == "" {
		em.setStatus("A command is deleted in the editor: Ctrl+E, then Ctrl+D. Ctrl+D here deletes an image.")
		return
	}
	em.pages.SwitchToPage("playback")
	em.deleteImage(r.img)
}
