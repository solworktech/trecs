package lib

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"time"
)

// Cutting down the long pauses of a recording.
//
// A pause is the time between one frame and the next (the recording has nothing else to say about it: the terminal printed nothing).
// A pause longer than the limit is cut down to it, and everything after it moves earlier by what was cut: the frames, the timed
// annotations, the images, and (through the spans, for ApplyAudio) the sound.
//
// The one time that is not a pause is that of an image: it is shown from its time for as long as it says, in a gap that the
// recording has made for it (see InsertGap). That gap is kept whole, and is not counted against the limit - or an image set for ten
// seconds would be shown for two.

// DefaultIdleTrim is the longest pause that is kept when none is asked for: the GIF export's default too.
const DefaultIdleTrim = 2 * time.Second

// MinIdleTrim is the shortest limit that can be asked for. A limit of nothing would run every command into the one before.
const MinIdleTrim = 100 * time.Millisecond

// CheckIdleTrim says what is wrong with a limit, if anything is.
func CheckIdleTrim(limit time.Duration) error {
	if limit < MinIdleTrim {
		return fmt.Errorf("the longest pause to keep is at least %s (it was %s)", MinIdleTrim, limit)
	}
	return nil
}

// IdleSpans is the time that cutting the pauses down to limit takes out of the recording, as stretches of the recording as it is now,
// in order of time and apart from each other: what a sound that goes with the recording must lose too.
func IdleSpans(commands []Command, limit time.Duration) []Span {
	limitMs := limit.Milliseconds()
	times := frameTimes(commands)
	images := RecordingImages(commands)

	var out []Span
	add := func(at, dur int64) {
		if dur <= 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].AtMs+out[n-1].DurMs == at {
			out[n-1].DurMs += dur
			return
		}
		out = append(out, Span{at, dur})
	}
	prev := int64(0) // the recording starts at 0, so a long wait before the first frame is a pause as well
	for _, t := range times {
		if t > prev {
			budget := limitMs
			// The pause, less the images in it: what is left is counted against the limit, and what is over it is cut.
			for _, seg := range freeStretches(prev, t, images) {
				if seg.DurMs <= budget {
					budget -= seg.DurMs
					continue
				}
				at := seg.AtMs + budget
				add(at, seg.AtMs+seg.DurMs-at)
				budget = 0
			}
		}
		prev = max(prev, t)
	}
	return out
}

// freeStretches is the part of [from, to) that no image is on show in, in order.
func freeStretches(from, to int64, images []Annotation) []Span {
	var out []Span
	at := from
	for _, im := range images {
		start, end := max(im.Timestamp, from), min(ImageEnd(im), to)
		if end <= start || end <= at {
			continue
		}
		if start > at {
			out = append(out, Span{at, start - at})
		}
		at = max(at, end)
	}
	if at < to {
		out = append(out, Span{at, to - at})
	}
	return out
}

// frameTimes is the time of every frame of every command, in order.
func frameTimes(commands []Command) []int64 {
	var ts []int64
	for _, c := range commands {
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
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	return ts
}

// movedBy is where a time of the recording is once the spans are cut out of it: earlier by the spans before it, and, if it is in one,
// at the place the span was cut from.
func movedBy(spans []Span, t int64) int64 {
	var cut int64
	for _, s := range spans {
		if s.AtMs >= t {
			break
		}
		cut += min(s.DurMs, t-s.AtMs)
	}
	return t - cut
}

func movedFrames(spans []Span, in []TerminalFrame) []TerminalFrame {
	if in == nil {
		return nil
	}
	out := make([]TerminalFrame, len(in))
	for i, f := range in {
		f.Timestamp = movedBy(spans, f.Timestamp)
		out[i] = f
	}
	return out
}

func movedAnnotations(spans []Span, in []Annotation) []Annotation {
	if in == nil {
		return nil
	}
	out := make([]Annotation, len(in))
	for i, a := range in {
		a.Timestamp = movedBy(spans, a.Timestamp)
		out[i] = a
	}
	return out
}

// CutSpans returns the commands with the spans taken out of their timeline, as copies; commands is not changed.
func CutSpans(commands []Command, spans []Span) []Command {
	out := make([]Command, len(commands))
	for i, c := range commands {
		if c.HasPrompt {
			c.PromptFrame.Timestamp = movedBy(spans, c.PromptFrame.Timestamp)
		}
		c.InputFrames = movedFrames(spans, c.InputFrames)
		c.OutputFrames = movedFrames(spans, c.OutputFrames)
		c.StartTime = movedBy(spans, c.StartTime)
		c.EndTime = movedBy(spans, c.EndTime)
		c.TimedAnnotations = movedAnnotations(spans, c.TimedAnnotations)
		c.Images = movedAnnotations(spans, c.Images)
		out[i] = c
	}
	return out
}

// TrimIdleResult is what cutting the pauses did.
type TrimIdleResult struct {
	Pauses      int           // how many stretches were cut
	Cut         []Span        // what was taken out
	Removed     time.Duration // the sum of Cut
	Before      time.Duration // the length of the recording, before and after
	After       time.Duration
	BackupPath  string // the recording as it was ("" if nothing was cut)
	AudioBackup string // the sound as it was ("" if there is none, or nothing was cut)
}

// TrimIdle cuts the recording's pauses down to limit and writes it back (the recording as it was goes to backupPath), and cuts the
// sound that goes with it, if audio names a file, so that it stays with the picture. Either both are changed or neither is: if the
// sound cannot be cut (no ffmpeg, say) the recording is not written, and if the cutting fails half way the recording is put back.
func TrimIdle(path, backupPath, audio string, limit time.Duration) (*TrimIdleResult, error) {
	if err := CheckIdleTrim(limit); err != nil {
		return nil, err
	}
	commands, err := ParseRecording(path)
	if err != nil {
		return nil, fmt.Errorf("the recording could not be read: %w", err)
	}
	if len(commands) == 0 {
		return nil, errors.New("there are no commands in the recording")
	}
	res := &TrimIdleResult{Before: time.Duration(LastTimestamp(commands)) * time.Millisecond}
	res.Cut = IdleSpans(commands, limit)
	res.Pauses = len(res.Cut)
	if len(res.Cut) == 0 {
		res.After = res.Before
		return res, nil // nothing is longer than the limit: the files are left as they are
	}
	var removed int64
	for _, s := range res.Cut {
		removed += s.DurMs
	}
	res.Removed = time.Duration(removed) * time.Millisecond
	res.After = res.Before - res.Removed

	if audio != "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return nil, errors.New("the recording has sound, which has to be cut to match, and ffmpeg is not installed: nothing was changed")
		}
	}
	if err := RebuildRecording(path, backupPath, CutSpans(commands, res.Cut)); err != nil {
		return nil, fmt.Errorf("the recording could not be written: %w", err)
	}
	res.BackupPath = backupPath
	if audio != "" {
		// the sound is made again only when ffmpeg has made the new one, so if this fails it is as it was
		err := CopyFile(audio, audio+".backup")
		if err == nil {
			err = CutAudio(audio, res.Cut)
		}
		if err != nil {
			if rerr := CopyFile(backupPath, path); rerr != nil {
				return nil, fmt.Errorf("the sound could not be cut (%v), and the recording could not be put back (%v): it is in %s", err, rerr, backupPath)
			}
			return nil, fmt.Errorf("the sound could not be cut (%v): nothing was changed", err)
		}
		res.AudioBackup = audio + ".backup"
	}
	return res, nil
}
