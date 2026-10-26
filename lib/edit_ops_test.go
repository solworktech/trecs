package lib

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func applyOK(t *testing.T, path string, ops ...EditOp) *EditResult {
	t.Helper()
	res, err := ApplyEdits(path, filepath.Join(t.TempDir(), "before.jsonl"), ops)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEditableCommandsListsWhatTheEditorShows(t *testing.T) {
	path := markedRecording(t)
	list, dur, err := EditableCommands(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || dur != 22500 {
		t.Fatalf("%d commands, %d ms", len(list), dur)
	}
	if list[1].Index != 1 || list[1].Input != "echo hi" || !strings.Contains(list[1].Output, "hi") || list[1].StartMs != 11000 {
		t.Errorf("%+v", list[1])
	}
	cut, _, _ := EditableCommands(path, 1)
	if cut[0].Output != "" && !cut[0].OutputTruncated && len(cut[0].Output) > 1 {
		t.Errorf("output not cut: %+v", cut[0])
	}
}

func TestEditingInputOutputAndNotesIsCarriedIntoTheFile(t *testing.T) {
	path := markedRecording(t)
	res := applyOK(t, path,
		EditOp{Op: "edit", Index: 1, Input: sp("echo bye")},
		EditOp{Op: "edit", Index: 2, Output: sp("redacted\n")},
		EditOp{Op: "note", Index: 0, Text: sp("**first**"), Seconds: 3},
		EditOp{Op: "cue", Index: 0, Placement: "intro", Text: sp("Welcome"), Seconds: 4},
		EditOp{Op: "cue", Index: 2, Placement: "post", Text: sp("The end"), Seconds: 2},
	)
	if res.Commands != 3 || len(res.Cut) != 0 || len(res.AudioEdits) != 0 || len(res.Changes) != 5 {
		t.Fatalf("%+v", res)
	}
	list, _, err := EditableCommands(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if list[1].Input != "echo bye" {
		t.Errorf("input %q", list[1].Input)
	}
	if !strings.Contains(list[2].Output, "redacted") || strings.Contains(list[2].Output, "hi") {
		t.Errorf("output %q", list[2].Output)
	}
	if list[0].Note == nil || list[0].Note.Text != "**first**" || list[0].Note.Seconds != 3 {
		t.Errorf("note %+v", list[0].Note)
	}
	if list[0].Intro == nil || list[0].Intro.Text != "Welcome" || list[2].Post == nil || list[2].Post.Seconds != 2 {
		t.Errorf("cards %+v %+v", list[0].Intro, list[2].Post)
	}
	// unchanged commands are as they were
	if list[0].Input != "echo hi" || !strings.Contains(list[0].Output, "hi") {
		t.Errorf("the first command changed: %+v", list[0])
	}
	// taken off again
	applyOK(t, path, EditOp{Op: "note", Index: 0, Text: sp(""), Seconds: 0}, EditOp{Op: "cue", Index: 0, Placement: "intro", Text: sp("")})
	list, _, _ = EditableCommands(path, 0)
	if list[0].Note != nil || list[0].Intro != nil || list[2].Post == nil {
		t.Errorf("not taken off: %+v", list[0])
	}
}

func TestDeletingACommandTakesItsTimeAndSaysWhatToCutFromTheSound(t *testing.T) {
	path := markedRecording(t)
	before := LastTimestamp(loadCommands(t, path))
	res := applyOK(t, path, EditOp{Op: "delete", Index: 1})
	if res.Commands != 2 || len(res.Cut) != 1 || res.Cut[0].AtMs != 11000 || res.Cut[0].DurMs != 1500 {
		t.Fatalf("%+v", res)
	}
	after := LastTimestamp(loadCommands(t, path))
	if after != before-1500 || res.DurationMs != after {
		t.Errorf("%d -> %d (result says %d)", before, after, res.DurationMs)
	}
}

func TestAnAnnotationOnADeletedCommandGoesWithIt(t *testing.T) {
	path := markedRecording(t)
	applyOK(t, path, EditOp{Op: "note", Index: 1, Text: sp("gone soon"), Seconds: 2})
	applyOK(t, path, EditOp{Op: "delete", Index: 1})
	list, _, _ := EditableCommands(path, 0)
	for _, c := range list {
		if c.Note != nil {
			t.Errorf("a note is left: %+v", c)
		}
	}
}

func TestTimedAnnotationsAreAddedByTheSecondChangedAndDeleted(t *testing.T) {
	path := markedRecording(t)
	applyOK(t, path, EditOp{Op: "timed_set", AtMs: 12200, Text: sp("look here"), Seconds: 3, Style: "centered", Pause: true, Nav: true})
	list, _, _ := EditableCommands(path, 0)
	if len(list[1].Timed) != 1 {
		t.Fatalf("held by the command its time is in: %+v", list)
	}
	a := list[1].Timed[0]
	if a.AtMs != 12200 || a.Text != "look here" || a.Style != "centered" || !a.Pause || !a.Nav || a.ID == "" {
		t.Fatalf("%+v", a)
	}
	// one at a second: this replaces it, and it is the same annotation
	applyOK(t, path, EditOp{Op: "timed_set", AtMs: 12800, Text: sp("look there"), Seconds: 2})
	list, _, _ = EditableCommands(path, 0)
	if len(list[1].Timed) != 1 || list[1].Timed[0].ID != a.ID || list[1].Timed[0].Text != "look there" || list[1].Timed[0].Style != "bottom" {
		t.Fatalf("%+v", list[1].Timed)
	}
	// by id, moved to another command
	applyOK(t, path, EditOp{Op: "timed_set", ID: a.ID, AtMs: 22000, Text: sp("moved"), Seconds: 2})
	list, _, _ = EditableCommands(path, 0)
	if len(list[1].Timed) != 0 || len(list[2].Timed) != 1 || list[2].Timed[0].ID != a.ID {
		t.Fatalf("%+v", list)
	}
	applyOK(t, path, EditOp{Op: "timed_delete", ID: a.ID})
	list, _, _ = EditableCommands(path, 0)
	if len(list[2].Timed) != 0 {
		t.Fatalf("%+v", list[2].Timed)
	}
}

func TestImagesMakeRoomChangeAndGoAndSaySoForTheSound(t *testing.T) {
	path := markedRecording(t)
	before := LastTimestamp(loadCommands(t, path))
	img := newImage(t, 12200, 4, "/home/me/shot.png")
	res := applyOK(t, path, EditOp{Op: "image_add", AtMs: 12200, Seconds: 4, Image: img.Image, ImageName: img.ImageName})
	if len(res.AudioEdits) != 1 || res.AudioEdits[0].Remove || res.AudioEdits[0].At != 12200 || res.AudioEdits[0].Dur != 4000 {
		t.Fatalf("%+v", res.AudioEdits)
	}
	if res.DurationMs != before+4000 || len(res.Images) != 1 || res.Images[0] != img.Image {
		t.Fatalf("%+v", res)
	}
	list, _, _ := EditableCommands(path, 0)
	if len(list[1].Images) != 1 {
		t.Fatalf("%+v", list)
	}
	id := list[1].Images[0].ID

	res = applyOK(t, path, EditOp{Op: "image_set", ID: id, AtMs: 12200, Seconds: 6, Image: img.Image, ImageName: img.ImageName})
	if len(res.AudioEdits) != 1 || res.AudioEdits[0].Remove || res.AudioEdits[0].Dur != 2000 || res.DurationMs != before+6000 {
		t.Fatalf("%+v %d", res.AudioEdits, res.DurationMs)
	}
	res = applyOK(t, path, EditOp{Op: "image_delete", ID: id})
	if len(res.AudioEdits) != 1 || !res.AudioEdits[0].Remove || res.AudioEdits[0].Dur != 6000 || res.DurationMs != before || len(res.Images) != 0 {
		t.Fatalf("%+v %d", res.AudioEdits, res.DurationMs)
	}
}

func TestABatchIsAllOrNothing(t *testing.T) {
	path := markedRecording(t)
	want, _ := os.ReadFile(path)
	cases := map[string][]EditOp{
		"an unknown change":           {{Op: "frobnicate"}},
		"a command that is not there": {{Op: "edit", Index: 0, Input: sp("x")}, {Op: "delete", Index: 9}},
		"editing a deleted command":   {{Op: "delete", Index: 0}, {Op: "edit", Index: 0, Input: sp("x")}},
		"deleting everything":         {{Op: "delete", Index: 0}, {Op: "delete", Index: 1}, {Op: "delete", Index: 2}},
		"an empty edit":               {{Op: "edit", Index: 0}},
		"a card without a place":      {{Op: "cue", Index: 0, Placement: "middle", Text: sp("x"), Seconds: 1}},
		"a card without seconds":      {{Op: "cue", Index: 0, Placement: "post", Text: sp("x")}},
		"a timed one past the end":    {{Op: "timed_set", AtMs: 999999, Text: sp("x"), Seconds: 1}},
		"a timed one with no text":    {{Op: "timed_set", AtMs: 1000, Text: sp(" "), Seconds: 1}},
		"a bad image name":            {{Op: "image_add", AtMs: 1000, Seconds: 2, Image: "../../etc/passwd"}},
		"two images":                  {{Op: "image_delete", ID: "a"}, {Op: "image_delete", ID: "b"}},
		"an image and a deletion":     {{Op: "delete", Index: 0}, {Op: "image_delete", ID: "a"}},
		"nothing":                     {},
	}
	for name, ops := range cases {
		_, err := ApplyEdits(path, filepath.Join(t.TempDir(), "b.jsonl"), ops)
		var ee *EditError
		if err == nil || !errors.As(err, &ee) {
			t.Errorf("%s: %v", name, err)
		}
		if got, _ := os.ReadFile(path); string(got) != string(want) {
			t.Fatalf("%s: the file was changed", name)
		}
	}
}

func TestTheErrorSaysWhichChangeIsWrong(t *testing.T) {
	path := markedRecording(t)
	_, err := ApplyEdits(path, filepath.Join(t.TempDir(), "b.jsonl"), []EditOp{{Op: "edit", Index: 0, Input: sp("ok")}, {Op: "delete", Index: 7}})
	var ee *EditError
	if !errors.As(err, &ee) || ee.Op != 1 || !strings.Contains(err.Error(), "change 2") {
		t.Fatalf("%v", err)
	}
}
