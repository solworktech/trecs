package lib

import (
	"strings"
	"testing"

	"github.com/rivo/tview"
)

// What tview shows for the text a screen renders: the characters of the screen, with the colour tags taken out and nothing else.
func shownByTview(rendered string) string {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetText(rendered)
	return strings.TrimRight(tv.GetText(true), "\n ")
}

// Square brackets are on screens everywhere: a prompt "[user@host]", vim's "[+]", "a[]" in C, "[red]" in a README. Each must come out
// as it is - not doubled (tview does not take "[[" for an escaped "["), and not taken for a colour tag.
var bracketLines = []string{
	"int a[] = {10, 20, 30};",
	`printf("%d\n", 2[a]);`,
	"q2.c [+]",
	"[user@host ~]$ ls",
	"[red] [#00ff00] [a:b:c] [-] [] [[] [[[]",
	"x[",
	"[",
	"]",
	"[[",
	`["123"] [a#"[[]`,
}

func TestBracketsOnTheScreenAreShownAsTheyAre(t *testing.T) {
	for _, line := range bracketLines {
		vt := NewVTScreenSized(80, 5)
		vt.Feed(line)
		if got := shownByTview(vt.Render()); got != line {
			t.Errorf("VTScreen %q is shown as %q", line, got)
		}
		db := NewDisplayBuffer()
		db.Feed(line)
		if got := shownByTview(db.Render()); got != line {
			t.Errorf("DisplayBuffer %q is shown as %q", line, got)
		}
	}
}

func TestBracketsAreShownAsTheyAreAcrossColours(t *testing.T) {
	// the bracket and what makes it look like a tag are in runs of different colours; the tag between them is a tag
	for _, line := range []string{"\x1b[31m[\x1b[32mred]", "\x1b[1m[a\x1b[0m]", "a\x1b[7m[\x1b[0m]b"} {
		want := "[red]"
		switch {
		case strings.Contains(line, "[a"):
			want = "[a]"
		case strings.HasPrefix(line, "a"):
			want = "a[]b"
		}
		vt := NewVTScreenSized(80, 5)
		vt.Feed(line)
		if got := shownByTview(vt.Render()); got != want {
			t.Errorf("%q is shown as %q, want %q", line, got, want)
		}
	}
}

func TestEscapeTviewTagsLeavesWhatIsNotATagAlone(t *testing.T) {
	for in, want := range map[string]string{
		"a[b":      "a[b",
		"[":        "[",
		"[]":       "[]",
		"[[":       "[[",
		"no ]":     "no ]",
		"[red]":    "[red[]",
		"[+]":      "[+]",
		"x[y]z[w]": "x[y[]z[w[]",
	} {
		if got := escapeTviewTags(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
