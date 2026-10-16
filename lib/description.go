package lib

import (
	"fmt"
	"sort"
	"strings"
)

// AnnotationDescription builds a recording's description from the annotations marked "show in nav menu", one line each, in
// time order: the time as a link that opens the recording there, then the annotation's first line (annotations are Markdown, so
// its own emphasis is kept):
//
//	[**[00:09]**](https://example.com/watch/<id>?t=9) **Step 0 – Back up the config.**
//
// watchURL is the recording's page, without the query. The description is held to maxChars (0 = no limit): lines that would take
// it past that are left out, latest first, and used says how many of the total were kept so the caller can tell the author.
func AnnotationDescription(annotations []Annotation, watchURL string, maxChars int) (description string, used, total int) {
	nav := make([]Annotation, 0, len(annotations))
	for _, a := range annotations {
		if a.ShowInNav && firstLine(a.Text) != "" {
			nav = append(nav, a)
		}
	}
	sort.SliceStable(nav, func(i, j int) bool { return nav[i].Timestamp < nav[j].Timestamp })

	var b strings.Builder
	for _, a := range nav {
		secs := a.Timestamp / 1000
		line := fmt.Sprintf("[**[%s]**](%s?t=%d) %s", clockLabel(secs), watchURL, secs, firstLine(a.Text))
		if maxChars > 0 && len([]rune(b.String()))+len([]rune(line))+1 > maxChars {
			break
		}
		if used > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		used++
	}
	return b.String(), used, len(nav)
}

// firstLine is an annotation's title: its first line that isn't blank, without a heading's leading #s (a description is a list,
// and a heading in it would break the list).
func firstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
			return l
		}
	}
	return ""
}

// clockLabel is 00:09, 12:34 or 1:02:03.
func clockLabel(secs int64) string {
	if secs >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", secs/3600, secs%3600/60, secs%60)
	}
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}
