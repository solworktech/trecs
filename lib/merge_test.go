package lib

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mergeFixture(t *testing.T, name string, extra ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range extra {
		data = append(data, []byte(l+"\n")...)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mergeOK(t *testing.T, gap int64, inputs ...string) (string, []string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "merged.jsonl")
	var warnings []string
	warn := func(format string, a ...any) { warnings = append(warnings, format) }
	if err := MergeRecordings(inputs, out, gap, warn); err != nil {
		t.Fatal(err)
	}
	return out, warnings
}

func lastTimestamp(t *testing.T, path string) int64 {
	t.Helper()
	frames, _, _, err := LoadFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	return frames[len(frames)-1].Timestamp
}

func TestMergeShiftsEachRecordingToWhereThePreviousEndedAndClearsTheScreen(t *testing.T) {
	a, b, c := mergeFixture(t, "fish-marked.jsonl"), mergeFixture(t, "fish-native.jsonl"), mergeFixture(t, "fish-marked.jsonl")
	out, _ := mergeOK(t, 500, a, b, c)
	fa, _, _, _ := LoadFrames(a)
	fb, _, _, _ := LoadFrames(b)
	fc, _, _, _ := LoadFrames(c)
	got, _, _, err := LoadFrames(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(fa) + len(fb) + len(fc) + 2; len(got) != want { // and a clear before the second and the third
		t.Fatalf("%d frames, want %d", len(got), want)
	}
	offB := fa[len(fa)-1].Timestamp + 500
	offC := offB + fb[len(fb)-1].Timestamp + 500
	i := 0
	for _, f := range fa {
		if got[i] != f {
			t.Fatalf("frame %d of the first changed: %+v", i, got[i])
		}
		i++
	}
	for n, rec := range []struct {
		frames []TerminalFrame
		off    int64
	}{{fb, offB}, {fc, offC}} {
		if got[i].Data != mergeClear || got[i].Timestamp != rec.off {
			t.Fatalf("recording %d: not cleared at %d: %+v", n+2, rec.off, got[i])
		}
		i++
		for _, f := range rec.frames {
			if want := (TerminalFrame{Timestamp: f.Timestamp + rec.off, Data: f.Data, Width: f.Width, Height: f.Height}); got[i] != want {
				t.Fatalf("recording %d, frame %d: %+v, want %+v", n+2, i, got[i], want)
			}
			i++
		}
	}
	for j := 1; j < len(got); j++ {
		if got[j].Timestamp < got[j-1].Timestamp {
			t.Fatalf("time goes back at frame %d", j)
		}
	}
}

func TestMergeAnnotationsGoWithTheirFramesAndStayWithTheirCommands(t *testing.T) {
	// a timed annotation, a note and a cue card, in the second recording, at the times of its commands
	cmds, _ := ParseRecording(mergeFixture(t, "fish-marked.jsonl"))
	second := cmds[2]
	start := commandFirstTimestamp(second)
	a := mergeFixture(t, "fish-marked.jsonl")
	b := mergeFixture(t, "fish-marked.jsonl",
		`{"type":"annotation","id":"tm_1","timestamp":700,"text":"timed","kind":"timed","durationSeconds":3,"pausePlayback":true,"showInNav":true}`,
		`{"type":"annotation","id":"n_1","timestamp":`+itoa(start)+`,"text":"a note"}`,
		`{"type":"annotation","id":"c_1","timestamp":`+itoa(start)+`,"text":"a card","kind":"cue","placement":"intro","durationSeconds":4}`)
	out, _ := mergeOK(t, DefaultMergeGapMs, a, b)
	offset := lastTimestamp(t, a) + DefaultMergeGapMs

	_, anns, _, _ := LoadFrames(out)
	by := map[string]Annotation{}
	for _, x := range anns {
		by[x.ID] = x
	}
	if x := by["tm_1"]; x.Timestamp != 700+offset || !x.PausePlayback || !x.ShowInNav || x.DurationSeconds != 3 || x.Kind != AnnotationKindTimed {
		t.Errorf("timed: %+v (offset %d)", x, offset)
	}
	if by["n_1"].Timestamp != start+offset || by["c_1"].Timestamp != start+offset || by["c_1"].Placement != "intro" {
		t.Errorf("note %+v, card %+v", by["n_1"], by["c_1"])
	}

	merged, err := ParseRecording(out)
	if err != nil {
		t.Fatal(err)
	}
	single, _ := ParseRecording(b)
	// the first recording's commands, then the second's: the annotations are with the commands they were with
	idx := len(merged) - len(single) + 2
	if !merged[idx].HasAnnotation || merged[idx].Annotation.Text != "a note" || !merged[idx].HasIntroCard || merged[idx].IntroCard.Text != "a card" {
		t.Errorf("command %d (%q): %+v", idx, merged[idx].InputText, merged[idx])
	}
	timed := 0
	for _, c := range merged {
		for _, x := range c.TimedAnnotations {
			timed++
			if x.Text != "timed" {
				t.Errorf("%+v", x)
			}
		}
	}
	if timed != 1 {
		t.Errorf("%d timed annotations", timed)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestMergeOfUnmarkedRecordingsKeepsAllTheCommandsInOrder(t *testing.T) {
	a, b := mergeFixture(t, "reverse-search.jsonl"), mergeFixture(t, "cli-session.jsonl")
	ca, _ := ParseRecording(a)
	cb, _ := ParseRecording(b)
	out, _ := mergeOK(t, 0, a, b)
	got, err := ParseRecording(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ca)+len(cb) {
		t.Fatalf("%d commands, want %d + %d", len(got), len(ca), len(cb))
	}
	for i, c := range append(append([]Command{}, ca...), cb...) {
		if got[i].InputText != c.InputText {
			t.Errorf("command %d: %q, want %q", i, got[i].InputText, c.InputText)
		}
	}
}

func TestMergeKeepsAnIdUnlessAnotherHasItAndTheTerminalSizeOfTheFirst(t *testing.T) {
	line := `{"type":"annotation","id":"same","timestamp":100,"text":"x","kind":"timed","durationSeconds":2}`
	a, b := mergeFixture(t, "fish-marked.jsonl", line), mergeFixture(t, "fish-marked.jsonl", line)
	out, warns := mergeOK(t, 0, a, b)
	_, anns, meta, _ := LoadFrames(out)
	if len(anns) != 2 || anns[0].ID != "same" || anns[1].ID == "same" || anns[1].ID == "" {
		t.Errorf("ids: %+v", anns)
	}
	if meta.Width != 172 || meta.Height != 38 || len(warns) != 0 {
		t.Errorf("meta %+v, warnings %v", meta, warns)
	}
}

func TestMergeWarnsOfARecordingOfAnotherSizeAndUsesTheFirstsSize(t *testing.T) {
	a, b := mergeFixture(t, "fish-marked.jsonl"), mergeFixture(t, "fish-native.jsonl")
	_, wa, _, _ := LoadFrames(a)
	_ = wa
	_, _, ma, _ := LoadFrames(a)
	_, _, mb, _ := LoadFrames(b)
	out, warns := mergeOK(t, 0, a, b)
	_, _, got, _ := LoadFrames(out)
	if ma == mb {
		t.Skip("the fixtures have the same size")
	}
	if got != ma || len(warns) != 1 {
		t.Errorf("size %+v, want %+v; warnings %v", got, ma, warns)
	}
}

func TestMergeRefusesWhatItCannotDoRightAndChangesNothing(t *testing.T) {
	a, b := mergeFixture(t, "fish-marked.jsonl"), mergeFixture(t, "multiline.jsonl")
	before, _ := os.ReadFile(a)
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		in   []string
		out  string
		want string
	}{
		"one recording":    {[]string{a}, filepath.Join(dir, "o"), "at least two"},
		"output is input":  {[]string{a, b}, a, "one of the recordings"},
		"marks and none":   {[]string{a, b}, filepath.Join(dir, "o"), "prompt marks"},
		"missing input":    {[]string{a, filepath.Join(dir, "none.jsonl")}, filepath.Join(dir, "o"), "none.jsonl"},
		"none marks, then": {[]string{b, a}, filepath.Join(dir, "o"), "prompt marks"},
	} {
		err := MergeRecordings(tc.in, tc.out, 0, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if after, _ := os.ReadFile(a); !bytes.Equal(before, after) {
		t.Error("an input was changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "o")); err == nil {
		t.Error("an output was written for a refused merge")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".trecs-merge-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
