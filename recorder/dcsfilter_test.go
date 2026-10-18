package main

import (
	"math/rand"
	"strings"
	"testing"
)

const (
	testSixel = "\x1bP0;1;0q\"1;1;4;6#1;2;100;0;0#1!4~\x1b\\"
	testQuery = "\x1bP+q544e\x1b\\" // XTGETTCAP, the kind of thing vim sends
)

// run feeds the chunks through a fresh filter and returns the recorded output, and what each chunk gave.
func run(f *dcsFilter, chunks ...string) (all string, each []string) {
	for _, c := range chunks {
		o := f.Filter(c)
		each = append(each, o)
		all += o
	}
	return all + f.Flush(), each
}

func chunked(s string, sizes ...int) []string {
	var out []string
	for len(s) > 0 {
		n := sizes[len(out)%len(sizes)]
		n = min(n, len(s))
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

func TestDCSFilterKeepsSixelAndDropsTheRest(t *testing.T) {
	in := "before " + testQuery + "mid" + testSixel + "after" + "\x1bP$qm\x1b\\" + " end"
	got, _ := run(newDCSFilter(), in)
	want := "before mid" + testSixel + "after end"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestDCSFilterGivesTheSameWhereverTheOutputIsCut(t *testing.T) {
	in := "a" + testQuery + "b" + testSixel + "c\x1b[31mred\x1b[0m" + testSixel + testQuery + "d\x1b"
	want, _ := run(newDCSFilter(), in)
	if strings.Contains(want, "+q544e") || strings.Count(want, "0;1;0q") != 2 || !strings.HasSuffix(want, "d\x1b") {
		t.Fatalf("whole: %q", want)
	}
	for size := 1; size <= len(in); size++ {
		if got, _ := run(newDCSFilter(), chunked(in, size)...); got != want {
			t.Fatalf("chunks of %d:\n got %q\nwant %q", size, got, want)
		}
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		var chunks []string
		for rest := in; rest != ""; {
			n := min(1+r.Intn(12), len(rest))
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		if got, _ := run(newDCSFilter(), chunks...); got != want {
			t.Fatalf("random cuts %q:\n got %q\nwant %q", chunks, got, want)
		}
	}
}

func TestDCSFilterHoldsAGraphicUntilItIsWhole(t *testing.T) {
	f := newDCSFilter()
	_, each := run(f, "x"+testSixel[:10], testSixel[10:20], testSixel[20:]+"y")
	if each[0] != "x" || each[1] != "" || each[2] != testSixel+"y" {
		t.Errorf("%q", each)
	}
	if f.holding() {
		t.Error("still holding")
	}
	// the chunk that is only held gives no frame; see captureAndEchoOutput
	f = newDCSFilter()
	f.Filter(testSixel[:10])
	if !f.holding() {
		t.Error("not holding a half graphic")
	}
}

func TestDCSFilterDropsABigGraphicWithAMark(t *testing.T) {
	f := newDCSFilter()
	f.max = 100
	big := "\x1bP0;1;0q" + strings.Repeat("~", 500) + "\x1b\\"
	got, _ := run(f, "a", big[:150], big[150:400], big[400:]+"b"+testSixel)
	if got != "a"+markSixelDropped+"b"+testSixel {
		t.Errorf("%q", got)
	}
	// whole in one chunk too
	got, _ = run(newDCSFilterMax(100), "a"+big+"b")
	if got != "a"+markSixelDropped+"b" {
		t.Errorf("%q", got)
	}
	// exactly at the limit is kept
	f = newDCSFilter()
	f.max = len("0;1;0q\"1;1;4;6#1;2;100;0;0#1!4~")
	if got, _ := run(f, testSixel); got != testSixel {
		t.Errorf("at the limit: %q", got)
	}
	f.max--
	if got, _ := run(f, testSixel); got != markSixelDropped {
		t.Errorf("one over: %q", got)
	}
}

func newDCSFilterMax(n int) *dcsFilter { f := newDCSFilter(); f.max = n; return f }

func TestDCSFilterEdges(t *testing.T) {
	// an ESC that is not the start of anything is output (the end of the stream flushes it)
	if got, _ := run(newDCSFilter(), "a\x1b"); got != "a\x1b" {
		t.Errorf("%q", got)
	}
	// a graphic that never ends is not output
	if got, _ := run(newDCSFilter(), "a\x1bP0;1;0q#1~~"); got != "a" {
		t.Errorf("%q", got)
	}
	// a string with no kind yet at the end
	if got, _ := run(newDCSFilter(), "a\x1bP12;"); got != "a" {
		t.Errorf("%q", got)
	}
	// CSI, OSC and so on pass through untouched
	osc := "\x1b]0;title\a\x1b[2J\x1b[?1049h"
	if got, _ := run(newDCSFilter(), osc); got != osc {
		t.Errorf("%q", got)
	}
	// two graphics in a row
	if got, _ := run(newDCSFilter(), testSixel+testSixel); got != testSixel+testSixel {
		t.Errorf("%q", got)
	}
}

const (
	testImage    = "\x1b]1337;File=inline=1;width=2;height=1:iVBORw0KGgo=\a"
	testImageST  = "\x1b]1337;File=inline=1:iVBORw0KGgo=\x1b\\"
	testDownload = "\x1b]1337;File=name=YQ==;size=3:YWJj\a" // not inline: a file for the terminal to save
	testOtherOSC = "\x1b]1337;SetUserVar=a=YQ==\a\x1b]0;a title\a"
)

func TestDCSFilterKeepsInlineImages(t *testing.T) {
	in := "a" + testImage + "b" + testDownload + "c" + testImageST + "d" + testOtherOSC + "e" + testSixel
	got, _ := run(newDCSFilter(), in)
	want := "a" + testImage + "bc" + testImageST + "d" + testOtherOSC + "e" + testSixel
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestDCSFilterGivesTheSameForImagesWhereverTheOutputIsCut(t *testing.T) {
	in := "x\x1b[1mhi" + testImage + testQuery + "mid" + testImageST + testDownload + "end" + testSixel + "\x1b"
	want, _ := run(newDCSFilter(), in)
	if strings.Contains(want, "YWJj") || strings.Contains(want, "544e") {
		t.Fatalf("not filtered: %q", want)
	}
	for n := 1; n <= len(in); n++ {
		if got, _ := run(newDCSFilter(), chunked(in, n)...); got != want {
			t.Fatalf("cut every %d: got %q\nwant %q", n, got, want)
		}
	}
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		sizes := []int{1 + r.Intn(9), 1 + r.Intn(30), 1 + r.Intn(5)}
		if got, _ := run(newDCSFilter(), chunked(in, sizes...)...); got != want {
			t.Fatalf("cut %v: got %q\nwant %q", sizes, got, want)
		}
	}
}

func TestDCSFilterHoldsAnImageUntilItIsWholeAndDropsABigOneWithAMark(t *testing.T) {
	f := newDCSFilter()
	if got := f.Filter(testImage[:20]); got != "" || !f.holding() {
		t.Errorf("part of an image was output: %q", got)
	}
	if got := f.Filter(testImage[20:]); got != testImage || f.holding() {
		t.Errorf("got %q", got)
	}
	big := "\x1b]1337;File=inline=1:" + strings.Repeat("A", 100) + "\a"
	for _, sizes := range [][]int{{1000}, {7}, {30, 1}} {
		got, _ := run(newDCSFilterMax(50), chunked("a"+big+"b", sizes...)...)
		if got != "a"+markSixelDropped+"b" {
			t.Errorf("cut %v: %q", sizes, got)
		}
	}
}
