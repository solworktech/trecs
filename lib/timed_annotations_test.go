package lib

import (
	"encoding/json"
	"strings"
	"testing"
)

func timedAt(ms int64, text string) Annotation {
	return Annotation{Timestamp: ms, Text: text, DurationSeconds: 3}
}

func timedHolders(cmds []Command) map[string][]int64 {
	out := map[string][]int64{}
	for i, c := range cmds {
		for _, a := range c.TimedAnnotations {
			out[a.Text] = append(out[a.Text], int64(i), a.Timestamp)
		}
	}
	return out
}

// A timed annotation is at a time. It is held by the command that time is in, which is not the command it was put on in memory:
// it is where its timestamp says, when the file is read.
func TestATimedAnnotationIsWithTheCommandItsTimeIsIn(t *testing.T) {
	path := copyToTemp(t, cueFixture) // commands begin at 22, 33, 39, 43, 1250, 1256 ms
	cmds, err := ParseRecording(path)
	if err != nil || len(cmds) != 6 {
		t.Fatalf("%d commands, %v", len(cmds), err)
	}
	cmds[0].TimedAnnotations = []Annotation{
		timedAt(5, "before the first command"), timedAt(500, "in the idle part of the fourth"), timedAt(1257, "in the last"),
	}
	cmds[5].TimedAnnotations = []Annotation{timedAt(35, "in the second")}
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	got, err := ParseRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	held := timedHolders(got)
	want := map[string][]int64{
		"before the first command":       {0, 5},  // before them all: with the first
		"in the second":                  {1, 35}, // and not with the command it was put on
		"in the idle part of the fourth": {3, 500},
		"in the last":                    {5, 1257},
	}
	for text, w := range want {
		if g := held[text]; len(g) != 2 || g[0] != w[0] || g[1] != w[1] {
			t.Errorf("%q: command %v, want command %d at %d ms", text, g, w[0], w[1])
		}
	}
	for i, c := range got {
		if c.HasAnnotation || c.HasIntroCard || c.HasPostCard {
			t.Errorf("command %d: a timed annotation became a note or a card", i)
		}
	}
}

// Every other kind of annotation is where its command begins; this one is where it is: its time is not changed to that of a command.
func TestATimedAnnotationKeepsItsTimeAndSitsBesideTheOthersInTheFile(t *testing.T) {
	path := copyToTemp(t, cueFixture)
	cmds, _ := ParseRecording(path)
	cmds[0].IntroCard, cmds[0].HasIntroCard = cueCard("a card", 3), true
	cmds[3].Annotation, cmds[3].HasAnnotation = Annotation{Text: "a note"}, true
	cmds[3].TimedAnnotations = []Annotation{timedAt(777, "at 777"), {Timestamp: 888, Text: "   "}} // the second has no text
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var at int64
	for _, l := range strings.Split(strings.TrimSpace(readFile(t, path)), "\n") {
		var rl recordingLine
		if err := json.Unmarshal([]byte(l), &rl); err != nil {
			t.Fatal(err)
		}
		switch {
		case rl.Type == "annotation" && rl.Kind == AnnotationKindTimed:
			kinds = append(kinds, "timed")
			at = rl.Timestamp
			if !strings.HasPrefix(rl.ID, "tm_") || rl.DurationSeconds != 3 || rl.Text != "at 777" {
				t.Errorf("%+v", rl)
			}
		case rl.Type == "annotation" && rl.Kind == AnnotationKindCue:
			kinds = append(kinds, "cue")
		case rl.Type == "annotation":
			kinds = append(kinds, "note")
		default:
			kinds = append(kinds, rl.Type)
		}
	}
	if got := strings.Join(kinds[:5], ","); got != "meta,cue,timed,note,frame" {
		t.Errorf("the head of the file is %s: the cards, then the timed annotations, then the notes, then the frames", got)
	}
	if at != 777 {
		t.Errorf("its time is %d, not 777", at)
	}
	if n := strings.Count(strings.Join(kinds, ","), "timed"); n != 1 {
		t.Errorf("%d timed annotations written; the one with no text is not one", n)
	}
}

func TestTimedAnnotationsKeepTheirIdsAndAreNotFramesToAnotherReader(t *testing.T) {
	path := copyToTemp(t, cueFixture)
	cmds, _ := ParseRecording(path)
	cmds[0].TimedAnnotations = []Annotation{timedAt(100, "one"), timedAt(200, "two")}
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	first, _ := ParseRecording(path)
	ids := map[string]string{}
	for _, c := range first {
		for _, a := range c.TimedAnnotations {
			ids[a.Text] = a.ID
		}
	}
	if ids["one"] == "" || ids["one"] == ids["two"] {
		t.Fatalf("ids: %v", ids)
	}
	if err := RebuildRecording(path, path+".bak", first); err != nil {
		t.Fatal(err)
	}
	second, _ := ParseRecording(path)
	for _, c := range second {
		for _, a := range c.TimedAnnotations {
			if ids[a.Text] != a.ID {
				t.Errorf("%q: id %s the second time, %s the first", a.Text, a.ID, ids[a.Text])
			}
		}
	}
	// To a reader that ignores kinds they are annotations, not frames.
	frames, anns, _, err := LoadFrames(path)
	if err != nil || len(anns) != 2 {
		t.Fatalf("%d annotations, %v", len(anns), err)
	}
	for _, f := range frames {
		if f.Data == "" && f.Timestamp == 100 {
			t.Error("a timed annotation became a frame")
		}
	}
}

// Deleting a command takes out its time: what is later is earlier by as much - and what was in the command goes with it.
func TestTimedAnnotationsMoveWithTheirCommandsWhenAnEarlierOneIsDeleted(t *testing.T) {
	path := copyToTemp(t, cueFixture)
	cmds, _ := ParseRecording(path)
	deleted := cmds[1] // 33 to 39 ms
	cmds[1].TimedAnnotations = []Annotation{timedAt(35, "in the deleted command")}
	cmds[3].TimedAnnotations = []Annotation{timedAt(500, "later")}
	cmds[0].TimedAnnotations = []Annotation{timedAt(25, "earlier")}
	shift := commandLastTimestamp(deleted) - commandFirstTimestamp(deleted)
	if shift <= 0 {
		t.Fatalf("shift %d", shift)
	}

	compressed := CompressTimestamps(cmds, map[int]bool{1: true})
	if got := cmds[3].TimedAnnotations[0].Timestamp; got != 500 {
		t.Errorf("the original was changed: %d", got)
	}
	if err := RebuildRecording(path, path+".bak", compressed); err != nil {
		t.Fatal(err)
	}
	got, _ := ParseRecording(path)
	held := timedHolders(got)
	if _, ok := held["in the deleted command"]; ok {
		t.Error("an annotation of a command that is gone was kept")
	}
	if g := held["later"]; len(g) != 2 || g[0] != 2 || g[1] != 500-shift {
		t.Errorf("\"later\": %v, want command 2 at %d ms", g, 500-shift)
	}
	if g := held["earlier"]; len(g) != 2 || g[0] != 0 || g[1] != 25 {
		t.Errorf("\"earlier\" is before what was deleted, and is where it was: %v", g)
	}
}

func TestClockText(t *testing.T) {
	for ms, want := range map[int64]string{0: "00:00", 999: "00:00", 1000: "00:01", 65_000: "01:05", 599_999: "09:59", 3_600_000: "1:00:00", 3_725_000: "1:02:05", -5: "00:00"} {
		if got := ClockText(ms); got != want {
			t.Errorf("%d ms: %q, want %q", ms, got, want)
		}
	}
}

func TestMarkdownIncludesTimedAnnotationsUnderTheirCommandsInTimeOrder(t *testing.T) {
	cmds, _ := ParseRecording("testdata/marks-script.jsonl")
	cmds[1].Annotation, cmds[1].HasAnnotation = Annotation{Text: "A note."}, true
	cmds[1].TimedAnnotations = []Annotation{
		{Timestamp: 125_000, Text: "Second, with `code` and **bold**."},
		{Timestamp: 5_000, Text: "First.\n\nAnd a second paragraph."},
		{Timestamp: 7_000, Text: "```sh\nls <dir>\n```"},
		{Timestamp: 9_000, Text: "  "}, // nothing
	}
	md := MarkdownFromCommands("t", cmds)
	h := strings.Index(md, "### `"+CollapseCommand(cmds[1].InputText)+"`")
	note := strings.Index(md, "\nA note.\n")
	first := strings.Index(md, "**[00:05]** First.\n\nAnd a second paragraph.")
	block := strings.Index(md, "**[00:07]**\n\n```sh\nls <dir>\n```")
	second := strings.Index(md, "**[02:05]** Second, with `code` and **bold**.")
	if h < 0 || note < h || first < note || block < first || second < block {
		t.Errorf("heading %d, note %d, 00:05 %d, 00:07 %d, 02:05 %d\n%s", h, note, first, block, second, md)
	}
	if strings.Contains(md, "**[00:09]**") {
		t.Error("an annotation with no text")
	}
}

// Code is what it is: its < is not escaped, and a fenced block is as it was written - in a card as in a timed annotation.
func TestTheExportLeavesCodeAsItWas(t *testing.T) {
	cmds, _ := ParseRecording("testdata/marks-script.jsonl")
	cmds[0].IntroCard, cmds[0].HasIntroCard = Annotation{Text: "Run `a < b` and <b>this</b>.\n```html\n<div>\n  <p>indented</p>\n\n</div>\n```\nAfter."}, true
	md := MarkdownFromCommands("t", cmds)
	for _, want := range []string{
		"> Run `a < b` and &lt;b>this&lt;/b>.\n",
		"> ```html\n> <div>\n>   <p>indented</p>\n>\n> </div>\n> ```\n",
		"> After.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("lacks %q\n%s", want, md)
		}
	}
}

// How a timed annotation is shown is its Placement: written, and read again, as it was - one that is not known as well, which
// is a newer writer's.
func TestATimedAnnotationsPlacementIsWrittenAndReadAgain(t *testing.T) {
	path := copyToTemp(t, cueFixture)
	cmds, _ := ParseRecording(path)
	cmds[3].TimedAnnotations = []Annotation{
		{Timestamp: 100, Text: "centred", Placement: TimedPlacementCentered},
		{Timestamp: 200, Text: "bottom", Placement: TimedPlacementBottom},
		{Timestamp: 300, Text: "none"},
		{Timestamp: 400, Text: "newer", Placement: "somewhere-else"},
	}
	if err := RebuildRecording(path, path+".bak", cmds); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, path), `"placement":"centered"`) {
		t.Error("the placement is not in the file")
	}
	got, _ := ParseRecording(path)
	placement := map[string]string{}
	for _, c := range got {
		for _, a := range c.TimedAnnotations {
			placement[a.Text] = a.Placement
		}
	}
	for text, want := range map[string]string{"centred": "centered", "bottom": "bottom", "none": "", "newer": "somewhere-else"} {
		if placement[text] != want {
			t.Errorf("%s: %q, want %q", text, placement[text], want)
		}
	}
}
