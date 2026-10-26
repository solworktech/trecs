package lib

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
)

// The sound of a recording is a file of its own beside it (audio.opus and the like), played against the position of the terminal. When the
// recording's timeline changes - time taken out for deleted commands, time made for an image - the sound has to change with it, or
// everything after the change would be heard early or late. It is done with ffmpeg, which makes the file again, once, in the codec and
// bitrate it had. (Shared by the terminal editor, trecs-edit, and the web editor on the server.)

// AudioEdit is a change of the sound: silence of Dur milliseconds put in at At, or - Remove - Dur taken out from At.
type AudioEdit struct {
	Remove  bool
	At, Dur int64
}

// AudioEditsForChange is what the sound needs when an image becomes another: the timeline is as it was with the old image taken out
// (what was after it is earlier by its duration) and the new one put in.
func AudioEditsForChange(old, now Annotation) []AudioEdit {
	oldD, newD := ImageDurationMs(old), ImageDurationMs(now)
	if now.Timestamp == old.Timestamp {
		switch {
		case newD > oldD:
			return []AudioEdit{{At: old.Timestamp + oldD, Dur: newD - oldD}} // in the silence it has: the same as at its start
		case newD < oldD:
			return []AudioEdit{{Remove: true, At: old.Timestamp + newD, Dur: oldD - newD}}
		}
		return nil
	}
	return []AudioEdit{{Remove: true, At: old.Timestamp, Dur: oldD}, {At: now.Timestamp, Dur: newD}}
}

// ApplyAudio makes the sound of a recording what an edit left it: the time taken out of the timeline cut from it (all at once), then
// the silences put in and taken out. The file is changed in place, and only when ffmpeg has made the new one: on an error it is as it
// was. Nothing to do is not an error, and no ffmpeg is one only when there is something to do.
func ApplyAudio(file string, cut []Span, edits []AudioEdit) error {
	if len(cut) == 0 && len(edits) == 0 {
		return nil
	}
	if err := CutAudio(file, cut); err != nil {
		return err
	}
	for _, e := range edits {
		if err := ChangeAudio(file, e); err != nil {
			return err
		}
	}
	return nil
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

func audioSeconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }

// audioFilter is the ffmpeg filter graph of a change.
func audioFilter(e AudioEdit, p audioParams) string {
	format := fmt.Sprintf("aformat=sample_fmts=fltp:sample_rates=%d:channel_layouts=%s", p.rate, p.layout)
	head := fmt.Sprintf("[0:a]atrim=end=%s,asetpts=PTS-STARTPTS,apad=whole_dur=%s,%s[a]", audioSeconds(e.At), audioSeconds(e.At), format)
	if e.Remove {
		return head + fmt.Sprintf(";[0:a]atrim=start=%s,asetpts=PTS-STARTPTS,%s[b];[a][b]concat=n=2:v=0:a=1[out]", audioSeconds(e.At+e.Dur), format)
	}
	return head + fmt.Sprintf(";anullsrc=r=%d:cl=%s,atrim=end=%s,%s[s];[0:a]atrim=start=%s,asetpts=PTS-STARTPTS,%s[b];[a][s][b]concat=n=3:v=0:a=1[out]",
		p.rate, p.layout, audioSeconds(e.Dur), format, audioSeconds(e.At), format)
}

// ChangeAudio puts silence into the file, or takes time out of it. The file is replaced only when ffmpeg has made the new one.
func ChangeAudio(file string, e AudioEdit) error {
	if e.Dur <= 0 {
		return nil
	}
	return filterAudio(file, func(p audioParams) string { return audioFilter(e, p) })
}

// cutFilter is the ffmpeg filter graph that keeps all of the sound but the spans (in order of time, apart from one another).
func cutFilter(spans []Span, p audioParams) string {
	format := fmt.Sprintf("aformat=sample_fmts=fltp:sample_rates=%d:channel_layouts=%s", p.rate, p.layout)
	var parts, labels []string
	from := int64(0)
	add := func(to int64, open bool) {
		label := fmt.Sprintf("[k%d]", len(labels))
		trim := fmt.Sprintf("atrim=start=%s", audioSeconds(from))
		if !open {
			trim += ":end=" + audioSeconds(to)
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

// CutAudio takes the spans out of the file, all at once: once, so that the sound is encoded once however many there are.
func CutAudio(file string, spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	return filterAudio(file, func(p audioParams) string { return cutFilter(spans, p) })
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

// CopyFile copies src to dst (made, or replaced).
func CopyFile(src, dst string) error {
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
