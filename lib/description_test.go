package lib

import (
	"strings"
	"testing"
)

func TestAnnotationDescriptionUsesOnlyNavAnnotationsInTimeOrder(t *testing.T) {
	anns := []Annotation{
		{Timestamp: 50504, Text: "**Step 1 – Edit nginx.conf in vim, bottom-up.**\nChanges lower in the file go first.", ShowInNav: true},
		{Timestamp: 4000, Text: "not in the nav", ShowInNav: false},
		{Timestamp: 9501, Text: "**Step 0 – Back up the config.**\nA copy.", ShowInNav: true},
		{Timestamp: 5000, Text: "  \n# A heading\nbody", ShowInNav: true},
		{Timestamp: 6000, Text: "   ", ShowInNav: true},
		{Timestamp: 3723000, Text: "late", ShowInNav: true},
	}
	got, used, total := AnnotationDescription(anns, "http://trecs.solwork.io/watch/abc", 0)
	want := strings.Join([]string{
		"[**[00:05]**](http://trecs.solwork.io/watch/abc?t=5) A heading",
		"[**[00:09]**](http://trecs.solwork.io/watch/abc?t=9) **Step 0 – Back up the config.**",
		"[**[00:50]**](http://trecs.solwork.io/watch/abc?t=50) **Step 1 – Edit nginx.conf in vim, bottom-up.**",
		"[**[1:02:03]**](http://trecs.solwork.io/watch/abc?t=3723) late",
	}, "\n")
	if got != want || used != 4 || total != 4 {
		t.Errorf("got (%d of %d)\n%s\nwant\n%s", used, total, got, want)
	}
}

func TestAnnotationDescriptionStopsAtTheLimit(t *testing.T) {
	var anns []Annotation
	for i := 0; i < 10; i++ {
		anns = append(anns, Annotation{Timestamp: int64(i) * 1000, Text: "step", ShowInNav: true})
	}
	got, used, total := AnnotationDescription(anns, "http://h/watch/x", 200)
	if total != 10 || used >= 10 || used == 0 || len([]rune(got)) > 200 || strings.Count(got, "\n") != used-1 {
		t.Errorf("used %d of %d in %d chars:\n%s", used, total, len([]rune(got)), got)
	}
	if got, used, total = AnnotationDescription(nil, "u", 100); got != "" || used != 0 || total != 0 {
		t.Errorf("no annotations: %q %d %d", got, used, total)
	}
}
