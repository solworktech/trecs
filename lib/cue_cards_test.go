package lib

import (
	"encoding/json"
	"strings"
	"testing"
)

const cueFixture = "testdata/fish-marked.jsonl"

func cueCard(text string, seconds int) Annotation {
	return Annotation{Text: text, DurationSeconds: seconds}
}

func annotationLines(t *testing.T, path string) []recordingLine {
	t.Helper()
	var out []recordingLine
	for _, l := range strings.Split(strings.TrimSpace(readFile(t, path)), "\n") {
		var rl recordingLine
		if err := json.Unmarshal([]byte(l), &rl); err != nil {
			t.Fatal(err)
		}
		if rl.Type == "annotation" {
			out = append(out, rl)
		}
	}
	return out
}

func TestCueCardsRoundTripAndSitBesideTheCommandsNote(t *testing.T) {
	path := copyToTemp(t, cueFixture)
	cmds, err := ParseRecording(path)
	if err != nil || len(cmds) < 4 {
		t.Fatalf("%d commands, %v", len(cmds), err)
	}
	last := len(cmds) - 1

	cmds[0].IntroCard, cmds[0].HasIntroCard = cueCard("Welcome", 4), true
	cmds[1].Annotation, cmds[1].HasAnnotation = Annotation{Text: "a note", DurationSeconds: 2}, true
	cmds[1].IntroCard, cmds[1].HasIntroCard = cueCard("Before the second", 3), true
	cmds[1].PostCard, cmds[1].HasPostCard = cueCard("After the second", 6), true
	cmds[last].PostCard, cmds[last].HasPostCard = cueCard("The end", 5), true

	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	got, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cmds) {
		t.Fatalf("%d commands after, %d before", len(got), len(cmds))
	}
	check := func(i int, intro, post, note string) {
		t.Helper()
		c := got[i]
		if c.IntroCard.Text != intro || c.HasIntroCard != (intro != "") {
			t.Errorf("command %d: intro %q (has %v), want %q", i, c.IntroCard.Text, c.HasIntroCard, intro)
		}
		if c.PostCard.Text != post || c.HasPostCard != (post != "") {
			t.Errorf("command %d: post %q (has %v), want %q", i, c.PostCard.Text, c.HasPostCard, post)
		}
		if c.Annotation.Text != note || c.HasAnnotation != (note != "") {
			t.Errorf("command %d: note %q (has %v), want %q", i, c.Annotation.Text, c.HasAnnotation, note)
		}
	}
	check(0, "Welcome", "", "")
	check(1, "Before the second", "After the second", "a note")
	check(2, "", "", "")
	check(last, "", "The end", "")
	if got[0].IntroCard.DurationSeconds != 4 || got[1].PostCard.DurationSeconds != 6 || got[1].Annotation.DurationSeconds != 2 {
		t.Errorf("durations: %d %d %d", got[0].IntroCard.DurationSeconds, got[1].PostCard.DurationSeconds, got[1].Annotation.DurationSeconds)
	}

	// On the file: every card is an annotation line saying what it is, anchored to the start of its own command - the
	// post card too - and they come before the notes.
	lines := annotationLines(t, path)
	seenNote := false
	for _, l := range lines {
		if l.Kind != AnnotationKindCue {
			seenNote = true
			continue
		}
		if seenNote {
			t.Errorf("a cue card line after a note's: %+v", l)
		}
		if l.Placement != CuePlacementIntro && l.Placement != CuePlacementPost || !strings.HasPrefix(l.ID, "cue_") {
			t.Errorf("%+v", l)
		}
	}
	if len(lines) != 5 {
		t.Errorf("%d annotation lines, want 4 cards and a note", len(lines))
	}
	for _, l := range lines {
		if l.Placement == CuePlacementPost && l.Text == "After the second" && l.Timestamp != commandFirstTimestamp(got[1]) {
			t.Errorf("a post card is anchored at %d, the command starts at %d", l.Timestamp, commandFirstTimestamp(got[1]))
		}
	}

	// Saved again, nothing changes: the same ids, the same cards, no duplicates.
	ids := map[string]bool{}
	for _, l := range lines {
		ids[l.ID] = true
	}
	if err := RebuildRecording(path, path+".bak", got); err != nil {
		t.Fatal(err)
	}
	again := annotationLines(t, path)
	if len(again) != len(lines) {
		t.Fatalf("%d lines the second time, %d the first", len(again), len(lines))
	}
	for _, l := range again {
		if !ids[l.ID] {
			t.Errorf("a new id on the second save: %s", l.ID)
		}
	}
}

// A reader that does not know of cue cards sees the file as it saw one without them: the same frames - a rebuild itself
// regroups the frames, so what is compared is a rebuild with the cards and one without - and a card comes out as an
// annotation, not as a frame.
func TestCueCardLinesAreNotFramesToAReaderThatIgnoresTheirKind(t *testing.T) {
	without := copyToTemp(t, cueFixture)
	cmds, _ := ParseRecording(without)
	if err := RebuildRecording(without, without+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	framesWithout, _, _, err := LoadFrames(without)
	if err != nil {
		t.Fatal(err)
	}

	with := copyToTemp(t, cueFixture)
	cmds, _ = ParseRecording(with)
	cmds[0].IntroCard, cmds[0].HasIntroCard = cueCard("Intro", 3), true
	cmds[len(cmds)-1].PostCard, cmds[len(cmds)-1].HasPostCard = cueCard("Post", 3), true
	if err := RebuildRecording(with, with+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	frames, anns, _, err := LoadFrames(with)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != len(framesWithout) {
		t.Errorf("%d frames with the cards, %d without: a card became a frame", len(frames), len(framesWithout))
	}
	for i := range frames {
		if frames[i] != framesWithout[i] {
			t.Fatalf("frame %d differs", i)
		}
	}
	if len(anns) != 2 || anns[0].Kind != AnnotationKindCue {
		t.Errorf("%+v", anns)
	}
}

// A card after a command is anchored to the command's start, because a command's last output and the next command's prompt
// commonly have the same timestamp: one anchored at the end would belong to the next.
func TestACardIsAttachedByTheStartOfItsCommandNotItsEnd(t *testing.T) {
	cmd := func(first, last int64) Command {
		return Command{StartTime: first, EndTime: last, InputFrames: []TerminalFrame{{Timestamp: first}}, OutputFrames: []TerminalFrame{{Timestamp: last}}}
	}
	mk := func() []Command { return []Command{cmd(0, 100), cmd(100, 200)} }

	cmds := mk()
	attachAnnotations(cmds, []Annotation{{Text: "x", Kind: AnnotationKindCue, Placement: CuePlacementPost, Timestamp: 0}})
	if !cmds[0].HasPostCard || cmds[1].HasPostCard {
		t.Errorf("anchored at the start: %v %v", cmds[0].HasPostCard, cmds[1].HasPostCard)
	}
	cmds = mk()
	attachAnnotations(cmds, []Annotation{{Text: "x", Kind: AnnotationKindCue, Placement: CuePlacementPost, Timestamp: 100}})
	if cmds[0].HasPostCard || !cmds[1].HasPostCard {
		t.Errorf("anchored at the end it lands on the next command (the reason it is not): %v %v", cmds[0].HasPostCard, cmds[1].HasPostCard)
	}
	// a placement that is not known is left out, and does not take the place of anything
	cmds = mk()
	cmds[0].Annotation, cmds[0].HasAnnotation = Annotation{Text: "note"}, true
	attachAnnotations(cmds, []Annotation{{Text: "x", Kind: AnnotationKindCue, Placement: "sideways", Timestamp: 0}})
	if cmds[0].HasIntroCard || cmds[0].HasPostCard || cmds[0].Annotation.Text != "note" {
		t.Errorf("%+v", cmds[0])
	}
}

func TestCardsFollowTheirCommandWhenAnEarlierOneIsDeleted(t *testing.T) {
	path := copyToTemp(t, cueFixture)
	cmds, _ := ParseRecording(path)
	cmds[3].IntroCard, cmds[3].HasIntroCard = cueCard("Intro of the fourth", 3), true
	cmds[3].PostCard, cmds[3].HasPostCard = cueCard("Post of the fourth", 3), true
	want := cmds[3].InputText

	compressed := CompressTimestamps(cmds, map[int]bool{1: true})
	if err := RebuildRecording(path, path+".bak", compressed); err != nil {
		t.Fatal(err)
	}
	got, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cmds)-1 {
		t.Fatalf("%d commands", len(got))
	}
	if c := got[2]; c.InputText != want || c.IntroCard.Text != "Intro of the fourth" || c.PostCard.Text != "Post of the fourth" {
		t.Errorf("the fourth command is now the third: %q intro %q post %q", c.InputText, c.IntroCard.Text, c.PostCard.Text)
	}
	for i, c := range got {
		if i != 2 && (c.HasIntroCard || c.HasPostCard) {
			t.Errorf("command %d got a card", i)
		}
	}
}

func TestACardWithNoTextIsNotWrittenAndALoadedOneKeepsItsId(t *testing.T) {
	path := copyToTemp(t, cueFixture)
	cmds, _ := ParseRecording(path)
	cmds[0].IntroCard, cmds[0].HasIntroCard = cueCard("  \n ", 3), true // a card that was emptied
	cmds[1].PostCard, cmds[1].HasPostCard = cueCard("Kept", 3), true
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	lines := annotationLines(t, path)
	if len(lines) != 1 || lines[0].Text != "Kept" {
		t.Errorf("%+v", lines)
	}
}
