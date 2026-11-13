package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	libtrecs "github.com/solworktech/trecs/lib"
)

func TestParseJumpTime(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		bad  bool
	}{
		{"0:0:0", 0, false},
		{"0:1:30", 90_000, false},
		{"1:02:03", 3_723_000, false},
		{"1:30", 90_000, false},
		{"90", 90_000, false},
		{"5", 5000, false},
		{"0:00:07.5", 7500, false},
		{" 2:00 ", 120_000, false},
		{"", 0, true},
		{":", 0, true},
		{"1::2", 0, true},
		{"1:2:3:4", 0, true},
		{"1:75", 0, true},    // seconds go up to 59 when there are minutes
		{"1:60:00", 0, true}, // and minutes when there are hours
		{"a", 0, true},
		{"-5", 0, true},
		{"1.5:30", 0, true}, // only the seconds have a fraction
		{"1e3", 0, true},
		{"inf", 0, true},
		{"+5", 0, true},
	} {
		got, err := parseJumpTime(c.in)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("parseJumpTime(%q) = %d, %v; want %d, bad=%v", c.in, got, err, c.want, c.bad)
		}
	}
}

func TestClampSeek(t *testing.T) {
	for _, c := range []struct{ ms, total, want int64 }{
		{-5000, 52500, 0}, {0, 52500, 0}, {10000, 52500, 10000}, {52500, 52500, 52500}, {99999, 52500, 52500}, {5, 0, 0},
	} {
		if got := clampSeek(c.ms, c.total); got != c.want {
			t.Errorf("clampSeek(%d, %d) = %d, want %d", c.ms, c.total, got, c.want)
		}
	}
}

func (h *harness) sought() (ms int64) {
	h.onUI(func() { ms = h.em.currentTimeMs() })
	return
}

func (h *harness) waitPosition(ms int64) {
	h.t.Helper()
	h.waitFor("the position to be about "+clockHMS(ms), func() bool {
		p := h.sought()
		return p >= ms-50 && p <= ms+300
	}, 3*time.Second)
}

func TestRightAndLeftMoveTenSecondsAndStayPaused(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	h.key(tcell.KeyRight)
	h.waitPosition(10_000)
	h.key(tcell.KeyRight)
	h.waitPosition(20_000)
	h.key(tcell.KeyLeft)
	h.waitPosition(10_000)
	h.key(tcell.KeyLeft)
	h.key(tcell.KeyLeft) // past the start: it stops there
	h.waitPosition(0)
	if got := h.playbackState(); got != libtrecs.PlaybackPaused {
		t.Errorf("playback is %q, want it paused as it was", got)
	}
	for range 7 { // past the end: the same
		h.key(tcell.KeyRight)
	}
	var total int64
	h.onUI(func() { total = h.em.totalDurationMs })
	h.waitPosition(total)
}

func TestRightKeepsPlayingWhenPlaying(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.key(tcell.KeyRight)
	h.waitFor("playback to be past 10 s", func() bool { return h.position() >= 10_000 }, 3*time.Second)
	if got := h.playbackState(); got != libtrecs.PlaybackPlaying {
		t.Errorf("playback is %q, want it playing as it was", got)
	}
}

func TestCtrlJJumpsToATimeAndShowsTheScreenThen(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlJ)
	h.waitText("Jump to a time", 2*time.Second)
	if got := h.front(); got != jumpTimePage {
		t.Fatalf("the front page is %q", got)
	}
	h.typeText("0:00:31.6") // inside the fourth command: it is typed ("echo hi", at 31.5 s) and not yet run
	h.key(tcell.KeyEnter)
	h.waitPosition(31_600)
	if got := h.front(); got != "playback" {
		t.Errorf("the front page is %q", got)
	}
	if !strings.Contains(h.screen(), "echo hi") {
		t.Errorf("what was typed by then is not on the screen:\n%s", h.screen())
	}
	if got := h.playbackState(); got != libtrecs.PlaybackPaused {
		t.Errorf("playback is %q, want it paused as it was", got)
	}
	if !strings.Contains(h.status(), "0:31") {
		t.Errorf("the status does not say where: %q", h.status())
	}
}

func TestCtrlJRefusesWhatIsNotATimeOrIsPastTheEnd(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlJ)
	h.waitText("Jump to a time", 2*time.Second)
	before := h.sought()

	h.typeText("1:75")
	h.key(tcell.KeyEnter)
	h.waitText("minutes and seconds go up to 59", 2*time.Second)
	for range "1:75" {
		h.key(tcell.KeyBackspace2)
	}
	h.typeText("5:00") // the recording is 0:52 long
	h.key(tcell.KeyEnter)
	h.waitText("the recording is 0:52 long", 2*time.Second)
	if got := h.front(); got != jumpTimePage {
		t.Errorf("the front page is %q: the modal should stay for a correction", got)
	}
	if got := h.sought(); got != before {
		t.Errorf("playback moved from %d to %d", before, got)
	}

	h.key(tcell.KeyEscape)
	h.waitFor("Esc to close the modal", func() bool { return h.front() == "playback" }, 2*time.Second)
}

func TestCtrlJWhilePlayingPausesAndEscResumes(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.key(tcell.KeyCtrlJ)
	h.waitText("Jump to a time", 2*time.Second)
	if got := h.playbackState(); got != libtrecs.PlaybackPaused {
		t.Errorf("with the modal up playback is %q", got)
	}
	h.key(tcell.KeyCtrlJ) // the key that opened it closes it
	h.waitFor("^J to close the modal", func() bool { return h.front() == "playback" }, 2*time.Second)
	if got := h.playbackState(); got != libtrecs.PlaybackPlaying {
		t.Errorf("after closing playback is %q, want it going again", got)
	}
}

func TestArrowsAndCtrlJAreLeftAloneOnTheEditPage(t *testing.T) {
	h := newHarnessFile(t, pausedSix(t), nil)
	h.pausePlayback()
	h.key(tcell.KeyCtrlE)
	h.waitFor("the edit page", func() bool { return h.front() == "edit" }, 2*time.Second)
	before := h.sought()
	h.key(tcell.KeyLeft)
	h.key(tcell.KeyRight)
	h.key(tcell.KeyCtrlJ)
	time.Sleep(200 * time.Millisecond)
	if got := h.front(); got != "edit" {
		t.Errorf("the front page is %q", got)
	}
	if got := h.sought(); got != before {
		t.Errorf("playback moved from %d to %d", before, got)
	}
}

func TestTheHelpAndLegendNameTheNewKeys(t *testing.T) {
	_, rows := helpRows("playback")
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.key)
	}
	all := strings.Join(keys, "|")
	for _, want := range []string{"Left / Right", "^J"} {
		if !strings.Contains(all, want) {
			t.Errorf("the playback help has no %q: %v", want, keys)
		}
	}
	var legend []string
	for _, it := range playbackLegendItems() {
		legend = append(legend, it.key)
	}
	all = strings.Join(legend, "|")
	for _, want := range []string{"Left/Right", "^J"} {
		if !strings.Contains(all, want) {
			t.Errorf("the legend has no %q: %v", want, legend)
		}
	}
}
