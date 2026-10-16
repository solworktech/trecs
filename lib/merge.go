package lib

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DefaultMergeGapMs is the silence between the end of one recording and the screen being cleared for the next, in a merge.
const DefaultMergeGapMs = 1000

// mergeClear is what is played between two recordings: the screen, and its scrollback, are cleared, and the cursor goes home - the next
// recording starts at a prompt, and is not to be drawn over what the one before it left.
const mergeClear = "\x1b[H\x1b[2J\x1b[3J"

// MergeRecordings joins recordings into one, in the order they are given, and writes it to output. Each recording after the first is
// played after the one before it: its frames, and its annotations (the notes and cue cards that are anchored to a command's start, and
// the timed annotations, which have a time of their own), all have the time the previous one ended, and gapMs more, added to them.
// The screen is cleared at the start of each of them but the first. Annotations keep their ids, unless that would give two the same.
//
// The size of the terminal (the recording's metadata) is that of the first recording that has one; warnf is told of a recording whose
// size is another, and of nothing else. The inputs are not changed, and output may not be one of them.
func MergeRecordings(inputs []string, output string, gapMs int64, warnf func(format string, args ...any)) error {
	if len(inputs) < 2 {
		return fmt.Errorf("at least two recordings are needed to merge, got %d", len(inputs))
	}
	if gapMs < 0 {
		return fmt.Errorf("the gap cannot be negative: %d", gapMs)
	}
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	outInfo, outErr := os.Stat(output)
	for _, in := range inputs {
		if a, b := filepath.Clean(in), filepath.Clean(output); a == b {
			return fmt.Errorf("the output %s is one of the recordings to merge", output)
		}
		if info, err := os.Stat(in); err == nil && outErr == nil && os.SameFile(info, outInfo) {
			return fmt.Errorf("the output %s is the same file as %s", output, in)
		}
	}

	var frames []TerminalFrame
	var annotations []Annotation
	var meta RecordingMeta
	metaFrom := ""
	marked := map[bool][]string{} // which recordings have prompt marks (OSC 133), and which have not
	ids := map[string]bool{}
	end := int64(0) // when what has been merged so far ends

	for i, in := range inputs {
		fs, as, m, err := LoadFrames(in)
		if err != nil {
			return fmt.Errorf("%s: %w", in, err)
		}
		if len(fs) == 0 {
			return fmt.Errorf("%s: no frames in the recording", in)
		}
		marked[hasPromptMarks(fs)] = append(marked[hasPromptMarks(fs)], in)
		if len(marked[true]) > 0 && len(marked[false]) > 0 {
			// Commands are found by the marks if there are any in a recording, and by the look of the prompt if there are none: a
			// recording with the one kind of the two, among the other, would be read by the other's rule, and its commands lost.
			return fmt.Errorf("cannot merge %s, which has prompt marks, with %s, which has none: the commands of one would be lost when the result is read",
				marked[true][0], marked[false][0])
		}
		if m.Width > 0 && m.Height > 0 {
			switch {
			case metaFrom == "":
				meta, metaFrom = m, in
			case m != meta:
				warnf("%s is %dx%d, and the merged recording is %dx%d, as %s is", in, m.Width, m.Height, meta.Width, meta.Height, metaFrom)
			}
		}

		offset := int64(0)
		if i > 0 {
			offset = end + gapMs
			frames = append(frames, TerminalFrame{Timestamp: offset, Data: mergeClear})
		}
		last := offset
		for _, f := range fs {
			f.Timestamp += offset
			last = max(last, f.Timestamp)
			frames = append(frames, f)
		}
		for _, a := range as {
			a.Timestamp += offset
			last = max(last, a.Timestamp)
			if a.ID != "" {
				a.ID = freshID(a.ID, i+1, ids)
				ids[a.ID] = true
			}
			annotations = append(annotations, a)
		}
		end = last
	}

	// the frames of a recording are in time order, and so are what follows them: a stable sort only keeps what has the same time in the
	// order it was in
	sort.SliceStable(frames, func(a, b int) bool { return frames[a].Timestamp < frames[b].Timestamp })
	return writeMerged(output, meta, annotations, frames)
}

// freshID is id, or - if another annotation has it - id with the number of the recording it is from, and a count if that is taken too.
func freshID(id string, recording int, taken map[string]bool) string {
	if !taken[id] {
		return id
	}
	for n := 0; ; n++ {
		c := fmt.Sprintf("%s_r%d", id, recording)
		if n > 0 {
			c = fmt.Sprintf("%s_%d", c, n)
		}
		if !taken[c] {
			return c
		}
	}
}

// writeMerged writes the recording as the editor does: the header, the annotations, and then the frames (see RebuildRecording for why
// they come first). It is written beside output and moved into place, so that a failure leaves nothing half-written.
func writeMerged(output string, meta RecordingMeta, annotations []Annotation, frames []TerminalFrame) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(output), ".trecs-merge-*")
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", output, err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	if err = writeMetaLine(enc, meta); err != nil {
		return err
	}
	for _, a := range annotations {
		line := recordingLine{Type: "annotation", ID: a.ID, Timestamp: a.Timestamp, Text: a.Text, Author: a.Author, CreatedAt: a.CreatedAt, DurationSeconds: a.DurationSeconds, Kind: a.Kind, Placement: a.Placement, PausePlayback: a.PausePlayback, ShowInNav: a.ShowInNav}
		if err = enc.Encode(line); err != nil {
			return err
		}
	}
	for _, f := range frames {
		if err = enc.Encode(recordingLine{Timestamp: f.Timestamp, Data: f.Data, Width: f.Width, Height: f.Height}); err != nil {
			return err
		}
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), output)
}

// writeMetaLine writes the header line for meta - nothing, if the recording had none to carry (no size, no environment).
func writeMetaLine(enc *json.Encoder, meta RecordingMeta) error {
	if !(meta.Width > 0 && meta.Height > 0) && meta.Env == "" && meta.Shell == "" {
		return nil
	}
	line := NewRecordingMetaLine(meta.Width, meta.Height)
	line.Env, line.Shell = meta.Env, meta.Shell
	return enc.Encode(line)
}
