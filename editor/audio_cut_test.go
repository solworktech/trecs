package main

import (
	"strings"
	"testing"

	libtrecs "trecs/lib"
)

func TestCutFilterKeepsWhatIsBetweenTheSpans(t *testing.T) {
	p := audioParams{rate: 44100, layout: "stereo"}
	got := cutFilter([]libtrecs.Span{{AtMs: 2000, DurMs: 1000}, {AtMs: 5000, DurMs: 3000}}, p)
	for _, want := range []string{
		"atrim=start=0.000:end=2.000", "atrim=start=3.000:end=5.000", "atrim=start=8.000,", "concat=n=3:v=0:a=1[out]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not in %s", want, got)
		}
	}
	// a span at the very start leaves no piece before it; one filter piece alone needs no concat
	if got := cutFilter([]libtrecs.Span{{AtMs: 0, DurMs: 4000}}, p); strings.Contains(got, "concat") || !strings.Contains(got, "atrim=start=4.000,") {
		t.Errorf("from the start: %s", got)
	}
	// spans that touch make no empty piece between them
	if got := cutFilter([]libtrecs.Span{{AtMs: 1000, DurMs: 1000}, {AtMs: 2000, DurMs: 500}}, p); !strings.Contains(got, "concat=n=2:") {
		t.Errorf("touching: %s", got)
	}
}
