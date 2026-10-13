package lib

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The text of a cue card, and of a timed annotation, is Markdown, of which four things are understood: a link, which is a call to
// action - a button in a card in the TUI, a link on the web - bold and italics, and code. Nothing else of Markdown is interpreted,
// here or by whatever shows the text: a line that begins with # or - is text.
//
// What they are is defined here, once, so that what shows a card agrees with every other that shows one.
//
// Links
//
//   - [label](url), on one line: the label has no brackets, the url no spaces and no parentheses. The label may have
//     emphasis; what is shown is the label without its markers.
//   - the url is http or https. A link to anything else - file:, javascript:, a scheme that opens an application - is not a
//     link but the text it was written as: a recording can be shared, and what is in a card must not be able to start
//     anything on the machine of whoever plays it.
//   - ![alt](url) is an image, which is not supported: it is text.
//
// Emphasis
//
//   - **bold** and __bold__, *italic* and _italic_, ***both*** and ___both___; one can be in another, with the delimiters of
//     different lengths: **bold with *italic* in it**.
//   - An opening delimiter is followed by something that is not a space, a closing one is preceded by something that is not:
//     2 * 3 * 4 is arithmetic. The underscore is stricter, as in CommonMark: it does not open or close within a word, so that
//     snake_case_name is a name. The asterisk may.
//   - A delimiter that is not matched by one of the same kind and length is the text it was written as, and emphasis does not
//     reach across a blank line.
//   - A backslash before one of \ * _ [ ] ( ) ` is that character, and no more than that: \*not italic\*. Before anything else it
//     is a backslash.
//
// Code
//
//   - `code` is code: a run of backticks of any length, to the next run of exactly that length (“a ` b“ is a ` b). What is
//     between them is text, as it is written - no emphasis, no link, no escapes - with the line breaks in it as spaces, and one
//     space off each end if there is one on both. A run with no match is the backticks it was written as, and a span does not
//     reach across a blank line.
//   - A fenced block is a line of three or more backticks - and what is on it, which is not shown - to a line of as many or
//     more, which can have blank lines between: ```go ... ```. What is between is the block, as written, each line of it as it
//     is. Without the closing line it is not a block.
//   - Code is shown apart from the text: in blue in the TUI. It can be in bold or italics, which are the code's.
var cueLinkAtStart = regexp.MustCompile(`^\[([^\[\]\n]+)\]\((https?://[^\s()]+)\)`)

// CueSegment is a piece of the text of a cue card: plain text, or - when URL is set - a link, whose label is Text. Bold and
// Italic say how it is to be shown (a link in bold text is a bold link).
type CueSegment struct {
	Text   string
	URL    string
	Bold   bool
	Italic bool
	Code   bool // the text is code: shown as it is written
	Block  bool // and a fenced block, which is lines of its own
}

type cueAtom struct {
	kind     byte // 'l' literal, 'd' delimiter run, 'k' link, 'c' code, 'b' a blank line: a barrier for emphasis
	block    bool // of code: a fenced block
	text     string
	ch       rune // of a delimiter run
	n        int  // its length
	url      string
	canOpen  bool
	canClose bool
	matched  bool
	opens    bool // of a matched run: it opens the pair; the other closes it
}

func cueIsEscapable(r rune) bool { return strings.ContainsRune("\\*_[]()`", r) }

// ParseCueText divides the text of a cue card into plain text and links, in order, each with its emphasis. The Text of the
// segments, joined, is the text without its Markdown: the emphasis markers are gone, and each link is its label.
func ParseCueText(text string) []CueSegment {
	atoms := cueScan(text)
	cueMatchEmphasis(atoms)

	var segments []CueSegment
	bold, italic := 0, 0
	add := func(s CueSegment) {
		s.Bold, s.Italic = bold > 0, italic > 0
		if last := len(segments) - 1; last >= 0 && s.URL == "" && segments[last].URL == "" && !s.Code && !segments[last].Code && segments[last].Bold == s.Bold && segments[last].Italic == s.Italic {
			segments[last].Text += s.Text
			return
		}
		segments = append(segments, s)
	}
	for _, a := range atoms {
		switch {
		case a.kind == 'k':
			add(CueSegment{Text: a.text, URL: a.url})
		case a.kind == 'c':
			add(CueSegment{Text: a.text, Code: true, Block: a.block})
		case a.kind == 'd' && a.matched:
			if a.opens {
				if a.n >= 2 {
					bold++
				}
				if a.n == 1 || a.n == 3 {
					italic++
				}
			} else {
				if a.n >= 2 {
					bold--
				}
				if a.n == 1 || a.n == 3 {
					italic--
				}
			}
		case a.kind == 'd':
			add(CueSegment{Text: a.text})
		case a.kind == 'l':
			add(CueSegment{Text: a.text})
		}
	}
	return segments
}

// cueScan reads the text left to right into atoms: characters, runs of * and _, links, and the places where a blank line is.
func cueScan(text string) []cueAtom {
	var atoms []cueAtom
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == '\\' && i+size < len(text):
			if next, nsize := utf8.DecodeRuneInString(text[i+size:]); cueIsEscapable(next) {
				atoms = append(atoms, cueAtom{kind: 'l', text: string(next)})
				i += size + nsize
				continue
			}
			atoms = append(atoms, cueAtom{kind: 'l', text: "\\"})
			i += size
		case r == '`':
			// A fenced block, if this is the start of a line, and otherwise - or if it is not one - a code span. Either is
			// one atom, with what is in it as it is written: nothing in it is emphasis, or a link.
			if i == 0 || text[i-1] == '\n' {
				if end, content, ok := cueFenceAt(text, i); ok {
					atoms = append(atoms, cueAtom{kind: 'c', text: content, block: true})
					i = end
					continue
				}
			}
			n := 1
			for i+n < len(text) && text[i+n] == '`' {
				n++
			}
			if closeAt, ok := cueFindCloser(text, i+n, n); ok && !cueHasBlankLine(text[i+n:closeAt]) {
				atoms = append(atoms, cueAtom{kind: 'c', text: cueSpanText(text[i+n : closeAt])})
				i = closeAt + n
				continue
			}
			atoms = append(atoms, cueAtom{kind: 'l', text: text[i : i+n]})
			i += n
		case r == '[':
			// not an image's (![..](..) is text), and the link's label has its own emphasis, without the markers
			isImage := len(atoms) > 0 && atoms[len(atoms)-1].kind == 'l' && atoms[len(atoms)-1].text == "!"
			if m := cueLinkAtStart.FindStringSubmatch(text[i:]); m != nil && !isImage {
				label := cueLabel(m[1])
				atoms = append(atoms, cueAtom{kind: 'k', text: label, url: m[2]})
				i += len(m[0])
				continue
			}
			atoms = append(atoms, cueAtom{kind: 'l', text: "["})
			i += size
		case r == '*' || r == '_':
			n := 1
			for i+n < len(text) && rune(text[i+n]) == r {
				n++
			}
			atoms = append(atoms, cueAtom{kind: 'd', text: text[i : i+n], ch: r, n: n})
			i += n
		case r == '\n':
			// a blank line ends what emphasis could reach
			rest := strings.TrimLeft(text[i+size:], " \t\r")
			if strings.HasPrefix(rest, "\n") {
				atoms = append(atoms, cueAtom{kind: 'b'})
			}
			atoms = append(atoms, cueAtom{kind: 'l', text: "\n"})
			i += size
		default:
			atoms = append(atoms, cueAtom{kind: 'l', text: string(r)})
			i += size
		}
	}
	return atoms
}

var cueBlankLine = regexp.MustCompile(`\n[ \t\r]*\n`)

func cueHasBlankLine(s string) bool { return cueBlankLine.MatchString(s) }

// cueFindCloser is where, from the byte at from, the next run of exactly n backticks begins: the end of a code span that began with
// a run of n. A run of another length is not it, and is passed over.
func cueFindCloser(text string, from, n int) (int, bool) {
	for j := from; j < len(text); {
		if text[j] != '`' {
			j++
			continue
		}
		m := 1
		for j+m < len(text) && text[j+m] == '`' {
			m++
		}
		if m == n {
			return j, true
		}
		j += m
	}
	return 0, false
}

// cueSpanText is what a code span says: its line breaks are spaces, and if it begins and ends with a space - and is not all of
// them - one of each is for the backticks to stand off from it, and is not shown.
func cueSpanText(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	if len(s) >= 2 && s[0] == ' ' && s[len(s)-1] == ' ' && strings.TrimSpace(s) != "" {
		s = s[1 : len(s)-1]
	}
	return s
}

// cueFenceAt reads a fenced block that begins at byte i, which is the start of a line: three or more backticks and what is on the
// line after them (not backticks: then it is a code span), lines, and a line of nothing but as many backticks or more. It returns
// where the block ends - the end of its last line, not the line break after it - and what is between the fences.
func cueFenceAt(text string, i int) (end int, content string, ok bool) {
	n := 0
	for i+n < len(text) && text[i+n] == '`' {
		n++
	}
	if n < 3 {
		return 0, "", false
	}
	eol := strings.IndexByte(text[i:], '\n')
	if eol < 0 || strings.ContainsRune(text[i+n:i+eol], '`') {
		return 0, "", false
	}
	bodyStart := i + eol + 1
	for lineStart := bodyStart; lineStart <= len(text); {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		if t := strings.TrimSpace(text[lineStart:lineEnd]); len(t) >= n && strings.Trim(t, "`") == "" {
			body := text[bodyStart:lineStart]
			return lineEnd, strings.TrimSuffix(strings.TrimSuffix(body, "\n"), "\r"), true
		}
		if lineEnd >= len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return 0, "", false
}

// CueTextRegion is a piece of the text of a card or an annotation, as it was written: a fenced block, or the text between them.
type CueTextRegion struct {
	Text  string
	Fence bool
}

// CueFenceRegions divides a text into fenced blocks and what is between them, as it was written - for what must leave a block as it
// is: it has no Markdown of its own to be escaped.
func CueFenceRegions(text string) []CueTextRegion {
	var regions []CueTextRegion
	at := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '`' || (i != 0 && text[i-1] != '\n') {
			continue
		}
		end, _, ok := cueFenceAt(text, i)
		if !ok {
			// not a block: the run of backticks is passed over, so that its second and third are not tried as a start
			for i+1 < len(text) && text[i+1] == '`' {
				i++
			}
			continue
		}
		if i > at {
			regions = append(regions, CueTextRegion{Text: text[at:i]})
		}
		regions = append(regions, CueTextRegion{Text: text[i:end], Fence: true})
		at = end
		i = end - 1
	}
	if at < len(text) {
		regions = append(regions, CueTextRegion{Text: text[at:]})
	}
	return regions
}

// cueLabel is the text of a link's label: its emphasis taken out, and what it was around left.
func cueLabel(label string) string {
	var sb strings.Builder
	for _, s := range ParseCueText(label) {
		sb.WriteString(s.Text)
	}
	return sb.String()
}

// cueMatchEmphasis pairs the delimiter runs that open and close emphasis. A run is a pair only with one of the same character
// and the same length, and what is between a pair that is unmatched stays text.
func cueMatchEmphasis(atoms []cueAtom) {
	// what is on either side of a run, for the rules of what may open or close
	before := func(i int) (rune, bool) {
		if i == 0 || atoms[i-1].kind == 'b' {
			return 0, false
		}
		a := atoms[i-1]
		switch a.kind {
		case 'k', 'c':
			return '\uFFFC', true // a link, or code: not a space, not a letter
		case 'd':
			r, _ := utf8.DecodeLastRuneInString(a.text)
			return r, true
		}
		r, _ := utf8.DecodeLastRuneInString(a.text)
		return r, true
	}
	after := func(i int) (rune, bool) {
		if i+1 >= len(atoms) || atoms[i+1].kind == 'b' {
			return 0, false
		}
		a := atoms[i+1]
		if a.kind == 'k' || a.kind == 'c' {
			return '\uFFFC', true
		}
		r, _ := utf8.DecodeRuneInString(a.text)
		return r, true
	}
	isSpace := func(r rune, ok bool) bool { return !ok || unicode.IsSpace(r) }
	isWord := func(r rune, ok bool) bool { return ok && (unicode.IsLetter(r) || unicode.IsDigit(r)) }

	for i := range atoms {
		a := &atoms[i]
		if a.kind != 'd' {
			continue
		}
		pr, pok := before(i)
		nr, nok := after(i)
		a.canOpen = !isSpace(nr, nok)
		a.canClose = !isSpace(pr, pok)
		if a.ch == '_' { // not within a word
			a.canOpen = a.canOpen && !isWord(pr, pok)
			a.canClose = a.canClose && !isWord(nr, nok)
		}
		if a.n > 3 {
			a.canOpen, a.canClose = false, false
		}
	}

	type open struct{ at int }
	var stack []open
	for i := range atoms {
		a := &atoms[i]
		//nolint
		switch a.kind {
		case 'b':
			stack = nil
		case 'd':
			if a.canClose {
				found := false
				for s := len(stack) - 1; s >= 0; s-- {
					o := &atoms[stack[s].at]
					if o.ch == a.ch && o.n == a.n {
						o.matched, a.matched = true, true
						o.opens = true
						stack = stack[:s] // it, and what was opened after it and never closed, are done with
						found = true
						break
					}
				}
				if found {
					continue
				}
			}
			if a.canOpen {
				stack = append(stack, open{i})
			}
		}
	}
}

// CueLinks is the links of a cue card's text, in order.
func CueLinks(text string) []CueSegment {
	var links []CueSegment
	for _, s := range ParseCueText(text) {
		if s.URL != "" {
			links = append(links, s)
		}
	}
	return links
}
