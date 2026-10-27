package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	libtrecs "trecs/lib"
)

// ---- the legend ---------------------------------------------------------------------------------------------------------

func TestTheLegendKeepsTheItemsThatFitAndAlwaysTheHelp(t *testing.T) {
	items := playbackLegendItems()
	wide, help := fitLegend(items, "?", 300)
	if !help || len(wide) != len(items) {
		t.Fatalf("room for all: %d of %d, help %v", len(wide), len(items), help)
	}
	for _, width := range []int{120, 100, 80, 60, 45, 36, 30, 20, 16} {
		shown, help := fitLegend(items, "?", width)
		if !help {
			t.Errorf("width %d: no help", width)
		}
		text := legendText(items, "?", width)
		if w := tviewWidth(text); w > width {
			t.Errorf("width %d: the legend is %d wide: %q", width, w, text)
		}
		if !strings.HasSuffix(text, "[yellow]?[white] help") {
			t.Errorf("width %d: ends with %q", width, text[max(0, len(text)-40):])
		}
		// what is shown is in the order it was given
		last := -1
		for _, it := range shown {
			at := -1
			for i, o := range items {
				if o.key == it.key {
					at = i
				}
			}
			if at < last {
				t.Errorf("width %d: out of order", width)
			}
			last = at
		}
	}
	// a narrower window shows fewer, and the ones that go are the least used: Pause/Resume is the last to go
	prev := len(items) + 1
	for _, width := range []int{300, 120, 90, 70, 50, 40, 30} {
		shown, _ := fitLegend(items, "?", width)
		if len(shown) > prev {
			t.Errorf("width %d shows more (%d) than a wider one (%d)", width, len(shown), prev)
		}
		prev = len(shown)
	}
	if shown, _ := fitLegend(items, "?", 6+3+20); len(shown) != 1 || shown[0].key != "Space/P" {
		t.Errorf("room for one: %+v", shown)
	}
	if shown, help := fitLegend(items, "?", 5); len(shown) != 0 || help {
		t.Errorf("no room for the help: %+v %v", shown, help)
	}
	// an item that does not fit does not hold back a shorter one after it
	two := []legendItem{{"A", "short", 1}, {"B", strings.Repeat("long", 10), 2}, {"C", "ok", 3}}
	if shown, _ := fitLegend(two, "?", 6+3+7+3+4); len(shown) != 2 || shown[1].key != "C" {
		t.Errorf("%+v", shown)
	}
}

func tviewWidth(tagged string) int {
	n, in := 0, false
	for _, r := range tagged {
		switch {
		case r == '[':
			in = true
		case r == ']' && in:
			in = false
		case !in:
			n++
		}
	}
	return n
}

func TestTheLegendOnTheScreenFitsTheWidthAndSaysHelp(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	first := func() string { return strings.TrimRight(strings.Split(h.screen(), "\n")[0], " ") }
	for _, want := range []string{"Space/P Pause/Resume", "^A Timed Annotations", "^G Add Image", "^L Browse Commands", "? help"} {
		if !strings.Contains(first(), want) {
			t.Errorf("%q is not in the legend at 120 columns: %q", want, first())
		}
	}
	h.sim.SetSize(70, 40)
	h.waitFor("the legend at 70", func() bool {
		f := first()
		return strings.Contains(f, "? help") && !strings.Contains(f, "Timed Annotations")
	}, 3*time.Second)
	if f := first(); len([]rune(f)) > 70 || !strings.Contains(f, "Space/P Pause/Resume") {
		t.Errorf("the legend at 70 columns: %q", f)
	}
	h.sim.SetSize(160, 40)
	h.waitFor("the legend at 160", func() bool {
		return strings.Contains(first(), "Q Quit") && strings.Contains(first(), "? help")
	}, 3*time.Second)
}

func TestTheFullHelpMenuListsEveryKeyOfThePageAndEscClosesIt(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	h.typeText("?")
	h.waitFor("the help", func() bool { return h.front() == helpPage }, 2*time.Second)
	for _, want := range []string{"Playback - keys", "Add an image at this time", "Browse the commands and images", "Quit"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("%q is not in the help:\n%s", want, h.screen())
		}
	}
	h.key(tcell.KeyCtrlL) // nothing reaches past it
	time.Sleep(150 * time.Millisecond)
	if h.front() != helpPage {
		t.Error("a key got past the help")
	}
	h.key(tcell.KeyEscape)
	h.waitFor("the help to close", func() bool { return h.front() == "playback" }, 2*time.Second)

	h.key(tcell.KeyCtrlL)
	h.waitFor("the list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	h.typeText("?")
	h.waitFor("the help of the list", func() bool { return h.front() == helpPage }, 2*time.Second)
	h.waitText("Delete the image in focus", 2*time.Second)
	h.typeText("?") // the key that opened it closes it
	h.waitFor("back to the list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	h.key(tcell.KeyEscape)
	h.waitFor("back to the playback", func() bool { return h.front() == "playback" }, 2*time.Second)

	// on the editing page ? is a letter to be typed: F1 is the key
	h.key(tcell.KeyCtrlE)
	h.waitFor("the edit page", func() bool { return h.front() == "edit" }, 2*time.Second)
	if !strings.Contains(strings.Split(h.screen(), "\n")[0], "F1 help") {
		t.Errorf("the edit legend: %q", strings.Split(h.screen(), "\n")[0])
	}
	h.typeText("?")
	time.Sleep(100 * time.Millisecond)
	if h.front() != "edit" {
		t.Error("? opened the help where it is typed")
	}
	h.key(tcell.KeyF1)
	h.waitFor("the help of the edit page", func() bool { return h.front() == helpPage }, 2*time.Second)
	h.waitText("Editing a command - keys", 2*time.Second)
	h.key(tcell.KeyEscape)
	h.waitFor("back to the edit page", func() bool { return h.front() == "edit" }, 2*time.Second)
}

// ---- driving images -----------------------------------------------------------------------------------------------------------

func writePNG(t *testing.T, dir, name string, w, hgt int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, hgt))
	for x := 0; x < w; x++ {
		for y := 0; y < hgt; y++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *harness) imageModalUp() bool { return h.front() == imageModalPage }

func (h *harness) imageFields(file, at, secs string) {
	h.onUI(func() {
		m := h.em.imageModal
		if at != "" {
			m.time.SetText(at)
		}
		if file != "" {
			m.file.SetText(file)
		}
		if secs != "" {
			m.duration.SetText(secs)
		}
	})
}

// imagesOnDisk is the images of the recording as it is on disk now, with the command that holds each.
func (h *harness) imagesOnDisk() (all []libtrecs.Annotation, holder map[string]int) {
	h.t.Helper()
	holder = map[string]int{}
	for i, c := range h.reparsed() {
		for _, a := range c.Images {
			all = append(all, a)
			holder[a.ID] = i
		}
	}
	return
}

func (h *harness) path() (p string) {
	h.onUI(func() { p = h.em.filePath })
	return
}

func frameTimes(cmds []libtrecs.Command) []int64 {
	var ts []int64
	for _, c := range cmds {
		if c.HasPrompt {
			ts = append(ts, c.PromptFrame.Timestamp)
		}
		for _, f := range c.InputFrames {
			ts = append(ts, f.Timestamp)
		}
		for _, f := range c.OutputFrames {
			ts = append(ts, f.Timestamp)
		}
	}
	return ts
}

// addImage adds an image at a second, through the modal.
func (h *harness) addImage(file, at, secs string) {
	h.t.Helper()
	h.pausePlayback()
	h.key(imageKey)
	h.waitFor("the image modal", h.imageModalUp, 2*time.Second)
	h.imageFields(file, at, secs)
	h.key(tcell.KeyCtrlS)
	h.waitFor("the image to be written", func() bool { return strings.Contains(h.status(), "Image added") && h.front() == "playback" }, 6*time.Second)
}

func TestCtrlGOpensTheAddImageModalAtTheCurrentTimeAndPausesPlayback(t *testing.T) {
	h := newStretched(t, nil)
	h.waitFor("playback to be past a second and a half", func() bool { return h.position() > 1500 }, 5*time.Second)
	if !strings.Contains(strings.Split(h.screen(), "\n")[0], "^G Add Image") {
		t.Errorf("the legend: %q", strings.Split(h.screen(), "\n")[0])
	}
	h.key(imageKey)
	h.waitFor("the modal", h.imageModalUp, 2*time.Second)
	for _, want := range []string{"Add image", "Show at (m:ss or h:mm:ss)", "Image file (PNG, JPEG, GIF or WebP, up to 2 MB)", "Show for (seconds)", "Browse  ^O", "Save  ^S", "Discard  Esc"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("%q is not in the modal:\n%s", want, h.screen())
		}
	}
	if strings.Contains(h.screen(), "Delete image") {
		t.Error("a Delete button where there is nothing to delete")
	}
	if h.playbackState() != libtrecs.PlaybackPaused {
		t.Error("playback goes on behind the modal")
	}
	var at, dur string
	var pos int64
	h.onUI(func() {
		at, dur, pos = h.em.imageModal.time.GetText(), h.em.imageModal.duration.GetText(), h.em.player.Position()
	})
	if at != libtrecs.ClockText(pos) || dur != "5" {
		t.Errorf("time %q (playback is at %d), duration %q", at, pos, dur)
	}
	h.key(tcell.KeyEscape)
	h.waitFor("the modal to close and playback to go on", func() bool { return h.front() == "playback" && h.playbackState() == libtrecs.PlaybackPlaying }, 2*time.Second)
}

func TestAddingAnImageMakesRoomWritesTheFileAndShowsIt(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	before := h.reparsed()
	pic := writePNG(t, t.TempDir(), "My Shot.png", 40, 20)

	h.addImage(pic, "00:03", "4")

	// on disk: the image, with the name without its directory, the file beside the recording, and everything after it later by 4 s
	imgs, holder := h.imagesOnDisk()
	if len(imgs) != 1 {
		t.Fatalf("%d images on disk", len(imgs))
	}
	im := imgs[0]
	if im.Timestamp != 3000 || im.DurationSeconds != 4 || im.ImageName != "My Shot.png" || im.Text != "display image: My Shot.png" || !libtrecs.ValidStoredImageName(im.Image) {
		t.Errorf("%+v", im)
	}
	if strings.Contains(im.ImageName, string(filepath.Separator)) || strings.Contains(im.Text, filepath.Dir(pic)) {
		t.Error("the directory is in the recording")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.path()), libtrecs.ImagesDir, im.Image)); err != nil {
		t.Errorf("the file is not beside the recording: %v", err)
	}
	after := h.reparsed()
	bt, at := frameTimes(before), frameTimes(after)
	if len(bt) != len(at) {
		t.Fatalf("%d frames, were %d", len(at), len(bt))
	}
	for i := range bt {
		want := bt[i]
		if want >= 3000 {
			want += 4000
		}
		if at[i] != want {
			t.Fatalf("frame %d is at %d, want %d", i, at[i], want)
		}
	}
	if holder[im.ID] < 0 || holder[im.ID] >= len(after) {
		t.Errorf("held by %d", holder[im.ID])
	}
	if !strings.Contains(h.status(), "Image added at 00:03 for 4 s") || !strings.Contains(h.status(), "backup") {
		t.Errorf("status: %q", h.status())
	}

	// it is on show while playback is in [3 s, 7 s): a card with its name here (this terminal has no sixel), and gone after
	h.waitFor("the image on show", func() bool {
		return strings.Contains(h.screen(), "display image: My Shot.png") && strings.Contains(h.screen(), "PNG  40x20")
	}, 6*time.Second)
	for _, want := range []string{"00:03 - 00:07 (4 s)", "does not show pictures"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("%q is not on the card:\n%s", want, h.screen())
		}
	}
	h.waitFor("the image to go", func() bool { return !strings.Contains(h.screen(), "PNG  40x20") }, 8*time.Second)
}

func TestAnImageIsOnShowWhilePlaybackIsPausedInIt(t *testing.T) {
	h := newStretched(t, nil)
	pic := writePNG(t, t.TempDir(), "a.png", 10, 10)
	h.addImage(pic, "00:02", "30")
	h.pausePlayback()
	h.onUI(func() { h.em.jumpToTime(5000); h.em.player.Pause() })
	h.waitFor("the image on show, paused in it", func() bool { return strings.Contains(h.screen(), "PNG  10x10") }, 3*time.Second)
	time.Sleep(600 * time.Millisecond)
	if !strings.Contains(h.screen(), "PNG  10x10") {
		t.Error("the image went while playback was paused")
	}
	h.onUI(func() { h.em.jumpToTime(1000); h.em.player.Pause() })
	h.waitFor("the image gone, with playback moved before it", func() bool { return !strings.Contains(h.screen(), "PNG  10x10") }, 3*time.Second)
}

func TestTheListOfCommandsHasARowForTheImageThatPlaysEditsAndDeletes(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	orig := frameTimes(h.reparsed())
	pic := writePNG(t, t.TempDir(), "slide.png", 40, 20)
	h.addImage(pic, "00:03", "4")
	h.pausePlayback()

	h.key(tcell.KeyCtrlL)
	h.waitFor("the list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	h.waitText("display image: slide.png", 2*time.Second)
	var rows []listRow
	var host int
	imgAt := -1
	h.onUI(func() {
		rows = h.em.listRows
		for i, r := range rows {
			if r.img != "" {
				imgAt = i
				_, host, _ = libtrecs.FindImage(h.em.commands, r.img)
			}
		}
	})
	if imgAt < 1 || rows[imgAt-1].cmd != rows[imgAt].cmd || rows[imgAt].cmd != host || rows[imgAt-1].img != "" {
		t.Fatalf("the image is not right after the command that holds it: %+v", rows)
	}
	if got := strings.Count(h.screen(), "display image: slide.png"); got != 1 {
		t.Errorf("%d rows for the image", got)
	}
	if !strings.Contains(h.screen(), "00:03   display image: slide.png") {
		t.Errorf("the row has its time first, as a command's:\n%s", h.screen())
	}

	// Ctrl+A on it is a command's cue card: refused, with why
	for i := 0; i < imgAt; i++ {
		h.key(tcell.KeyDown)
	}
	h.waitFor("the cursor on the image", func() (ok bool) {
		h.onUI(func() { ok = h.em.commandList.GetCurrentItem() == imgAt })
		return
	}, 2*time.Second)
	h.key(tcell.KeyCtrlA)
	h.waitFor("the refusal", func() bool { return strings.Contains(h.status(), "not an image") }, 2*time.Second)
	if h.front() != "commandlist" {
		t.Errorf("page %q", h.front())
	}

	// Ctrl+E: the modal, on the image
	h.key(tcell.KeyCtrlE)
	h.waitFor("the edit modal", h.imageModalUp, 2*time.Second)
	h.waitText("Edit image", 2*time.Second)
	var at, file, dur string
	h.onUI(func() {
		at, file, dur = h.em.imageModal.time.GetText(), h.em.imageModal.file.GetText(), h.em.imageModal.duration.GetText()
	})
	if at != "00:03" || file != "slide.png" || dur != "4" {
		t.Errorf("time %q, file %q, duration %q", at, file, dur)
	}
	if !strings.Contains(h.screen(), "Delete image  ^D") {
		t.Error("no Delete button in the edit modal")
	}
	// shorter: what follows moves back by the difference only
	h.imageFields("", "", "1")
	h.key(tcell.KeyCtrlS)
	h.waitFor("the change", func() bool { return strings.Contains(h.status(), "Image changed") && h.front() == "playback" }, 6*time.Second)
	imgs, _ := h.imagesOnDisk()
	if len(imgs) != 1 || imgs[0].DurationSeconds != 1 || imgs[0].Timestamp != 3000 || imgs[0].ImageName != "slide.png" {
		t.Fatalf("%+v", imgs)
	}
	for i, ts := range frameTimes(h.reparsed()) {
		want := orig[i]
		if want >= 3000 {
			want += 1000
		}
		if ts != want {
			t.Fatalf("after the change, frame %d is at %d, want %d", i, ts, want)
		}
	}

	// Ctrl+P on its row: playback from the image, which is on show
	h.pausePlayback()
	h.key(tcell.KeyCtrlL)
	h.waitFor("the list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	for i := 0; i < imgAt; i++ {
		h.key(tcell.KeyDown)
	}
	h.key(tcell.KeyCtrlP)
	h.waitFor("playback", func() bool { return h.front() == "playback" }, 2*time.Second)
	h.waitFor("the image", func() bool { return strings.Contains(h.screen(), "PNG  40x20") }, 3*time.Second)
	h.pausePlayback()

	// Ctrl+D on its row: gone, and the time it took is given back
	h.key(tcell.KeyCtrlL)
	h.waitFor("the list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	for i := 0; i < imgAt; i++ {
		h.key(tcell.KeyDown)
	}
	h.key(tcell.KeyCtrlD)
	h.waitFor("the deletion", func() bool { imgs, _ := h.imagesOnDisk(); return len(imgs) == 0 && h.front() == "playback" }, 6*time.Second)
	for i, ts := range frameTimes(h.reparsed()) {
		if ts != orig[i] {
			t.Fatalf("after the deletion, frame %d is at %d, was %d", i, ts, orig[i])
		}
	}
	h.key(tcell.KeyCtrlL)
	h.waitFor("the list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	if strings.Contains(h.screen(), "display image") {
		t.Error("the row is still there")
	}
}

func TestCtrlEWhileAnImageIsOnShowEditsIt(t *testing.T) {
	h := newStretched(t, nil)
	pic := writePNG(t, t.TempDir(), "x.png", 10, 10)
	h.addImage(pic, "00:02", "30")
	h.pausePlayback()
	h.onUI(func() { h.em.jumpToTime(4000); h.em.player.Pause() })
	h.waitFor("the image", func() bool { return strings.Contains(h.screen(), "PNG  10x10") }, 3*time.Second)
	h.key(tcell.KeyCtrlE)
	h.waitFor("the edit modal", h.imageModalUp, 2*time.Second)
	h.waitText("Edit image", 2*time.Second)
	// and Ctrl+G does the same: there is no adding at a time that is inside an image
	h.key(tcell.KeyEscape)
	h.waitFor("playback", func() bool { return h.front() == "playback" }, 2*time.Second)
	h.pausePlayback()
	h.key(imageKey)
	h.waitFor("the edit modal again", h.imageModalUp, 2*time.Second)
	h.waitText("Edit image", 2*time.Second)
}

func TestAnImageThatCannotBeUsedIsRefusedWithWhy(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, append(pngBytesForTest(t), make([]byte, libtrecs.MaxImageBytes)...), 0o644); err != nil {
		t.Fatal(err)
	}
	good := writePNG(t, dir, "good.png", 10, 10)

	h.key(imageKey)
	h.waitFor("the modal", h.imageModalUp, 2*time.Second)
	try := func(file, at, secs, want string) {
		t.Helper()
		h.imageFields(file, at, secs)
		h.key(tcell.KeyCtrlS)
		h.waitFor("the refusal "+want, func() bool { return strings.Contains(h.screen(), want) }, 3*time.Second)
		if h.front() != imageModalPage {
			t.Errorf("%s: the modal closed", want)
		}
	}
	try(filepath.Join(dir, "missing.png"), "00:01", "3", "no such file")
	try(text, "00:01", "3", "not a PNG, JPEG, GIF or WebP image")
	try(big, "00:01", "3", "the most is 2.0 MB")
	try(dir, "00:01", "3", "that is a directory")
	try(good, "00:01", "0", "from 1 to 600.")
	try(good, "00:01", "601", "from 1 to 600.")
	try(good, "9:59:59", "3", "the recording is only")
	try(good, "1:99", "3", "minutes and seconds are under 60")
	if imgs, _ := h.imagesOnDisk(); len(imgs) != 0 {
		t.Error("an image was written")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.path()), libtrecs.ImagesDir)); err == nil {
		t.Error("a directory for images was made for nothing")
	}
}

func pngBytesForTest(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAnImageCannotBeAddedInsideAnother(t *testing.T) {
	h := newStretched(t, nil)
	pic := writePNG(t, t.TempDir(), "a.png", 10, 10)
	h.addImage(pic, "00:02", "5")
	h.pausePlayback()
	h.onUI(func() { h.em.imageOverlay.show = nil }) // as if playback were not in it, so that Ctrl+G adds
	h.onUI(func() { h.em.openImageModalAt(0, nil) })
	h.waitFor("the modal", h.imageModalUp, 2*time.Second)
	h.imageFields(pic, "00:04", "2")
	h.key(tcell.KeyCtrlS)
	h.waitFor("the refusal", func() bool { return strings.Contains(h.screen(), "is inside the image shown from 00:02 to 00:07") }, 3*time.Second)
	// at its start it goes before it
	h.imageFields("", "00:02", "")
	h.key(tcell.KeyCtrlS)
	h.waitFor("the second image", func() bool { return strings.Contains(h.status(), "Image added at 00:02") && h.front() == "playback" }, 6*time.Second)
	imgs, _ := h.imagesOnDisk()
	if len(imgs) != 2 || imgs[0].Timestamp != 2000 || imgs[1].Timestamp != 4000 {
		t.Errorf("%+v", imgs)
	}
}

// ---- the file picker ------------------------------------------------------------------------------------------------------------

func TestThePickerListsDirectoriesAndImagesAndChoosesOne(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	root := t.TempDir()
	sub := filepath.Join(root, "shots")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, sub, "inner.png", 10, 10)
	writePNG(t, root, "top.png", 10, 10)
	writePNG(t, root, ".hidden.png", 10, 10)
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "huge.gif"), append([]byte("GIF89a"), make([]byte, libtrecs.MaxImageBytes+10)...), 0o644); err != nil {
		t.Fatal(err)
	}

	h.key(imageKey)
	h.waitFor("the modal", h.imageModalUp, 2*time.Second)
	h.imageFields(filepath.Join(root, "x.png"), "", "") // the picker opens where the field points
	h.key(tcell.KeyCtrlO)
	h.waitFor("the picker", func() bool { return h.front() == imagePickerPage }, 2*time.Second)
	for _, want := range []string{"Choose an image", "shots/", "top.png", "huge.gif", "too large", "../"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("%q is not in the picker:\n%s", want, h.screen())
		}
	}
	for _, not := range []string{"readme.txt", ".hidden.png", "inner.png"} {
		if strings.Contains(h.screen(), not) {
			t.Errorf("%q is in the picker", not)
		}
	}
	h.typeText(".") // hidden files
	h.waitText(".hidden.png", 2*time.Second)
	h.typeText(".")
	h.waitGone(".hidden.png", 2*time.Second)

	h.typeText("s") // the next name that begins with s: the directory
	h.key(tcell.KeyEnter)
	h.waitText("inner.png", 2*time.Second)
	if strings.Contains(h.screen(), "top.png") {
		t.Error("the other directory is listed")
	}
	h.key(tcell.KeyBackspace2) // up, with the cursor on the directory it came from
	h.waitText("top.png", 2*time.Second)
	h.typeText("t")
	h.key(tcell.KeyEnter)
	h.waitFor("back to the modal", h.imageModalUp, 2*time.Second)
	var file, info string
	h.onUI(func() { file = h.em.imageModal.file.GetText(); info = h.em.imageModal.info.GetText(true) })
	if file != filepath.Join(root, "top.png") || !strings.Contains(info, "top.png  PNG 10x10") {
		t.Errorf("file %q, info %q", file, info)
	}
	h.key(tcell.KeyCtrlO)
	h.waitFor("the picker again", func() bool { return h.front() == imagePickerPage }, 2*time.Second)
	h.key(tcell.KeyEscape)
	h.waitFor("the modal, with the choice kept", func() bool { return h.front() == imageModalPage }, 2*time.Second)
}

func TestTheFileCanBeTypedAndTabCyclesTheModal(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	pic := writePNG(t, t.TempDir(), "typed.png", 12, 6)
	h.key(imageKey)
	h.waitFor("the modal", h.imageModalUp, 2*time.Second)
	h.typeText(pic)
	h.key(tcell.KeyEnter) // read when it is left
	h.waitFor("what the file is", func() bool { return strings.Contains(h.screen(), "typed.png  PNG 12x6") }, 10*time.Second)
	var onDuration bool
	h.onUI(func() { onDuration = h.em.app.GetFocus() == h.em.imageModal.duration })
	if !onDuration {
		t.Error("Enter in the file does not go on to the duration")
	}
	seen := map[string]bool{}
	focusName := func() (name string) {
		h.onUI(func() {
			m := h.em.imageModal
			for n, p := range map[string]tview.Primitive{"time": m.time, "file": m.file, "duration": m.duration, "browse": m.browse, "save": m.save, "discard": m.discard, "delete": m.del} {
				if h.em.app.GetFocus() == p {
					name = n
				}
			}
		})
		return
	}
	last := focusName()
	for i := 0; i < 8; i++ {
		h.key(tcell.KeyTab)
		h.waitFor("the focus to move", func() bool { return focusName() != last }, 2*time.Second)
		last = focusName()
		seen[last] = true
	}
	for _, k := range []string{"time", "file", "duration", "browse", "save", "discard"} {
		if !seen[k] {
			t.Errorf("Tab never reached %s", k)
		}
	}
	if seen["delete"] {
		t.Error("Tab went to a Delete button that is not there")
	}
}

// ---- the picture in a terminal that can show it -----------------------------------------------------------------------------------

func TestTheImageIsDrawnAsASixelPictureOverTheTerminalWhereThereIsRoom(t *testing.T) {
	var out syncBuf
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	copyFile(t, stretchedFixture(t, 8), path)
	cmds, err := libtrecs.ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, sim: tcell.NewSimulationScreen("UTF-8"), done: make(chan error, 1)}
	h.em = NewEditorMode(libtrecs.NewTerminalPlayer(), cmds, path)
	h.em.sixel = newSixelPainter(termGraphics{sixel: true, cellW: 8, cellH: 16})
	h.em.sixel.out = &out
	h.em.app.SetScreen(h.sim)
	h.sim.SetSize(120, 40)
	go func() { h.done <- h.em.Run() }()
	t.Cleanup(func() {
		h.em.app.Stop()
		err := <-h.done
		if err != nil {
			t.Errorf("the test stopped with an error: %v", err)
		}
	})
	h.waitFor("the editor", func() bool { return strings.Contains(h.screen(), "^L Browse") }, 5*time.Second)

	pic := writePNG(t, t.TempDir(), "wide.png", 1200, 600) // 2:1, red
	h.addImage(pic, "00:02", "30")
	h.pausePlayback()
	h.onUI(func() { h.em.jumpToTime(4000); h.em.player.Pause() })
	h.waitFor("the picture", func() bool { return strings.Contains(out.String(), "\x1bP") }, 25*time.Second)
	img, row, col := decodeDrawn(t, out.String())
	var vx, vy, vw, vh int
	h.onUI(func() { vx, vy, vw, vh = h.em.playbackView.GetRect() })
	// the area is 120x(vh-1) cells of 8x16 px; the picture is 2:1 and is as large as fits (it is larger than that): 960 px wide and half as high
	if img.W < 940 || img.W > 968 || img.H < 470 || img.H > 490 {
		t.Errorf("the picture is %dx%d px", img.W, img.H)
	}
	if pixel(img, img.W/2, img.H/2) != [3]byte{255, 0, 0} {
		t.Errorf("the middle is %v", pixel(img, img.W/2, img.H/2))
	}
	// 1-based; inside the view, above the caption's row, and centred across
	if col < vx+1 || row < vy+1 || row > vy+vh-1 || (img.W+7)/8+col-1 > vx+vw {
		t.Errorf("drawn at %d;%d for a view at %d,%d %dx%d", row, col, vx, vy, vw, vh)
	}
	if strings.Contains(h.screen(), "does not show pictures") {
		t.Error("the card is shown over the picture")
	}
	if !strings.Contains(h.screen(), "display image: wide.png    00:02 - 00:32 (30 s)") {
		t.Errorf("no caption:\n%s", h.screen())
	}

	// moved out of it: the painter lets go, and the cells are drawn again
	h.onUI(func() { h.em.jumpToTime(500); h.em.player.Pause() })
	h.waitFor("the picture to go", func() (gone bool) {
		h.onUI(func() { gone = len(h.em.sixel.last) == 0 })
		return
	}, 5*time.Second)
}

func TestAnImageIsNotDrawnOverACardOrAnotherPage(t *testing.T) {
	h := newStretched(t, nil)
	pic := writePNG(t, t.TempDir(), "x.png", 10, 10)
	h.addImage(pic, "00:02", "30")
	h.pausePlayback()
	h.onUI(func() { h.em.jumpToTime(4000); h.em.player.Pause() })
	h.waitFor("the image", func() bool { return strings.Contains(h.screen(), "PNG  10x10") }, 3*time.Second)
	h.key(tcell.KeyCtrlL)
	h.waitFor("the list", func() bool { return h.front() == "commandlist" }, 2*time.Second)
	if strings.Contains(h.screen(), "PNG  10x10") {
		t.Error("the image shows through the list")
	}
}

// ---- the sound ------------------------------------------------------------------------------------------------------------------

func ffmpegOrSkip(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

func audioSeconds(t *testing.T, file string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Format struct{ Duration string }
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	d, _ := strconv.ParseFloat(doc.Format.Duration, 64)
	return d
}

// meanVolume is the mean volume, in dB, of the part of a file from start for length seconds.
func meanVolume(t *testing.T, file string, start, length float64) float64 {
	t.Helper()
	out, _ := exec.Command("ffmpeg", "-v", "info", "-ss", strconv.FormatFloat(start, 'f', 3, 64), "-t", strconv.FormatFloat(length, 'f', 3, 64), "-i", file, "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
	for _, l := range strings.Split(string(out), "\n") {
		if i := strings.Index(l, "mean_volume:"); i >= 0 {
			v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l[i+len("mean_volume:"):]), "dB")), 64)
			return v
		}
	}
	t.Fatalf("no volume in %s", out)
	return 0
}

func TestTheSoundGetsSilenceForTheImageAndGivesItBack(t *testing.T) {
	ffmpegOrSkip(t)
	h := newStretched(t, nil)
	h.pausePlayback()
	audio := filepath.Join(filepath.Dir(h.path()), "audio.flac")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=12:sample_rate=44100", "-ac", "2", audio).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	before := audioSeconds(t, audio)

	h.addImage(writePNG(t, t.TempDir(), "s.png", 10, 10), "00:03", "4")
	h.waitFor("the sound to be 4 s longer", func() bool { got := audioSeconds(t, audio); return got > before+3.9 && got < before+4.1 }, 15*time.Second)
	if v := meanVolume(t, audio, 3.2, 3.6); v > -60 {
		t.Errorf("the 4 s of the image are not silent: %.1f dB", v)
	}
	if v := meanVolume(t, audio, 0.5, 2); v < -30 {
		t.Errorf("the sound before the image is gone: %.1f dB", v)
	}
	if v := meanVolume(t, audio, 7.5, 2); v < -30 {
		t.Errorf("the sound after the image is gone: %.1f dB", v)
	}
	if _, err := os.Stat(audio + ".backup"); err != nil {
		t.Errorf("no backup of the sound: %v", err)
	}
	h.waitFor("the sound's report", func() (ok bool) {
		h.onUI(func() { ok = strings.Contains(h.em.soundReport, "The sound was changed too") })
		return
	}, 10*time.Second)

	// shorter by 2 s: the sound is 2 s shorter; deleted: as it was
	var id string
	h.onUI(func() { id = libtrecs.RecordingImages(h.em.commands)[0].ID })
	h.waitFor("the sound to be done with", func() (ok bool) { h.onUI(func() { ok = !h.em.audioBusy.Load() }); return }, 10*time.Second)
	h.pausePlayback()
	h.onUI(func() { a, _ := h.em.imageByID(id); h.em.openImageModalFor(a) })
	h.waitFor("the edit modal", h.imageModalUp, 2*time.Second)
	h.imageFields("", "", "2")
	h.key(tcell.KeyCtrlS)
	h.waitFor("the sound to be 2 s longer than at first", func() bool { got := audioSeconds(t, audio); return got > before+1.9 && got < before+2.1 }, 15*time.Second)
	h.waitFor("the sound to be done with", func() (ok bool) { h.onUI(func() { ok = !h.em.audioBusy.Load() }); return }, 10*time.Second)
	h.pausePlayback()
	h.onUI(func() { h.em.deleteImage(id) })
	h.waitFor("the sound to be as it was", func() bool { got := audioSeconds(t, audio); return got > before-0.1 && got < before+0.1 }, 15*time.Second)
	if imgs, _ := h.imagesOnDisk(); len(imgs) != 0 {
		t.Errorf("%d images left", len(imgs))
	}
}

func TestWithoutFfmpegTheRecordingIsChangedAndTheSoundIsSaidNotToBe(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	audio := filepath.Join(filepath.Dir(h.path()), "audio.mp3")
	if err := os.WriteFile(audio, []byte("not really sound"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no ffmpeg
	h.addImage(writePNG(t, t.TempDir(), "s.png", 10, 10), "00:03", "4")
	h.waitFor("the report on the sound", func() (ok bool) {
		h.onUI(func() { ok = strings.Contains(h.em.soundReport, "The sound was not changed (ffmpeg is not installed)") })
		return
	}, 5*time.Second)
	if imgs, _ := h.imagesOnDisk(); len(imgs) != 1 {
		t.Error("the recording was not changed")
	}
	if got, _ := os.ReadFile(audio); string(got) != "not really sound" {
		t.Error("the sound file was touched")
	}
}

func TestAudioEditsForAChange(t *testing.T) {
	mk := func(ts int64, secs int) libtrecs.Annotation {
		return libtrecs.Annotation{Timestamp: ts, DurationSeconds: secs}
	}
	for name, c := range map[string]struct {
		old, now libtrecs.Annotation
		want     []libtrecs.AudioEdit
	}{
		"longer":    {mk(3000, 4), mk(3000, 9), []libtrecs.AudioEdit{{At: 7000, Dur: 5000}}},
		"shorter":   {mk(3000, 4), mk(3000, 1), []libtrecs.AudioEdit{{Remove: true, At: 4000, Dur: 3000}}},
		"the same":  {mk(3000, 4), mk(3000, 4), nil},
		"moved":     {mk(3000, 4), mk(8000, 2), []libtrecs.AudioEdit{{Remove: true, At: 3000, Dur: 4000}, {At: 8000, Dur: 2000}}},
		"unchanged": {mk(3000, 4), mk(3000, 4), nil},
	} {
		got := libtrecs.AudioEditsForChange(c.old, c.now)
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v", name, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %+v, want %+v", name, got, c.want)
			}
		}
	}
}

// ---- a note over the picture --------------------------------------------------------------------------------------------------------

func TestCutOutLeavesThePictureRoundTheBoxes(t *testing.T) {
	r := imageRect{dcs: "p", pxW: 800, pxH: 320, col: 10, row: 5, cols: 100, rows: 20, recCellW: 8, recCellH: 16, cellW: 8, cellH: 16}
	area := func(ps []imageRect) (n int) {
		for _, p := range ps {
			n += p.cols * p.rows
		}
		return
	}
	if got := cutOut(r, nil); len(got) != 1 || got[0] != r {
		t.Errorf("no box, no change: %+v", got)
	}
	// a box inside: four pieces round it, and what is left is the picture less the box
	box := cellBox{x: 40, y: 10, w: 30, h: 6}
	got := cutOut(r, []cellBox{box})
	if len(got) != 4 || area(got) != r.cols*r.rows-box.w*box.h {
		t.Errorf("%d pieces, %d cells (want %d)", len(got), area(got), r.cols*r.rows-box.w*box.h)
	}
	for _, p := range got {
		if p.col < box.x+box.w && p.col+p.cols > box.x && p.row < box.y+box.h && p.row+p.rows > box.y {
			t.Errorf("%+v is over the box", p)
		}
		// each piece is the part of the picture that is in its cells
		if p.srcCol != p.col-r.col || p.srcRow != p.row-r.row {
			t.Errorf("%+v takes the picture from the wrong place", p)
		}
	}
	// a box that is not over it, a box across the whole of it, and a box at its corner
	if got := cutOut(r, []cellBox{{x: 0, y: 0, w: 5, h: 5}}); len(got) != 1 || got[0] != r {
		t.Errorf("a box elsewhere: %+v", got)
	}
	if got := cutOut(r, []cellBox{{x: 0, y: 0, w: 200, h: 100}}); len(got) != 0 {
		t.Errorf("a box over all of it: %+v", got)
	}
	if got := cutOut(r, []cellBox{{x: 10, y: 5, w: 10, h: 4}}); len(got) != 2 || area(got) != r.cols*r.rows-40 {
		t.Errorf("a corner: %+v", got)
	}
	// two boxes
	if got := cutOut(r, []cellBox{box, {x: 12, y: 6, w: 3, h: 3}}); area(got) != r.cols*r.rows-box.w*box.h-9 {
		t.Errorf("two boxes: %d cells", area(got))
	}
}

func TestAnImageStaysOnShowWhileANoteIsOverItAndTheNoteIsNotCovered(t *testing.T) {
	var out syncBuf
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	copyFile(t, stretchedFixture(t, 8), path)
	cmds, err := libtrecs.ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, sim: tcell.NewSimulationScreen("UTF-8"), done: make(chan error, 1)}
	h.em = NewEditorMode(libtrecs.NewTerminalPlayer(), cmds, path)
	h.em.sixel = newSixelPainter(termGraphics{sixel: true, cellW: 8, cellH: 16})
	h.em.sixel.out = &out
	h.em.app.SetScreen(h.sim)
	h.sim.SetSize(120, 40)
	go func() { h.done <- h.em.Run() }()
	t.Cleanup(func() {
		h.em.app.Stop()
		if err := <-h.done; err != nil {
			t.Errorf("the test stopped with an error: %v", err)
		}
	})
	h.waitFor("the editor", func() bool { return strings.Contains(h.screen(), "^L Browse") }, 5*time.Second)

	h.addImage(writePNG(t, t.TempDir(), "wide.png", 1200, 600), "00:02", "30")
	h.pausePlayback()
	h.onUI(func() { h.em.jumpToTime(4000); h.em.player.Pause() })
	h.waitFor("the picture", func() (whole bool) {
		h.onUI(func() { whole = len(h.em.sixel.last) == 1 })
		return
	}, 25*time.Second)

	// a centred note comes up over it: the picture is still drawn, round the note
	h.onUI(func() {
		h.em.showTimed(&libtrecs.Annotation{Text: "**Downloading Trecs**\n\nhttps://example.com/releases", Placement: libtrecs.TimedPlacementCentered, DurationSeconds: 30})
	})
	var pieces []imageRect
	var boxes []cellBox
	h.waitFor("the picture round the note", func() (cut bool) {
		h.onUI(func() {
			pieces, boxes = append([]imageRect(nil), h.em.sixel.last...), h.em.overlayBoxes()
			cut = len(pieces) > 1
		})
		return
	}, 5*time.Second)
	if len(boxes) != 1 {
		t.Fatalf("%d boxes", len(boxes))
	}
	for _, p := range pieces {
		if b := boxes[0]; p.col < b.x+b.w && p.col+p.cols > b.x && p.row < b.y+b.h && p.row+p.rows > b.y {
			t.Errorf("a piece of the picture %+v is over the note %+v", p, b)
		}
	}

	// dismissed: the whole of it again
	h.onUI(func() { h.em.hideTimed() })
	h.waitFor("the whole picture", func() (whole bool) {
		h.onUI(func() { whole = len(h.em.sixel.last) == 1 })
		return
	}, 5*time.Second)
}
