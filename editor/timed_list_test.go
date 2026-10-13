package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	libtrecs "trecs/lib"
)

// Two timed annotations, a second apart in the stretched recording: the first with a long first line after a blank one.
func twoTimed(cmds []libtrecs.Command) {
	cmds[3].TimedAnnotations = []libtrecs.Annotation{
		{ID: "tm_a", Timestamp: 2000, Text: "\nFIRST LINE of the earlier annotation which is long enough to be cut off at the edge of the row\nsecond line", DurationSeconds: 3, Placement: libtrecs.TimedPlacementCentered, Kind: libtrecs.AnnotationKindTimed},
		{ID: "tm_b", Timestamp: 5000, Text: "LATER ONE", DurationSeconds: 4, Placement: libtrecs.TimedPlacementBottom, Kind: libtrecs.AnnotationKindTimed},
	}
}

func (h *harness) openTimedList() {
	h.t.Helper()
	h.pausePlayback()
	h.key(tcell.KeyCtrlA)
	h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
}

func TestCtrlAWithTimedAnnotationsShowsTheListWithTimesAndFirstLines(t *testing.T) {
	h := newStretched(t, twoTimed)
	h.openTimedList()
	h.waitText("Timed annotations", 2*time.Second)
	for _, want := range []string{"00:02  FIRST LINE of the earlier", "00:05  LATER ONE", "^N Add New", "ESC Discard"} {
		if !strings.Contains(h.screen(), want) {
			t.Errorf("%q is not on the screen:\n%s", want, h.screen())
		}
	}
	if strings.Contains(h.screen(), "second line") {
		t.Error("more than the first line is in the row")
	}
	if !strings.Contains(h.screen(), "...") {
		t.Error("a long first line is not cut with ...")
	}
	if strings.Contains(h.screen(), "Add annotation") {
		t.Error("the modal is up with the list")
	}
}

func TestEnterOnARowOpensThatAnnotationInTheModal(t *testing.T) {
	h := newStretched(t, twoTimed)
	h.openTimedList()
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	var ts, text string
	var style int
	var dur string
	h.onUI(func() {
		ts, text = h.em.timedModal.time.GetText(), h.em.timedModal.text.GetText()
		style, _ = h.em.timedModal.style.GetCurrentOption()
		dur = h.em.timedModal.duration.GetText()
	})
	if ts != "00:05" || text != "LATER ONE" || style != timedStyleIndex(libtrecs.TimedPlacementBottom) || dur != "4" {
		t.Errorf("time %q, text %q, style %d, duration %q", ts, text, style, dur)
	}
	h.timedModalHas("Delete annotation", "Editing the annotation at this second")
}

func TestCtrlNInTheListAddsNewAtTheCurrentTime(t *testing.T) {
	h := newStretched(t, twoTimed)
	h.waitFor("playback to be past a second and a half", func() bool { return h.position() > 1500 }, 5*time.Second)
	h.key(tcell.KeyCtrlA)
	h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
	var pos int64
	h.onUI(func() { pos = h.em.player.Position() })
	h.key(tcell.KeyCtrlN)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
	var ts, text string
	h.onUI(func() { ts, text = h.em.timedModal.time.GetText(), h.em.timedModal.text.GetText() })
	if want := libtrecs.ClockText(pos); ts != want || text != "" {
		t.Errorf("time %q (want %q), text %q", ts, want, text)
	}
	// playback was going on when the list opened, and goes on when the modal, which the list led to, is discarded
	h.key(tcell.KeyEscape)
	h.waitFor("playback to go on", func() bool { return h.front() == "playback" && h.playbackState() == libtrecs.PlaybackPlaying }, 2*time.Second)
}

func TestEscInTheListDiscardsItAndPlaybackGoesOn(t *testing.T) {
	h := newStretched(t, twoTimed)
	h.waitFor("playback to be past a second", func() bool { return h.position() > 1000 }, 5*time.Second)
	h.key(tcell.KeyCtrlA)
	h.waitFor("the list", func() bool { return h.front() == timedListPage }, 2*time.Second)
	if h.playbackState() != libtrecs.PlaybackPaused {
		t.Error("playback goes on behind the list")
	}
	h.key(tcell.KeyEscape)
	h.waitFor("playback to go on", func() bool { return h.front() == "playback" && h.playbackState() == libtrecs.PlaybackPlaying }, 2*time.Second)
}

func TestCtrlAWithNoTimedAnnotationsShowsTheModalDirectly(t *testing.T) {
	h := newStretched(t, nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlA)
	h.waitFor("the modal", func() bool { return h.front() == timedNotePage }, 2*time.Second)
}

func TestTheListSwallowsOtherKeys(t *testing.T) {
	h := newStretched(t, twoTimed)
	h.openTimedList()
	h.key(tcell.KeyCtrlL)
	time.Sleep(150 * time.Millisecond)
	if h.front() != timedListPage {
		t.Errorf("Ctrl+L reached past the list: %s", h.front())
	}
}

func TestTimedFirstLine(t *testing.T) {
	long := strings.Repeat("x", 80)
	for _, c := range []struct{ in, want string }{
		{"one", "one"},
		{"\n  \n  two  \nthree", "two"},
		{"", ""},
		{long, strings.Repeat("x", timedListRowMaxRunes-3) + "..."},
		{strings.Repeat("é", timedListRowMaxRunes), strings.Repeat("é", timedListRowMaxRunes)},
	} {
		if got := timedFirstLine(c.in); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatDurationGoesToHoursFromAnHour(t *testing.T) {
	for _, c := range []struct {
		ms   int64
		want string
	}{
		{-5, "00:00"},
		{0, "00:00"},
		{65_000, "01:05"},
		{3_599_999, "59:59"},
		{3_600_000, "1:00:00"},
		{(4*3600 + 37*60 + 41) * 1000, "4:37:41"},
		{(10*3600 + 5) * 1000, "10:00:05"},
	} {
		if got := formatDuration(c.ms); got != c.want {
			t.Errorf("%d ms: %q, want %q", c.ms, got, c.want)
		}
	}
}

// What the editor shows of a long recording is read back by what it accepts for a time.
func TestAnHourPlusTimeRoundTripsThroughTheClockText(t *testing.T) {
	ms := int64((4*3600 + 37*60 + 41) * 1000)
	got, err := parseClockText(formatDuration(ms))
	if err != nil || got != ms {
		t.Errorf("%d, %v", got, err)
	}
	if text := libtrecs.ClockText(ms); text != formatDuration(ms) {
		t.Errorf("the editor shows %q and the exports %q", formatDuration(ms), text)
	}
}
