package lib

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Editing a recording with a list of operations.
//
// trecs-edit changes a recording by staging changes on the commands (their input and output, notes, cue cards, deletions) and writing
// them all, one way or another, with RebuildRecording; images and timed annotations are written as they are made. ApplyEdits is that for a
// caller that has no screen to stage them on - the server's editor in the web page: it is given what to change, by the number of the command
// in the list ParseRecording makes of the file, and it changes the file in one go, or not at all. It makes the same file the editor would
// have made: the text of an edited command is carried into its frames in the same way, and what is deleted takes its time with it.
//
// What the sound needs is not done here (there is none, to ApplyEdits: it only sees the terminal file); it is in the result, for
// ApplyAudio.

// EditOp is one change. Op says which, and the other fields are those it needs:
//
//	edit          Index, and Input and/or Output: new text for the command (the output as plain text, as EditableCommands gives it).
//	note          Index, Text ("" takes the note off), Seconds (0 or more: how long playback shows it by itself, 0 for never).
//	cue           Index, Placement (intro or post), Text ("" takes the card off), Seconds (1 or more).
//	delete        Index: the command goes, and the time it took.
//	timed_set     ID, or none to add; AtMs, Text, Seconds (1 or more), Style (centered or bottom), Pause, Nav. One at a second: another at
//	              the same second is replaced.
//	timed_delete  ID.
//	image_add     AtMs, Seconds (1 to ImageMaxSeconds), Image (the name the picture is kept under) and ImageName (what it was called).
//	image_set     ID, and as image_add: the same image changed.
//	image_delete  ID.
//
// An image makes room for itself in the timeline (and the sound), and takes it when it goes: it is the one change that is not
// together with the rest of a batch that deletes commands, and only one of them is made at a time - the time of an image is a moment
// of the recording as it is when it is made, which the others change.
type EditOp struct {
	Op        string  `json:"op"`
	Index     int     `json:"index"`
	Input     *string `json:"input,omitempty"`
	Output    *string `json:"output,omitempty"`
	Text      *string `json:"text,omitempty"`
	Seconds   int     `json:"seconds,omitempty"`
	Placement string  `json:"placement,omitempty"`
	ID        string  `json:"id,omitempty"`
	AtMs      int64   `json:"at_ms,omitempty"`
	Style     string  `json:"style,omitempty"`
	Pause     bool    `json:"pause,omitempty"`
	Nav       bool    `json:"nav,omitempty"`
	Image     string  `json:"image,omitempty"`
	ImageName string  `json:"image_name,omitempty"`
}

// EditError is what is wrong with a batch of operations: Op is the number (from 0) of the one that is, or -1 for the batch.
type EditError struct {
	Op      int
	Message string
}

func (e *EditError) Error() string {
	if e.Op < 0 {
		return e.Message
	}
	return fmt.Sprintf("change %d: %s", e.Op+1, e.Message)
}

func editErr(op int, format string, a ...any) error {
	return &EditError{Op: op, Message: fmt.Sprintf(format, a...)}
}

// The most a batch may bring: what a person can have meant, and not more than a request should carry.
const (
	MaxEditOps       = 500
	MaxEditInputLen  = 64 << 10
	MaxEditOutputLen = 8 << 20
	MaxEditTextLen   = 20 << 10
)

// EditResult says what a batch did.
type EditResult struct {
	// Changes says in words what was done, one line each, in the order of the batch: the edit history's text.
	Changes []string
	// Commands is how many commands the recording has now.
	Commands int
	// Cut is the time taken out of the recording by deleted commands, and AudioEdits what the images put in or took out, for ApplyAudio.
	Cut        []Span
	AudioEdits []AudioEdit
	// Images are the names of the pictures the recording shows now.
	Images []string
	// DurationMs is the length of the recording now.
	DurationMs int64
}

// ApplyEdits changes the recording in the file, with the operations, all or none: on an error the file is as it was. The backup of
// the file as it was is made beside it with the name backupPath (which RebuildRecording makes, if there is none).
func ApplyEdits(path, backupPath string, ops []EditOp) (*EditResult, error) {
	if len(ops) == 0 {
		return nil, editErr(-1, "there is nothing to change")
	}
	if len(ops) > MaxEditOps {
		return nil, editErr(-1, "at most %d changes at a time", MaxEditOps)
	}
	commands, err := ParseRecording(path)
	if err != nil {
		return nil, fmt.Errorf("the recording cannot be read: %w", err)
	}
	if len(commands) == 0 {
		return nil, editErr(-1, "the recording has no commands")
	}
	deleted := map[int]bool{}
	imageOps := 0
	for i, op := range ops {
		switch op.Op {
		case "delete":
			deleted[op.Index] = true
		case "image_add", "image_set", "image_delete":
			imageOps++
		}
		_ = i
	}
	if imageOps > 1 {
		return nil, editErr(-1, "change one image at a time")
	}
	if imageOps > 0 && len(deleted) > 0 {
		return nil, editErr(-1, "commands are deleted in this batch: save that first, and then change the image")
	}

	res := &EditResult{}
	var imageOp *EditOp
	var imageOpNo int
	edited := map[int]bool{}
	for i := range ops {
		op := &ops[i]
		switch op.Op {
		case "image_add", "image_set", "image_delete":
			imageOp, imageOpNo = op, i
			continue
		}
		if err := applyOne(commands, deleted, edited, op, i, res); err != nil {
			return nil, err
		}
	}
	if len(deleted) >= len(commands) {
		return nil, editErr(-1, "all the commands would be deleted")
	}

	// What the deleted commands took is taken out of the timeline, and of the sound.
	res.Cut = RemovedSpans(commands, deleted)
	final := CompressTimestamps(commands, deleted)

	if imageOp != nil {
		var err error
		if final, err = applyImage(final, imageOp, imageOpNo, res); err != nil {
			return nil, err
		}
	}

	if err := RebuildRecording(path, backupPath, final); err != nil {
		return nil, fmt.Errorf("the recording could not be written: %w", err)
	}
	res.Commands = len(final)
	res.DurationMs = LastTimestamp(final)
	res.Images = ReferencedImages(RecordingImages(final))
	return res, nil
}

func inRange(commands []Command, op *EditOp, no int) error {
	if op.Index < 0 || op.Index >= len(commands) {
		return editErr(no, "there is no command %d (there are %d)", op.Index+1, len(commands))
	}
	return nil
}

func checkText(no int, what, s string, limit int) error {
	if !utf8.ValidString(s) {
		return editErr(no, "%s is not valid text", what)
	}
	if len(s) > limit {
		return editErr(no, "%s is %d bytes: at most %d", what, len(s), limit)
	}
	return nil
}

func applyOne(commands []Command, deleted, edited map[int]bool, op *EditOp, no int, res *EditResult) error {
	switch op.Op {
	case "delete":
		if err := inRange(commands, op, no); err != nil {
			return err
		}
		res.Changes = append(res.Changes, fmt.Sprintf("deleted command %d (%s)", op.Index+1, shorten(commands[op.Index].InputText)))
		return nil

	case "edit":
		if err := inRange(commands, op, no); err != nil {
			return err
		}
		if deleted[op.Index] {
			return editErr(no, "command %d is deleted", op.Index+1)
		}
		cmd := &commands[op.Index]
		if op.Input == nil && op.Output == nil {
			return editErr(no, "an edit needs a new input or a new output")
		}
		if op.Input != nil && *op.Input != cmd.InputText {
			if err := checkText(no, "the input", *op.Input, MaxEditInputLen); err != nil {
				return err
			}
			res.Changes = append(res.Changes, fmt.Sprintf("changed the input of command %d (%s)", op.Index+1, shorten(cmd.InputText)))
			cmd.InputText = *op.Input
		}
		if op.Output != nil && *op.Output != cmd.OutputText {
			if err := checkText(no, "the output", *op.Output, MaxEditOutputLen); err != nil {
				return err
			}
			res.Changes = append(res.Changes, fmt.Sprintf("changed the output of command %d (%s)", op.Index+1, shorten(commands[op.Index].InputText)))
			cmd.OutputText = *op.Output
		}
		edited[op.Index] = true
		return nil

	case "note":
		if err := inRange(commands, op, no); err != nil {
			return err
		}
		if deleted[op.Index] {
			return editErr(no, "command %d is deleted", op.Index+1)
		}
		if op.Text == nil {
			return editErr(no, "a note needs its text (empty takes the note off)")
		}
		if op.Seconds < 0 || op.Seconds > 3600 {
			return editErr(no, "the seconds are from 0 to 3600")
		}
		if err := checkText(no, "the note", *op.Text, MaxEditTextLen); err != nil {
			return err
		}
		cmd := &commands[op.Index]
		had := cmd.HasAnnotation
		cmd.Annotation.Text = *op.Text
		cmd.Annotation.DurationSeconds = op.Seconds
		cmd.HasAnnotation = strings.TrimSpace(*op.Text) != ""
		switch {
		case cmd.HasAnnotation && had:
			res.Changes = append(res.Changes, fmt.Sprintf("changed the note of command %d", op.Index+1))
		case cmd.HasAnnotation:
			res.Changes = append(res.Changes, fmt.Sprintf("added a note to command %d", op.Index+1))
		case had:
			res.Changes = append(res.Changes, fmt.Sprintf("took the note off command %d", op.Index+1))
		}
		return nil

	case "cue":
		if err := inRange(commands, op, no); err != nil {
			return err
		}
		if deleted[op.Index] {
			return editErr(no, "command %d is deleted", op.Index+1)
		}
		if op.Placement != CuePlacementIntro && op.Placement != CuePlacementPost {
			return editErr(no, "a cue card is shown before (intro) or after (post) a command")
		}
		if op.Text == nil {
			return editErr(no, "a cue card needs its text (empty takes the card off)")
		}
		cmd := &commands[op.Index]
		card, has := &cmd.IntroCard, &cmd.HasIntroCard
		if op.Placement == CuePlacementPost {
			card, has = &cmd.PostCard, &cmd.HasPostCard
		}
		if strings.TrimSpace(*op.Text) == "" {
			if *has {
				res.Changes = append(res.Changes, fmt.Sprintf("took the %s card off command %d", op.Placement, op.Index+1))
			}
			*card, *has = Annotation{}, false
			return nil
		}
		if op.Seconds < 1 || op.Seconds > 3600 {
			return editErr(no, "a cue card is shown for 1 to 3600 seconds")
		}
		if err := checkText(no, "the card", *op.Text, MaxEditTextLen); err != nil {
			return err
		}
		verb := "added a"
		if *has {
			verb = "changed the"
		}
		// what it replaces keeps its id and its date, so that it is the same card to everything that has read the file
		*card = Annotation{ID: card.ID, CreatedAt: card.CreatedAt, Text: *op.Text, DurationSeconds: op.Seconds, Kind: AnnotationKindCue, Placement: op.Placement}
		*has = true
		res.Changes = append(res.Changes, fmt.Sprintf("%s %s card on command %d", verb, op.Placement, op.Index+1))
		return nil

	case "timed_set":
		if op.Text == nil || strings.TrimSpace(*op.Text) == "" {
			return editErr(no, "enter the text of the annotation")
		}
		if op.Seconds < 1 || op.Seconds > 3600 {
			return editErr(no, "the duration is a whole number of seconds, 1 or more")
		}
		if op.AtMs < 0 || op.AtMs > LastTimestamp(commands) {
			return editErr(no, "the recording is only %s long", ClockText(LastTimestamp(commands)))
		}
		if err := checkText(no, "the annotation", *op.Text, MaxEditTextLen); err != nil {
			return err
		}
		style := op.Style
		switch style {
		case "", TimedPlacementBottom, TimedPlacementCentered:
		default:
			return editErr(no, "the style is centered or bottom")
		}
		a := Annotation{Timestamp: op.AtMs, Text: *op.Text, DurationSeconds: op.Seconds, Kind: AnnotationKindTimed, Placement: style, PausePlayback: op.Pause, ShowInNav: op.Nav}
		verb := "added"
		var old *Annotation
		if op.ID != "" {
			old = removeTimed(commands, edited, func(x Annotation) bool { return x.ID == op.ID })
			if old == nil {
				return editErr(no, "there is no annotation %s", op.ID)
			}
		} else {
			old = removeTimed(commands, edited, func(x Annotation) bool { return x.Timestamp/1000 == op.AtMs/1000 })
		}
		if old != nil {
			a.ID, a.CreatedAt = old.ID, old.CreatedAt
			verb = "changed"
		}
		host := imageHost(commands, op.AtMs)
		commands[host].TimedAnnotations = append(commands[host].TimedAnnotations, a)
		edited[host] = true
		res.Changes = append(res.Changes, fmt.Sprintf("%s the annotation at %s", verb, ClockText(op.AtMs)))
		return nil

	case "timed_delete":
		old := removeTimed(commands, edited, func(x Annotation) bool { return x.ID == op.ID && op.ID != "" })
		if old == nil {
			return editErr(no, "there is no annotation %s", op.ID)
		}
		res.Changes = append(res.Changes, fmt.Sprintf("deleted the annotation at %s", ClockText(old.Timestamp)))
		return nil
	}
	return editErr(no, "%q is not a change this knows", op.Op)
}

// removeTimed takes the first timed annotation that match says is the one out of the command that holds it, and returns it.
func removeTimed(commands []Command, edited map[int]bool, match func(Annotation) bool) *Annotation {
	for i := range commands {
		list := commands[i].TimedAnnotations
		for j := range list {
			if match(list[j]) {
				removed := list[j]
				commands[i].TimedAnnotations = append(append([]Annotation(nil), list[:j]...), list[j+1:]...)
				edited[i] = true
				return &removed
			}
		}
	}
	return nil
}

func applyImage(commands []Command, op *EditOp, no int, res *EditResult) ([]Command, error) {
	switch op.Op {
	case "image_delete":
		old, _, ok := FindImage(commands, op.ID)
		if !ok {
			return nil, editErr(no, "there is no image %s", op.ID)
		}
		out, _, err := RemoveImage(commands, op.ID)
		if err != nil {
			return nil, editErr(no, "%v", err)
		}
		res.AudioEdits = append(res.AudioEdits, AudioEdit{Remove: true, At: old.Timestamp, Dur: ImageDurationMs(old)})
		res.Changes = append(res.Changes, fmt.Sprintf("deleted the image %s at %s", imageLabel(old), ClockText(old.Timestamp)))
		return out, nil

	case "image_add", "image_set":
		if !ValidStoredImageName(op.Image) {
			return nil, editErr(no, "%q is not the name of an image", op.Image)
		}
		if op.Seconds < 1 || op.Seconds > ImageMaxSeconds {
			return nil, editErr(no, "the duration is a whole number of seconds, from 1 to %d", ImageMaxSeconds)
		}
		img := Annotation{Timestamp: op.AtMs, DurationSeconds: op.Seconds, Image: op.Image, ImageName: op.ImageName}
		if op.Op == "image_add" {
			out, err := AddImage(commands, img)
			if err != nil {
				return nil, editErr(no, "%v", err)
			}
			res.AudioEdits = append(res.AudioEdits, AudioEdit{At: op.AtMs, Dur: int64(op.Seconds) * 1000})
			res.Changes = append(res.Changes, fmt.Sprintf("added the image %s at %s for %d s", imageLabel(img), ClockText(op.AtMs), op.Seconds))
			return out, nil
		}
		old, _, ok := FindImage(commands, op.ID)
		if !ok {
			return nil, editErr(no, "there is no image %s", op.ID)
		}
		out, err := ReplaceImage(commands, op.ID, img)
		if err != nil {
			return nil, editErr(no, "%v", err)
		}
		now, _, _ := FindImage(out, op.ID)
		res.AudioEdits = append(res.AudioEdits, AudioEditsForChange(old, now)...)
		res.Changes = append(res.Changes, fmt.Sprintf("changed the image %s: %s for %d s", imageLabel(now), ClockText(now.Timestamp), op.Seconds))
		return out, nil
	}
	return nil, editErr(no, "%q is not an image change", op.Op)
}

func imageLabel(a Annotation) string {
	if a.ImageName != "" {
		return a.ImageName
	}
	return a.Image
}

// shorten is a command as a few words, for a line of the history.
func shorten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 48 {
		return string(r[:47]) + "…"
	}
	return s
}

// ---- what there is to edit ------------------------------------------------------------------------------------------------------

// EditableCommand is a command as the editor shows it.
type EditableCommand struct {
	Index   int    `json:"index"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	// OutputTruncated: Output is the start of it; it is not edited with the rest (an edit of it would be of a part).
	OutputTruncated bool            `json:"output_truncated,omitempty"`
	Note            *EditableCard   `json:"note,omitempty"`
	Intro           *EditableCard   `json:"intro,omitempty"`
	Post            *EditableCard   `json:"post,omitempty"`
	Timed           []EditableTimed `json:"timed,omitempty"`
	Images          []EditableImage `json:"images,omitempty"`
}

// EditableCard is a note or a cue card.
type EditableCard struct {
	Text    string `json:"text"`
	Seconds int    `json:"seconds"`
}

// EditableTimed is an annotation shown at a time.
type EditableTimed struct {
	ID      string `json:"id"`
	AtMs    int64  `json:"at_ms"`
	Text    string `json:"text"`
	Seconds int    `json:"seconds"`
	Style   string `json:"style"`
	Pause   bool   `json:"pause"`
	Nav     bool   `json:"nav"`
}

// EditableImage is a picture shown at a time.
type EditableImage struct {
	ID      string `json:"id"`
	AtMs    int64  `json:"at_ms"`
	Seconds int    `json:"seconds"`
	Image   string `json:"image"`
	Name    string `json:"name"`
}

// EditableCommands lists the commands of the recording in the file, as the numbers ApplyEdits goes by. Output longer than maxOutput
// bytes (0: no limit) is cut at a character, and says so.
func EditableCommands(path string, maxOutput int) ([]EditableCommand, int64, error) {
	commands, err := ParseRecording(path)
	if err != nil {
		return nil, 0, err
	}
	out := make([]EditableCommand, len(commands))
	for i, c := range commands {
		e := EditableCommand{Index: i, Input: c.InputText, Output: c.OutputText, StartMs: commandFirstTimestamp(c), EndMs: max(commandLastTimestamp(c), c.EndTime)}
		if maxOutput > 0 && len(e.Output) > maxOutput {
			cut := maxOutput
			for cut > 0 && !utf8.RuneStart(e.Output[cut]) {
				cut--
			}
			e.Output, e.OutputTruncated = e.Output[:cut], true
		}
		if c.HasAnnotation {
			e.Note = &EditableCard{Text: c.Annotation.Text, Seconds: c.Annotation.DurationSeconds}
		}
		if c.HasIntroCard {
			e.Intro = &EditableCard{Text: c.IntroCard.Text, Seconds: c.IntroCard.DurationSeconds}
		}
		if c.HasPostCard {
			e.Post = &EditableCard{Text: c.PostCard.Text, Seconds: c.PostCard.DurationSeconds}
		}
		for _, a := range c.TimedAnnotations {
			style := a.Placement
			if style != TimedPlacementCentered {
				style = TimedPlacementBottom
			}
			e.Timed = append(e.Timed, EditableTimed{ID: a.ID, AtMs: a.Timestamp, Text: a.Text, Seconds: a.DurationSeconds, Style: style, Pause: a.PausePlayback, Nav: a.ShowInNav})
		}
		for _, a := range c.Images {
			e.Images = append(e.Images, EditableImage{ID: a.ID, AtMs: a.Timestamp, Seconds: a.DurationSeconds, Image: a.Image, Name: a.ImageName})
		}
		out[i] = e
	}
	return out, LastTimestamp(commands), nil
}
