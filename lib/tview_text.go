package lib

import (
	"regexp"
	"strings"
)

// The screens are handed to tview as text with colour tags ("[fg:bg:attrs]") between runs of characters. A character that is
// a "[" is just a "[" to tview: only a "[" that starts something that looks like a tag - "[red]", "[+]" - and ends in "]" would be
// taken for one, and that is escaped by putting one more "[" before its "]" ("[red[]"), as tview's own Escape does. (It is not escaped
// by doubling the "[": tview prints "[[" as two brackets, so every "[" on the screen, in a prompt or in vim's "[+]", was drawn twice.)
var tviewTagLookalike = regexp.MustCompile(`(\[[a-zA-Z0-9_,;: \-\."#]+\[*)\]`)

// escapeTviewTags is tview.Escape: text that is printed as it is, with nothing taken for a tag.
func escapeTviewTags(s string) string {
	if !strings.Contains(s, "]") {
		return s
	}
	return tviewTagLookalike.ReplaceAllString(s, "$1[]")
}

// tviewRun is the characters between two tags (or line ends), put out escaped when the run is over.
type tviewRun struct{ b strings.Builder }

func (r *tviewRun) add(ch rune) { r.b.WriteRune(ch) }

// flush writes the run to out and starts another.
func (r *tviewRun) flush(out *strings.Builder) {
	if r.b.Len() == 0 {
		return
	}
	out.WriteString(escapeTviewTags(r.b.String()))
	r.b.Reset()
}
