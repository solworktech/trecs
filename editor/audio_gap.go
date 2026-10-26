package main

import (
	"fmt"

	"trecs/cloud"
	libtrecs "trecs/lib"
)

// The sound of a recording is a file of its own beside it (audio.opus and the like), played against the position of the terminal. An
// image takes time of its own (see lib/images.go), so the sound has to take it too, or everything after the image would be heard
// that much early: silence of the image's duration is put into it at the image's time - and taken out again when the image is deleted
// or made shorter. It is done with ffmpeg, which re-encodes the file once (the original is kept as <file>.backup); if the editor has no
// ffmpeg, or it fails, the recording is still changed, and the editor says that the sound is not.
// The changes themselves are made by package lib (AudioEdit, ApplyAudio, ChangeAudio, CutAudio), which the web editor on the server uses as well.

// audioFile is the sound of this session: the one given to the editor, or else the one beside the recording.
func (em *EditorMode) audioFile() string {
	if em.sound != nil {
		return em.sound.file
	}
	return recordingAudio(em.filePath)
}

// recordingAudio is the sound file beside a recording, "" if there is none.
func recordingAudio(recordingPath string) string {
	_, audio, err := cloud.ResolveSessionFiles(recordingPath)
	if err != nil {
		return ""
	}
	return audio
}

// changeSound does the changes to the sound of the recording - it takes a while, so not on the UI goroutine: the status line says so, and
// then how it went. Another change of an image waits until it is done (audioBusy), as the sound would be changed twice at once.
func (em *EditorMode) changeSound(status string, edits []libtrecs.AudioEdit) {
	if len(edits) == 0 {
		em.setStatus(status)
		return
	}
	em.changeSoundWith(status, func(file string) string { return runAudioEdits(file, edits) })
}

// changeSoundCut is changeSound for time taken out of the recording (commands deleted): the sound loses it too.
func (em *EditorMode) changeSoundCut(status string, spans []libtrecs.Span) {
	if len(spans) == 0 {
		em.setStatus(status)
		return
	}
	em.changeSoundWith(status, func(file string) string { return runAudioCut(file, spans) })
}

// changeSoundWith runs a change of the sound's file in the background and tells how it went.
func (em *EditorMode) changeSoundWith(status string, run func(file string) string) {
	file := em.audioFile()
	if file == "" {
		em.setStatus(status)
		return
	}
	em.soundReport = ""
	em.audioBusy.Store(true)
	em.setStatus(status + "  Changing the sound...")
	go func() {
		msg := run(file)
		em.app.QueueUpdateDraw(func() {
			em.soundReport = msg
			em.audioBusy.Store(false)
			if ap, ok := em.player.(*audioPlayer); ok {
				ap.resync() // what is heard is the file as it was
			}
			em.setStatus(status + msg)
		})
	}()
}

// runAudioEdits does the changes, and returns what is to be told after the status: how it went.
func runAudioEdits(audio string, edits []libtrecs.AudioEdit) string {
	if err := libtrecs.CopyFile(audio, audio+".backup"); err != nil { // the sound as it was, once, whatever the number of changes
		return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(edits[0].At))
	}
	for _, e := range edits {
		if err := libtrecs.ChangeAudio(audio, e); err != nil {
			return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(e.At))
		}
	}
	return "  The sound was changed too; backup: " + audio + ".backup"
}

// runAudioCut is runAudioEdits' for a cut, and returns what is to be told after the status.
func runAudioCut(audio string, spans []libtrecs.Span) string {
	if len(spans) == 0 {
		return ""
	}
	if err := libtrecs.CopyFile(audio, audio+".backup"); err != nil {
		return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(spans[0].AtMs))
	}
	if err := libtrecs.CutAudio(audio, spans); err != nil {
		return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(spans[0].AtMs))
	}
	return "  The sound was cut to match; backup: " + audio + ".backup"
}
