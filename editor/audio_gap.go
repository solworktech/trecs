package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"trecs/cloud"
	libtrecs "trecs/lib"
)

// The sound of a recording is a file of its own beside it (audio.opus and the like), played against the position of the terminal. An
// image takes time of its own (see lib/images.go), so the sound has to take it too, or everything after the image would be heard
// that much early: silence of the image's duration is put into it at the image's time - and taken out again when the image is deleted
// or made shorter. It is done with ffmpeg, which re-encodes the file once (the original is kept as <file>.backup); if the editor has no
// ffmpeg, or it fails, the recording is still changed, and the editor says that the sound is not.

// audioEdit is a change of the sound: silence of dur milliseconds put in at at, or - remove - dur taken out from at.
type audioEdit struct {
	remove  bool
	at, dur int64
}

// audioEditsForChange is what the sound needs when an image becomes another: the timeline is as it was with the old image taken out
// (what was after it is earlier by its duration) and the new one put in.
func audioEditsForChange(old, now libtrecs.Annotation) []audioEdit {
	oldD, newD := libtrecs.ImageDurationMs(old), libtrecs.ImageDurationMs(now)
	if now.Timestamp == old.Timestamp {
		switch {
		case newD > oldD:
			return []audioEdit{{at: old.Timestamp + oldD, dur: newD - oldD}} // in the silence it has: the same as at its start
		case newD < oldD:
			return []audioEdit{{remove: true, at: old.Timestamp + newD, dur: oldD - newD}}
		}
		return nil
	}
	return []audioEdit{{remove: true, at: old.Timestamp, dur: oldD}, {at: now.Timestamp, dur: newD}}
}

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
func (em *EditorMode) changeSound(status string, edits []audioEdit) {
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
func runAudioEdits(audio string, edits []audioEdit) string {
	if err := copyAudioFile(audio, audio+".backup"); err != nil { // the sound as it was, once, whatever the number of changes
		return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(edits[0].at))
	}
	for _, e := range edits {
		if err := changeAudio(audio, e); err != nil {
			return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(e.at))
		}
	}
	return "  The sound was changed too; backup: " + audio + ".backup"
}

// audioParams are what a new piece of silence has to match in the file.
type audioParams struct {
	rate    int
	layout  string
	bitrate int
}

func probeAudio(file string) (audioParams, error) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return audioParams{}, errors.New("ffprobe is not installed")
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=sample_rate,channels,channel_layout,bit_rate", "-of", "json", file).Output()
	if err != nil {
		return audioParams{}, fmt.Errorf("ffprobe: %w", err)
	}
	var doc struct {
		Streams []struct {
			SampleRate    string `json:"sample_rate"`
			Channels      int    `json:"channels"`
			ChannelLayout string `json:"channel_layout"`
			BitRate       string `json:"bit_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.Streams) == 0 {
		return audioParams{}, errors.New("there is no audio stream in the file")
	}
	s := doc.Streams[0]
	p := audioParams{layout: s.ChannelLayout}
	p.rate, _ = strconv.Atoi(s.SampleRate)
	p.bitrate, _ = strconv.Atoi(s.BitRate)
	if p.rate <= 0 {
		p.rate = 48000
	}
	if p.layout == "" || strings.Contains(p.layout, "unknown") {
		p.layout = "stereo"
		if s.Channels == 1 {
			p.layout = "mono"
		}
	}
	return p, nil
}

func seconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }

// audioFilter is the ffmpeg filter graph of a change.
func audioFilter(e audioEdit, p audioParams) string {
	format := fmt.Sprintf("aformat=sample_fmts=fltp:sample_rates=%d:channel_layouts=%s", p.rate, p.layout)
	head := fmt.Sprintf("[0:a]atrim=end=%s,asetpts=PTS-STARTPTS,apad=whole_dur=%s,%s[a]", seconds(e.at), seconds(e.at), format)
	if e.remove {
		return head + fmt.Sprintf(";[0:a]atrim=start=%s,asetpts=PTS-STARTPTS,%s[b];[a][b]concat=n=2:v=0:a=1[out]", seconds(e.at+e.dur), format)
	}
	return head + fmt.Sprintf(";anullsrc=r=%d:cl=%s,atrim=end=%s,%s[s];[0:a]atrim=start=%s,asetpts=PTS-STARTPTS,%s[b];[a][s][b]concat=n=3:v=0:a=1[out]",
		p.rate, p.layout, seconds(e.dur), format, seconds(e.at), format)
}

// changeAudio puts silence into the file, or takes time out of it. The file is replaced only when ffmpeg has made the new one.
func changeAudio(file string, e audioEdit) error {
	if e.dur <= 0 {
		return nil
	}
	return filterAudio(file, func(p audioParams) string { return audioFilter(e, p) })
}

// cutFilter is the ffmpeg filter graph that keeps all of the sound but the spans (in order of time, apart from one another).
func cutFilter(spans []libtrecs.Span, p audioParams) string {
	format := fmt.Sprintf("aformat=sample_fmts=fltp:sample_rates=%d:channel_layouts=%s", p.rate, p.layout)
	var parts, labels []string
	from := int64(0)
	add := func(to int64, open bool) {
		label := fmt.Sprintf("[k%d]", len(labels))
		trim := fmt.Sprintf("atrim=start=%s", seconds(from))
		if !open {
			trim += ":end=" + seconds(to)
		}
		parts = append(parts, fmt.Sprintf("[0:a]%s,asetpts=PTS-STARTPTS,%s%s", trim, format, label))
		labels = append(labels, label)
	}
	for _, sp := range spans {
		if sp.AtMs > from {
			add(sp.AtMs, false)
		}
		from = max(from, sp.AtMs+sp.DurMs)
	}
	add(0, true)
	if len(labels) == 1 {
		return parts[0] + ";" + labels[0] + "anull[out]"
	}
	return strings.Join(parts, ";") + ";" + strings.Join(labels, "") + fmt.Sprintf("concat=n=%d:v=0:a=1[out]", len(labels))
}

// cutAudio takes the spans out of the file, all at once: once, so that the sound is encoded once however many there are.
func cutAudio(file string, spans []libtrecs.Span) error {
	if len(spans) == 0 {
		return nil
	}
	return filterAudio(file, func(p audioParams) string { return cutFilter(spans, p) })
}

// runAudioCut is runAudioEdits' for a cut, and returns what is to be told after the status.
func runAudioCut(audio string, spans []libtrecs.Span) string {
	if len(spans) == 0 {
		return ""
	}
	if err := copyAudioFile(audio, audio+".backup"); err != nil {
		return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(spans[0].AtMs))
	}
	if err := cutAudio(audio, spans); err != nil {
		return fmt.Sprintf("  [red]The sound was not changed (%v): after %s it is not with the picture.", err, libtrecs.ClockText(spans[0].AtMs))
	}
	return "  The sound was cut to match; backup: " + audio + ".backup"
}

// filterAudio makes the file again through a filter graph, in the codec and bitrate it has. The file is replaced only when ffmpeg
// has made the new one.
func filterAudio(file string, filter func(audioParams) string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("ffmpeg is not installed")
	}
	p, err := probeAudio(file)
	if err != nil {
		return err
	}
	ext := filepath.Ext(file)
	tmp := filepath.Join(filepath.Dir(file), ".trecs-"+strings.TrimSuffix(filepath.Base(file), ext)+".new"+ext)
	defer func() { _ = os.Remove(tmp) }()
	args := []string{"-v", "error", "-y", "-i", file, "-filter_complex", filter(p), "-map", "[out]", "-vn"}
	switch strings.ToLower(ext) {
	case ".opus", ".ogg":
		args = append(args, "-c:a", "libopus")
	case ".mp3":
		args = append(args, "-c:a", "libmp3lame")
	case ".m4a":
		args = append(args, "-c:a", "aac")
	case ".flac":
		args = append(args, "-c:a", "flac")
	}
	if p.bitrate > 0 && !strings.EqualFold(ext, ".flac") {
		args = append(args, "-b:a", strconv.Itoa(p.bitrate))
	}
	args = append(args, tmp)
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("ffmpeg: %s", msg)
	}
	return os.Rename(tmp, file)
}

func copyAudioFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
