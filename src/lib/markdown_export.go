package lib

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// MarkdownFromCommands renders a recording's commands as a Markdown document: a
// title, a table of contents linking to each command, then for each command its
// heading, its annotation (if any) and its output. It matches the export the web
// player offers.
func MarkdownFromCommands(title string, commands []Command) string {
	if strings.TrimSpace(title) == "" {
		title = "Terminal recording"
	}
	type entry struct {
		cmd    Command
		title  string
		anchor string
	}
	slug := newHeadingSlugger()
	var entries []entry
	for _, c := range commands {
		if strings.TrimSpace(c.InputText) == "" {
			continue
		}
		t := CollapseCommand(c.InputText)
		entries = append(entries, entry{c, t, slug(t)})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	for i, e := range entries {
		fmt.Fprintf(&b, "%d. [%s](#%s)\n", i+1, mdInlineCode(e.title), e.anchor)
	}
	if len(entries) > 0 {
		b.WriteString("\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "### %s\n\n", mdInlineCode(e.title))
		if strings.Contains(e.cmd.InputText, "\n") {
			b.WriteString(mdBlock("bash", e.cmd.InputText) + "\n\n") // as typed: line breaks and all
		}
		if note := mdAnnotation(e.cmd.Annotation.Text); note != "" {
			b.WriteString(note + "\n\n")
		}
		if isInteractiveOutput(e.cmd.OutputTextRaw) {
			b.WriteString("_(interactive full-screen session - output not captured)_\n\n")
		} else if out := CleanOutput(e.cmd.OutputTextRaw); out != "" {
			b.WriteString(mdBlock("console", out) + "\n\n")
		}
	}
	return strings.TrimRight(regexp.MustCompile(`\n{3,}`).ReplaceAllString(b.String(), "\n\n"), "\n") + "\n"
}

var (
	altScreenRe = regexp.MustCompile("\x1b\\[\\?(?:1049|1047|47)h")
	charsetRe   = regexp.MustCompile("\x1b[()*+][0-9A-Za-z]")
)

// isInteractiveOutput reports whether the command switched to the alternate
// screen: vim, htop, less... Its output is a screen, not text worth exporting.
func isInteractiveOutput(raw string) bool { return altScreenRe.MatchString(raw) }

// CleanOutput is the plain text of what a command printed. It is not a full
// terminal emulation - fine for ordinary command output: escape sequences
// (colours, charset selection, titles) are dropped, tabs expand to 8-column
// stops, CRLF becomes a newline and a carriage return overwrites the line so
// far (a progress bar shows its final state). No carriage return is left in it.
func CleanOutput(raw string) string {
	const tab = "\uE000" // kept through stripEscapeSequences, which drops control characters
	text := stripEscapeSequences(strings.ReplaceAll(charsetRe.ReplaceAllString(raw, ""), "\t", tab))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if k := strings.LastIndex(l, "\r"); k >= 0 {
			l = l[k+1:]
		}
		var sb strings.Builder
		col := 0
		for _, r := range l {
			if string(r) == tab {
				n := 8 - col%8
				sb.WriteString(strings.Repeat(" ", n))
				col += n
				continue
			}
			sb.WriteRune(r)
			col++
		}
		lines[i] = strings.TrimRight(sb.String(), " \t\r")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// mdBlock is a fenced code block whose fence is longer than any run of
// backticks inside it, so the content can never close it early.
func mdBlock(lang, text string) string {
	f := strings.Repeat("`", maxBacktickRun(text, 2)+1)
	return f + lang + "\n" + text + "\n" + f
}

// mdInlineCode is inline code whose delimiter is longer than any backtick run
// inside it.
func mdInlineCode(text string) string {
	d := strings.Repeat("`", maxBacktickRun(text, 0)+1)
	pad := ""
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
		pad = " "
	}
	return d + pad + text + pad + d
}

func maxBacktickRun(s string, atLeast int) int {
	longest, run := atLeast, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}

// newHeadingSlugger makes heading anchors as GitHub (and the renderers that
// follow it) do: lower-cased, everything but letters, digits, hyphen,
// underscore and space dropped, spaces to hyphens; the second heading with the
// same text gets -1, the third -2...
func newHeadingSlugger() func(string) string {
	seen := map[string]int{}
	return func(text string) string {
		var sb strings.Builder
		for _, r := range strings.ToLower(text) {
			switch {
			case r == ' ':
				sb.WriteByte('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.M, r):
				sb.WriteRune(r)
			}
		}
		s := sb.String()
		n := seen[s]
		seen[s] = n + 1
		if n == 0 {
			return s
		}
		return fmt.Sprintf("%s-%d", s, n)
	}
}

var mdLineStartRe = regexp.MustCompile(`^([#>]|[-+*]\s|\d+[.)]\s)`)

// mdAnnotation is an annotation as Markdown paragraphs: lines trimmed (deep
// indentation would become a code block), the lines of a paragraph kept apart
// with hard breaks, blank lines separating paragraphs, and anything that would
// start a heading, quote or list at the start of a line escaped.
func mdAnnotation(text string) string {
	var paragraphs []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paragraphs = append(paragraphs, strings.Join(cur, "  \n"))
			cur = nil
		}
	}
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			flush()
			continue
		}
		l = strings.ReplaceAll(l, "<", "&lt;")
		if loc := mdLineStartRe.FindStringIndex(l); loc != nil {
			l = `\` + l
		}
		cur = append(cur, l)
	}
	flush()
	return strings.Join(paragraphs, "\n\n")
}
