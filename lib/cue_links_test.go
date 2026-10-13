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
		"other markdown is text": {"# heading and - a list and > a quote and ~~strike~~", []CueSegment{{Text: "# heading and - a list and > a quote and ~~strike~~"}}},
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

// ---- code ---------------------------------------------------------------------------------------------------------------------

func code(text string, style string) CueSegment {
	s := seg(text, style)
	s.Code = true
	return s
}

func block(text string) CueSegment {
	s := code(text, "")
	s.Block = true
	return s
}

func TestCodeSpansAreCodeAsTheyAreWritten(t *testing.T) {
	cases := map[string]struct {
		text string
		want []CueSegment
	}{
		"a span":                      {"use `ls -la` here", []CueSegment{seg("use ", ""), code("ls -la", ""), seg(" here", "")}},
		"two spans":                   {"`a` and `b`", []CueSegment{code("a", ""), seg(" and ", ""), code("b", "")}},
		"a longer fence of backticks": {"``a ` b``", []CueSegment{code("a ` b", "")}},
		"one space off each end":      {"` a `", []CueSegment{code("a", "")}},
		"more than one is kept":       {"`  a  `", []CueSegment{code(" a ", "")}},
		"all spaces is kept":          {"`  `", []CueSegment{code("  ", "")}},
		"a line break is a space":     {"`a\nb`", []CueSegment{code("a b", "")}},
		"emphasis in it is text":      {"`**not bold** and _not italic_`", []CueSegment{code("**not bold** and _not italic_", "")}},
		"a link in it is text":        {"`[a](https://x.example)`", []CueSegment{code("[a](https://x.example)", "")}},
		"code in bold is bold":        {"**`code`**", []CueSegment{code("code", "b")}},
		"code in bold, with text":     {"**use `ls` now**", []CueSegment{seg("use ", "b"), code("ls", "b"), seg(" now", "b")}},
		"code in italics":             {"*`code`*", []CueSegment{code("code", "i")}},
		"code in a link's label":      {"[`code`](https://x.example)", []CueSegment{CueSegment{Text: "code", URL: "https://x.example"}}},
		"a name with underscores":     {"call `snake_case_name` now", []CueSegment{seg("call ", ""), code("snake_case_name", ""), seg(" now", "")}},
		"an escaped backtick is text": {"\\`not code\\`", []CueSegment{seg("`not code`", "")}},
	}
	for name, tc := range cases {
		if got := ParseCueText(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q\n got  %+v\n want %+v", name, tc.text, got, tc.want)
		}
	}
}

func TestBackticksThatAreNotCodeAreTheTextTheyWereWrittenAs(t *testing.T) {
	for name, text := range map[string]string{
		"an unclosed backtick":            "a ` b",
		"a run that is not matched":       "``a`",
		"a span across a blank line":      "`one\n\ntwo`",
		"a span across an indented blank": "`one\n   \ntwo`",
		"an unclosed fence":               "```\ncode with no end",
		"a fence closed by too few":       "````\ncode\n```",
		"a lone fence line":               "```",
	} {
		got := ParseCueText(text)
		if len(got) != 1 || got[0].Text != text || got[0].Code {
			t.Errorf("%s: %q became %+v", name, text, got)
		}
	}
}

func TestFencedBlocksAreBlocksOfCodeWhateverIsInThem(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []CueSegment
	}{
		"a block":                              {"```\ncode\n```", []CueSegment{block("code")}},
		"its language is not shown":            {"```go\nfmt.Println()\n```", []CueSegment{block("fmt.Println()")}},
		"blank lines in it":                    {"```\na\n\nb\n```", []CueSegment{block("a\n\nb")}},
		"indentation is kept":                  {"```\n  indented\n\ttabbed\n```", []CueSegment{block("  indented\n\ttabbed")}},
		"markdown in it is text":               {"```\n**not bold** [a](https://x.example) `x`\n```", []CueSegment{block("**not bold** [a](https://x.example) `x`")}},
		"text around it":                       {"Intro\n```\ncode\n```\nOutro", []CueSegment{seg("Intro\n", ""), block("code"), seg("\nOutro", "")}},
		"a longer closing fence":               {"```\ncode\n`````", []CueSegment{block("code")}},
		"an indented closing fence":            {"```\ncode\n   ```", []CueSegment{block("code")}},
		"several lines":                        {"```\none\ntwo\nthree\n```", []CueSegment{block("one\ntwo\nthree")}},
		"two blocks":                           {"```\na\n```\n```\nb\n```", []CueSegment{block("a"), seg("\n", ""), block("b")}},
		"not at the start of a line is a span": {"see ```\ncode\n```", []CueSegment{seg("see ", ""), code("code", "")}},
	} {
		if got := ParseCueText(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q\n got  %+v\n want %+v", name, tc.text, got, tc.want)
		}
	}
}

func TestACodeSpanStopsEmphasisReachingIntoOrAcrossIt(t *testing.T) {
	// the asterisks in the span are not delimiters, so the ones outside it do not find each other through it
	got := ParseCueText("*a `*` b*")
	want := []CueSegment{seg("a ", "i"), code("*", "i"), seg(" b", "i")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCueFenceRegionsDivideATextAtItsBlocks(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []CueTextRegion
	}{
		"no block":         {"just text\nand `code`", []CueTextRegion{{Text: "just text\nand `code`"}}},
		"only a block":     {"```\na < b\n```", []CueTextRegion{{Text: "```\na < b\n```", Fence: true}}},
		"between text":     {"Before\n```sh\nls\n```\nAfter", []CueTextRegion{{Text: "Before\n"}, {Text: "```sh\nls\n```", Fence: true}, {Text: "\nAfter"}}},
		"two blocks":       {"```\na\n```\n```\nb\n```", []CueTextRegion{{Text: "```\na\n```", Fence: true}, {Text: "\n"}, {Text: "```\nb\n```", Fence: true}}},
		"unclosed is text": {"```\nnever closed", []CueTextRegion{{Text: "```\nnever closed"}}},
		"empty":            {"", nil},
	} {
		if got := CueFenceRegions(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q\n got  %+v\n want %+v", name, tc.text, got, tc.want)
		}
	}
}
