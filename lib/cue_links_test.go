package lib

import (
	"reflect"
	"strings"
	"testing"
)

func TestACueCardsLinksAreTheOnlyMarkdownUnderstood(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []CueSegment
	}{
		"nothing": {"Just text.", []CueSegment{{Text: "Just text."}}},
		"empty":   {"", nil},
		"a link on a line of its own": {"[Download Trecs](https://github.com/solworktech/trecs/releases)",
			[]CueSegment{{Text: "Download Trecs", URL: "https://github.com/solworktech/trecs/releases"}}},
		"a link in a sentence": {"Get it [here](http://example.com/a?b=c#d) now.",
			[]CueSegment{{Text: "Get it "}, {Text: "here", URL: "http://example.com/a?b=c#d"}, {Text: " now."}}},
		"two links": {"[A](https://a.example) or [B](https://b.example)",
			[]CueSegment{{Text: "A", URL: "https://a.example"}, {Text: " or "}, {Text: "B", URL: "https://b.example"}}},
		"on lines of their own": {"Intro\n[A](https://a.example)\nOutro",
			[]CueSegment{{Text: "Intro\n"}, {Text: "A", URL: "https://a.example"}, {Text: "\nOutro"}}},
		"other markdown is text": {"`code` and # heading and - a list and > a quote", []CueSegment{{Text: "`code` and # heading and - a list and > a quote"}}},
		"an image is text":       {"![logo](https://example.com/l.png)", []CueSegment{{Text: "![logo](https://example.com/l.png)"}}},
		"not a web link is text": {"[Open](file:///etc/passwd) [Run](javascript:alert(1)) [App](zoommtg://x)",
			[]CueSegment{{Text: "[Open](file:///etc/passwd) [Run](javascript:alert(1)) [App](zoommtg://x)"}}},
		"a label cannot span lines":    {"[a\nb](https://x.example)", []CueSegment{{Text: "[a\nb](https://x.example)"}}},
		"a url cannot have a space":    {"[a](https://x.example/a b)", []CueSegment{{Text: "[a](https://x.example/a b)"}}},
		"an unclosed link is text":     {"[a](https://x.example", []CueSegment{{Text: "[a](https://x.example"}}},
		"brackets that are not a link": {"[red] and [x][y]", []CueSegment{{Text: "[red] and [x][y]"}}},
		"a link after an image's alt":  {"![a](https://i.example/a.png) [b](https://b.example)", []CueSegment{{Text: "![a](https://i.example/a.png) "}, {Text: "b", URL: "https://b.example"}}},
	} {
		if got := ParseCueText(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q\n got  %+v\n want %+v", name, tc.text, got, tc.want)
		}
	}
}

func TestCueLinksAreTheLinksInOrder(t *testing.T) {
	got := CueLinks("x [A](https://a.example) y [B](https://b.example) ![c](https://c.example/c.png) z")
	if len(got) != 2 || got[0].Text != "A" || got[1].URL != "https://b.example" {
		t.Errorf("%+v", got)
	}
}

// ---- emphasis -----------------------------------------------------------------------------------------------------------

func seg(text string, style string) CueSegment {
	return CueSegment{Text: text, Bold: strings.Contains(style, "b"), Italic: strings.Contains(style, "i")}
}

func TestBoldAndItalicAreUnderstood(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []CueSegment
	}{
		"bold":                   {"a **bold** word", []CueSegment{seg("a ", ""), seg("bold", "b"), seg(" word", "")}},
		"bold with underscores":  {"a __bold__ word", []CueSegment{seg("a ", ""), seg("bold", "b"), seg(" word", "")}},
		"italic":                 {"an *italic* word", []CueSegment{seg("an ", ""), seg("italic", "i"), seg(" word", "")}},
		"italic with underscore": {"an _italic_ word", []CueSegment{seg("an ", ""), seg("italic", "i"), seg(" word", "")}},
		"both":                   {"***both***", []CueSegment{seg("both", "bi")}},
		"both with underscores":  {"___both___", []CueSegment{seg("both", "bi")}},
		"italic in bold":         {"**bold and *italic* and bold**", []CueSegment{seg("bold and ", "b"), seg("italic", "bi"), seg(" and bold", "b")}},
		"bold in italic":         {"*italic and **bold** and italic*", []CueSegment{seg("italic and ", "i"), seg("bold", "bi"), seg(" and italic", "i")}},
		"two of them":            {"**a** and **b**", []CueSegment{seg("a", "b"), seg(" and ", ""), seg("b", "b")}},
		"across a line break":    {"**one\ntwo**", []CueSegment{seg("one\ntwo", "b")}},
		"inside a word with *":   {"un*frigging*believable", []CueSegment{seg("un", ""), seg("frigging", "i"), seg("believable", "")}},
		"punctuation":            {"(**bold**), *it*.", []CueSegment{seg("(", ""), seg("bold", "b"), seg("), ", ""), seg("it", "i"), seg(".", "")}},
	} {
		if got := ParseCueText(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q\n got  %+v\n want %+v", name, tc.text, got, tc.want)
		}
	}
}

func TestWhatIsNotEmphasisStaysTheTextItWasWrittenAs(t *testing.T) {
	for name, text := range map[string]string{
		"arithmetic":                    "2 * 3 * 4 = 24",
		"a name with underscores":       "call snake_case_name and other_name_too",
		"a lone delimiter":              "a * b",
		"unclosed bold":                 "**never closed",
		"unclosed italic":               "*never closed",
		"opened with a space":           "** not bold **",
		"mismatched lengths":            "**a* and *b",
		"a run of four":                 "****not****",
		"across a blank line":           "**one\n\ntwo**",
		"across a blank line, indented": "*one\n   \ntwo*",
		"a glob":                        "rm *.txt and ls *.go",
		"a path":                        `C:\Users\me and /tmp/_x_y`,
	} {
		got := ParseCueText(text)
		if len(got) != 1 || got[0].Text != text || got[0].Bold || got[0].Italic {
			t.Errorf("%s: %q became %+v", name, text, got)
		}
	}
}

func TestABackslashMakesTheNextDelimiterOrBracketText(t *testing.T) {
	for text, want := range map[string]string{
		`\*not italic\*`:                "*not italic*",
		`a \_b\_ c`:                     "a _b_ c",
		`\\ is one backslash`:           `\ is one backslash`,
		`a \[link\](https://x.example)`: "a [link](https://x.example)",
		`\q is a backslash and q`:       `\q is a backslash and q`,
	} {
		got := ParseCueText(text)
		var joined string
		for _, s := range got {
			joined += s.Text
			if s.URL != "" || s.Bold || s.Italic {
				t.Errorf("%q: %+v", text, got)
			}
		}
		if joined != want {
			t.Errorf("%q: %q, want %q", text, joined, want)
		}
	}
}

func TestLinksAndEmphasisTogether(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []CueSegment
	}{
		"a link with an emphasised label":           {"[**Download** Trecs](https://x.example)", []CueSegment{{Text: "Download Trecs", URL: "https://x.example"}}},
		"a link in bold":                            {"**see [docs](https://d.example) now**", []CueSegment{seg("see ", "b"), {Text: "docs", URL: "https://d.example", Bold: true}, seg(" now", "b")}},
		"bold around a link alone":                  {"**[docs](https://d.example)**", []CueSegment{{Text: "docs", URL: "https://d.example", Bold: true}}},
		"an image with emphasis in its url is text": {"![a](https://i.example/a_b_c.png)", []CueSegment{seg("![a](https://i.example/a_b_c.png)", "")}},
		"the url's underscores are not emphasis":    {"[a](https://x.example/a_b_c) and _it_", []CueSegment{{Text: "a", URL: "https://x.example/a_b_c"}, seg(" and ", ""), seg("it", "i")}},
	} {
		if got := ParseCueText(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q\n got  %+v\n want %+v", name, tc.text, got, tc.want)
		}
	}
}
