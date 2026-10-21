package lib

import (
	"strings"
	"unicode/utf8"
)

// Carrying an edit of a command's output into the output as it was recorded.
//
// What the editor shows - and what the user edits - is the output as plain text: the escape sequences that
// colour it are not in it. Writing that text back would take the colours with it. So the edit is applied to the
// recorded bytes instead: the text before and after the old output is compared with the new, and only what
// changed is touched. Text that was not touched keeps every escape sequence it had; a character that replaces
// another takes its place - and with it its colour; text that is added where nothing was replaced goes right after
// the character before it, inside whatever colour that is in.

// maxDiffCells bounds the work of a comparison: a table of this many cells at most.
const maxDiffCells = 4_000_000

// rawTok is one piece of the recorded output: an escape sequence or other byte that is not text (mapped is
// false), or a character of the plain text - a newline being "\r\n" there.
type rawTok struct {
	raw    string
	mapped bool
}

// tokenizeRaw cuts recorded output into tokens, matching its characters one by one with the plain text it was
// cleaned to. It reports false if they do not line up (tabs are expanded and lines overwritten by carriage returns
// when it is cleaned, so some outputs do not): the caller then cannot say where an edit belongs.
func tokenizeRaw(raw, plain string) ([]rawTok, bool) {
	want := []rune(plain)
	next := 0
	var toks []rawTok
	for i := 0; i < len(raw); {
		if raw[i] == 0x1b {
			j := skipEscape(raw, i)
			toks = append(toks, rawTok{raw: raw[i:j]})
			i = j
			continue
		}
		if raw[i] == '\r' && i+1 < len(raw) && raw[i+1] == '\n' && next < len(want) && want[next] == '\n' {
			toks = append(toks, rawTok{raw: "\r\n", mapped: true})
			next++
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(raw[i:])
		mapped := next < len(want) && r == want[next]
		if mapped {
			next++
		}
		toks = append(toks, rawTok{raw: raw[i : i+size], mapped: mapped})
		i += size
	}
	return toks, next == len(want)
}

// A hunk says: a[aStart:aEnd] became b[bStart:bEnd].
type hunk struct{ aStart, aEnd, bStart, bEnd int }

// hunks lists where b differs from a, by longest common subsequence. Too much to compare is one hunk.
func hunks[T comparable](a, b []T) []hunk {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	x, y := a[p:len(a)-s], b[p:len(b)-s]
	n, m := len(x), len(y)
	switch {
	case n == 0 && m == 0:
		return nil
	case n == 0 || m == 0 || n*m > maxDiffCells:
		return []hunk{{p, len(a) - s, p, len(b) - s}}
	}
	// lcs[i*(m+1)+j]: the length of the longest common subsequence of x[i:] and y[j:]
	lcs := make([]int32, (n+1)*(m+1))
	at := func(i, j int) int32 { return lcs[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i*(m+1)+j] = at(i+1, j+1) + 1
			} else {
				lcs[i*(m+1)+j] = max(at(i+1, j), at(i, j+1))
			}
		}
	}
	var out []hunk
	open := false
	for i, j := 0, 0; i < n || j < m; {
		if i < n && j < m && x[i] == y[j] {
			open = false
			i++
			j++
			continue
		}
		if !open {
			out = append(out, hunk{p + i, p + i, p + j, p + j})
			open = true
		}
		h := &out[len(out)-1]
		if j < m && (i == n || at(i, j+1) >= at(i+1, j)) {
			j++
			h.bEnd = p + j
		} else {
			i++
			h.aEnd = p + i
		}
	}
	return out
}

func splitLinesKeep(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// runeHunks compares two texts: by lines first, then by characters within the lines that changed, so a long
// output is not compared character by character where nothing happened.
func runeHunks(oldText, newText string) []hunk {
	oldLines, newLines := splitLinesKeep(oldText), splitLinesKeep(newText)
	offsets := func(lines []string) []int {
		off := make([]int, len(lines)+1)
		for i, l := range lines {
			off[i+1] = off[i] + utf8.RuneCountInString(l)
		}
		return off
	}
	oldOff, newOff := offsets(oldLines), offsets(newLines)
	oldRunes, newRunes := []rune(oldText), []rune(newText)
	var out []hunk
	for _, lh := range hunks(oldLines, newLines) {
		aS, aE, bS, bE := oldOff[lh.aStart], oldOff[lh.aEnd], newOff[lh.bStart], newOff[lh.bEnd]
		for _, rh := range hunks(oldRunes[aS:aE], newRunes[bS:bE]) {
			out = append(out, hunk{aS + rh.aStart, aS + rh.aEnd, bS + rh.bStart, bS + rh.bEnd})
		}
	}
	return out
}

// editedRaw is the recorded output with the edit applied: oldPlain is what raw cleans to, newPlain what it was
// edited into. Newlines in the result are the terminal's "\r\n". It is checked to clean to the edited text; if
// it cannot be made to, the second result is false and the caller has only the plain text to write.
func editedRaw(raw, oldPlain, newPlain string) (string, bool) {
	toks, ok := tokenizeRaw(raw, oldPlain)
	if !ok {
		return "", false
	}
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	newRunes := []rune(newPlain)
	deleted := make([]bool, utf8.RuneCountInString(oldPlain))
	replacement := map[int]string{} // text that takes the place of the old character at that index
	after := map[int]string{}       // text added right after the old character at that index
	lead := ""                      // text added before everything
	for _, h := range runeHunks(oldPlain, newPlain) {
		for k := h.aStart; k < h.aEnd; k++ {
			deleted[k] = true
		}
		text := string(newRunes[h.bStart:h.bEnd])
		switch {
		case h.aEnd > h.aStart:
			replacement[h.aStart] = text
		case h.aStart == 0:
			lead += text
		default:
			after[h.aStart-1] += text
		}
	}

	var sb strings.Builder
	sb.WriteString(crlf(lead))
	k := 0
	for _, t := range toks {
		if !t.mapped {
			sb.WriteString(t.raw)
			continue
		}
		if deleted[k] {
			sb.WriteString(crlf(replacement[k]))
		} else {
			sb.WriteString(t.raw)
		}
		sb.WriteString(crlf(after[k]))
		k++
	}
	got := sb.String()
	if CleanOutput(got) != CleanOutput(crlf(newPlain)) {
		return "", false
	}
	return got, true
}

// editedOutputFrames is the command's output frames after its output text was edited. The shell's marks (OSC 133: where the output
// begins and where the command ends) are kept exactly as they were - without them a command is no longer one, and the one before it
// swallows it - and so are the colours of the text the edit did not touch (see editedRaw). All the text goes to where the first of it
// was (after the mark that begins the output); a command whose output was emptied keeps its marks and no text; one that had none and
// has some gets it just before the mark that ends it.
func editedOutputFrames(cmd *Command) []TerminalFrame {
	skip := make(map[int]bool, len(cmd.TypedAhead))
	for _, i := range cmd.TypedAhead {
		skip[i] = true
	}
	var kept []TerminalFrame
	for i, f := range cmd.OutputFrames {
		if !skip[i] {
			kept = append(kept, f)
		}
	}
	pieces := splitAtMarks(kept)
	var oldRaw strings.Builder
	for _, p := range pieces {
		if p.kind == 0 {
			oldRaw.WriteString(p.data)
		}
	}
	newRaw, ok := editedRaw(oldRaw.String(), deriveOutputText(cmd), cmd.OutputText)
	if !ok {
		newRaw = strings.ReplaceAll(cmd.OutputText, "\n", "\r\n")
	}

	// where the text goes: the first text after the mark that begins the output, else the first text there is
	at, sawC := -1, false
	for i, p := range pieces {
		if p.kind == 'C' {
			sawC = true
		}
		if p.kind == 0 && (sawC || at < 0) {
			at = i
			if sawC {
				break
			}
		}
	}
	if at < 0 && newRaw != "" {
		ins := markPiece{frameIdx: -1, frame: TerminalFrame{Timestamp: cmd.EndTime}}
		pos := len(pieces)
		for i, p := range pieces {
			if p.kind == 'D' {
				pos = i
				break
			}
		}
		switch {
		case pos < len(pieces):
			ins.frame = pieces[pos].frame
		case len(pieces) > 0:
			ins.frame = pieces[len(pieces)-1].frame
		}
		pieces = append(pieces[:pos], append([]markPiece{ins}, pieces[pos:]...)...)
		at = pos
	}

	var out []TerminalFrame
	lastIdx := -2
	for i, p := range pieces {
		if p.kind == 0 {
			if i != at || newRaw == "" {
				continue
			}
			p.data = newRaw
		}
		if len(out) > 0 && p.frameIdx == lastIdx && p.frameIdx >= 0 {
			out[len(out)-1].Data += p.data
			continue
		}
		out = append(out, p.asFrame())
		lastIdx = p.frameIdx
	}
	return out
}
