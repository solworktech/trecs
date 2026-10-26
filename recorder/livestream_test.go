package main

import (
	"bytes"
	"strings"
	"testing"

	"trecs/cloud"
)

func TestTheStartWarningSaysWhatIsLeft(t *testing.T) {
	n := int64(512 << 20)
	got := livestreamWarning(&n)
	want := "This will start a live stream. We will make an effort to detect and mask any detected secrets, but we cannot guarantee detection.\nYou have 512 MB available on the server, if you exceed this amount, live streaming will end gracefully but you can continue recording,\nyour recording will be saved locally and you upload it later."
	if got != want {
		t.Errorf("%q", got)
	}
	if nolimit := livestreamWarning(nil); strings.Contains(nolimit, "available") || !strings.Contains(nolimit, "cannot guarantee detection") {
		t.Errorf("an account with no limit has nothing to run out of: %q", nolimit)
	}
}

func TestSizesAreSaidTheWayAPersonWould(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 900: "900 B", 2048: "2 KB", 5 << 20: "5 MB", (3 << 29): "1.5 GB", 2 << 30: "2 GB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

func TestOnlyYESStartsAStream(t *testing.T) {
	left := int64(10 << 20)
	for in, want := range map[string]bool{"YES\n": true, "  YES\r\n": true, "yes\n": false, "y\n": false, "\n": false, "": false, "YESS\n": false} {
		var out bytes.Buffer
		if got := confirmLivestream("", "https://trecs.example.com/live/abc", &left, strings.NewReader(in), &out, true, 0); got != want {
			t.Errorf("%q: %v", in, got)
		}
		if !strings.Contains(out.String(), "You have 10 MB available") || !strings.HasSuffix(out.String(), "Type 'YES' to continue, any other input to abort: ") {
			t.Errorf("%q", out.String())
		}
	}
}

func TestAScriptedStreamSaysWhereTheCommandsGo_AndAsksOnce(t *testing.T) {
	var out bytes.Buffer
	ok := confirmLivestream("demo.txt", "https://trecs.example.com/live/abc", nil, strings.NewReader("YES\n"), &out, true, 2)
	if !ok {
		t.Error("YES goes on")
	}
	s := out.String()
	for _, want := range []string{
		"This will start a live stream.",
		"WARNING: -commands-file is not interactive. Every line of demo.txt",
		"2 warnings were triggered when auditing demo.txt",
		"This will stream commands from demo.txt to https://trecs.example.com/live/abc.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
	if strings.Count(s, "Type 'YES'") != 1 {
		t.Errorf("asked more than once: %q", s)
	}
	if strings.Index(s, "This will stream commands") > strings.Index(s, "Type 'YES'") {
		t.Errorf("the question comes last: %q", s)
	}
}

func TestAStreamWithNoOneToAskIsWarnedAndGoesOn(t *testing.T) {
	var out bytes.Buffer
	if !confirmLivestream("ci.txt", "https://x/live/abc", nil, strings.NewReader(""), &out, false, 0) {
		t.Error("stopped, with nobody to ask")
	}
	if !strings.Contains(out.String(), "This will start a live stream") || strings.Contains(out.String(), "Type 'YES'") {
		t.Errorf("%q", out.String())
	}
}

func TestALivestreamIsNamedByItsIdOrItsPage(t *testing.T) {
	id := "3a1f0b5e-1111-4222-8333-444455556666"
	for _, in := range []string{id, "https://trecs.example.com/live/" + id, "https://trecs.example.com/live/" + id + "/", " " + strings.ToUpper(id) + " "} {
		if got, err := cloud.LivestreamID(in); err != nil || got != id {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", "https://trecs.example.com/live/", "https://trecs.example.com/live/not-an-id"} {
		if _, err := cloud.LivestreamID(in); err == nil {
			t.Errorf("%q is not one", in)
		}
	}
}
