package lib

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Search inside a recording: what each command printed, line by line, and when each line appeared. It is the web player's search
// (src/lib/searchIndex.js) in Go, and reads a recording the same way, so that a word found in one is found in the other.
//
//   - BuildSearchIndex turns the commands into lines (text, and the frame that completed it).
//   - CompileSearchPattern turns what the person typed into a regexp. Go's engine is RE2, not PCRE: the parts of PCRE that are only a
//     different spelling are translated; the rest (lookaround, back-references, possessive quantifiers...) is refused, with a message,
//     rather than quietly matching something else. RE2 matches in time proportional to the text, so no pattern can hang the editor.
//   - RunSearch finds the lines, with their neighbours for context, and cuts each into plain and highlighted pieces.

const (
	// SearchMinTerm is the fewest characters a search is made for.
	SearchMinTerm = 3
	// SearchMaxMatchLines is how many matching lines are listed; the rest are left out and the result says so.
	SearchMaxMatchLines = 200

	maxSearchLine   = 4000 // longer than this is not a line a person reads: matched up to here
	maxSearchPieces = 40   // highlighted stretches in one line
)

// SearchLine is a line of a command's text, the place it is at, and what the screen has to be made of to show it.
type SearchLine struct {
	Text string
	TS   int64 // when the frame that completed the line was written (ms)
	FI   int   // that frame's index in the recording's own frames; -1 if it is not known
	// Upto is how many of the command's frames - its prompt, then what was typed and printed, in the order of time (see CommandFrames) -
	// are on the screen with this line: the screen is made of the first Upto of them.
	Upto int
}

// SearchEntry is what can be searched in one command.
type SearchEntry struct {
	Idx     int    // the command's place in the list of commands
	Command string // what was typed, on one line
	Time    int64  // when the command began
	Input   SearchLine
	Lines   []SearchLine
}

// SearchPiece is a stretch of a line: plain, or what the pattern matched.
type SearchPiece struct {
	T   string
	Hit bool
}

// SearchRow is a line in a result: a match, or the line before or after one.
type SearchRow struct {
	SearchLine
	Pieces []SearchPiece // nil when the line is not a match
	Match  bool
	Input  bool // it is the command's own text
}

// SearchBlock is matched lines with the one line of context before and after each; runs that touch or overlap are one block.
type SearchBlock struct{ Rows []SearchRow }

// SearchResult is one command with what matched in it.
type SearchResult struct {
	Idx     int
	Command string
	Time    int64
	Blocks  []SearchBlock
}

// SearchOutcome is what RunSearch found.
type SearchOutcome struct {
	Results      []SearchResult
	MatchedLines int
	Truncated    bool // there were more matching lines than were listed
}

// CommandFrame is a frame of a command, and what it is: Kind is 'p' (the prompt), 'i' (typed) or 'o' (printed); Out is the place of an
// 'o' frame among the command's OutputFrames.
type CommandFrame struct {
	TerminalFrame
	Kind byte
	Out  int
}

// CommandFrames are the frames that make up a command on the screen, in the order they are drawn: the prompt that came before it, then
// what was typed and what was printed, by time (frames of the same time keep that order).
func CommandFrames(c *Command) []CommandFrame {
	var seq []CommandFrame
	if c.HasPrompt {
		seq = append(seq, CommandFrame{TerminalFrame: c.PromptFrame, Kind: 'p'})
	}
	for _, f := range c.InputFrames {
		seq = append(seq, CommandFrame{TerminalFrame: f, Kind: 'i'})
	}
	for k, f := range c.OutputFrames {
		seq = append(seq, CommandFrame{TerminalFrame: f, Kind: 'o', Out: k})
	}
	sort.SliceStable(seq, func(i, j int) bool { return seq[i].Timestamp < seq[j].Timestamp })
	return seq
}

type searchFrame struct {
	data string
	ts   int64
	fi   int
	upto int
}

// linesOfFrames are the lines of a stream of frames, as a person reads them off the screen (see screenLog): the escapes dropped, tabs
// to 8-column stops, a carriage return starting the line over, and the movements of a line editor followed, so that a line the shell
// drew again is the line it ended as. Each line has the frame in which it was completed. rows is the height of the terminal the
// frames were recorded in; 0 if it is not known.
func linesOfFrames(frames []searchFrame, rows int) []SearchLine {
	if len(frames) == 0 {
		return nil
	}
	var raw strings.Builder
	starts := make([]int, len(frames))
	for i, f := range frames {
		starts[i] = raw.Len()
		raw.WriteString(f.data)
	}
	fr := 0
	scr := newScreenLog(rows)
	scr.run(raw.String(), func(pos int) int {
		for fr+1 < len(frames) && starts[fr+1] <= pos {
			fr++
		}
		return fr
	})
	text, which := scr.lines()
	lines := make([]SearchLine, len(text))
	for i, t := range text {
		f := frames[which[i]]
		lines[i] = SearchLine{Text: t, TS: f.ts, FI: f.fi, Upto: f.upto}
	}
	return lines
}

// frameAtOrBefore is the index, in the whole recording, of the last frame at or before ts among frames[lo..hi]; -1 if there are none.
func frameAtOrBefore(frames []TerminalFrame, lo, hi int, ts int64) int {
	found := -1
	for lo <= hi {
		mid := (lo + hi) >> 1
		if frames[mid].Timestamp <= ts {
			found, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	return found
}

// locator finds the frames a command's own (possibly cut) frames come from, in order: the first at the same time that holds their
// data, from where the last one was. Several frames share a millisecond often enough that the time alone would put a typed command and
// its output at the same place.
func locator(frames []TerminalFrame, lo, hi int) func(TerminalFrame) int {
	from := lo
	return func(f TerminalFrame) int {
		j := from
		for j <= hi && frames[j].Timestamp < f.Timestamp {
			j++
		}
		for k := j; k <= hi && frames[k].Timestamp == f.Timestamp; k++ {
			if f.Data == "" || strings.Contains(frames[k].Data, f.Data) {
				from = k
				return k
			}
		}
		return frameAtOrBefore(frames, lo, hi, f.Timestamp)
	}
}

// BuildSearchIndex is what can be searched in each command: its text, and what it printed. A command that took over the screen (vim,
// htop...) printed nothing that can be read as lines, as in the exports.
//
// The frames a command holds are not always the recording's own (a recording with shell integration marks has them cut apart, and an
// output that was edited has frames of its own making), so the place of a line in the recording is found by its time, among the
// frames the command spans. frames is the whole recording, as the player has it.
func BuildSearchIndex(commands []Command, frames []TerminalFrame) []SearchEntry {
	return BuildSearchIndexFor(commands, frames, 0)
}

// BuildSearchIndexFor is BuildSearchIndex for a recording made in a terminal of rows rows (RecordingMeta.Height; 0 if it is not
// known): it is what tells a line of output from a program that draws on the screen where it likes.
func BuildSearchIndexFor(commands []Command, frames []TerminalFrame, rows int) []SearchEntry {
	var out []SearchEntry
	for idx := range commands {
		c := &commands[idx]
		command := CollapseCommand(c.InputText)
		if command == "" {
			continue // the command list leaves these out too
		}
		lo := max(0, c.FirstRawFrameIndex)
		hi := min(len(frames)-1, c.LastRawFrameIndex)
		if hi < lo {
			hi = len(frames) - 1
		}
		at := func(TerminalFrame) int { return -1 }
		time := c.StartTime
		if c.HasPrompt {
			time = c.PromptFrame.Timestamp
		}
		fi := -1
		if len(frames) > 0 && lo <= hi {
			l := locator(frames, lo, hi)
			at = l
			fi = frameAtOrBefore(frames, lo, hi, time)
		}

		seq := CommandFrames(c)
		skip := map[int]bool{}
		for _, k := range c.TypedAhead {
			skip[k] = true
		}
		input := SearchLine{Text: command, TS: time, FI: fi}
		for i := range seq {
			if seq[i].Timestamp <= time {
				input.Upto = i + 1
			}
		}
		lastInput := -1
		for i := range seq {
			if seq[i].Kind == 'i' {
				lastInput = i
			}
		}
		if lastInput >= 0 {
			f := seq[lastInput]
			input = SearchLine{Text: command, TS: f.Timestamp, FI: at(f.TerminalFrame), Upto: lastInput + 1}
		}

		var lines []SearchLine
		if !isInteractiveOutput(c.OutputTextRaw) {
			var own []searchFrame
			for i, f := range seq {
				if f.Kind != 'o' || skip[f.Out] {
					continue
				}
				own = append(own, searchFrame{data: f.Data, ts: f.Timestamp, fi: at(f.TerminalFrame), upto: i + 1})
			}
			lines = linesOfFrames(own, rows)
		}
		out = append(out, SearchEntry{Idx: idx, Command: command, Time: time, Input: input, Lines: lines})
	}
	return out
}

// ---- the pattern -----------------------------------------------------------------------------------------------------------------

// pcreInlineFlags are the (?i) (?s) (?m) at the very start of a pattern, which say how it is read as a whole.
var pcreInlineFlags = regexp.MustCompile(`^\(\?([ism]+)\)`)

// translatePCRE turns the spellings PCRE has and RE2 has not into RE2's, and reports the flags written at the start. What RE2 cannot
// do is left for the compiler to refuse.
func translatePCRE(src string) (source, flags string) {
	for {
		m := pcreInlineFlags.FindStringSubmatch(src)
		if m == nil {
			break
		}
		for _, f := range m[1] {
			if !strings.ContainsRune(flags, f) {
				flags += string(f)
			}
		}
		src = src[len(m[0]):]
	}
	var out strings.Builder
	inClass := false
	for i := 0; i < len(src); {
		ch := src[i]
		switch {
		case ch == '\\' && i+1 < len(src):
			n := src[i+1]
			rep := ""
			switch {
			case n == 'h':
				rep = map[bool]string{true: ` \t`, false: `[ \t]`}[inClass]
			case n == 'H' && !inClass:
				rep = `[^ \t]`
			case n == 'R' && !inClass:
				rep = `(?:\r\n|\n|\r)`
			case n == 'e':
				rep = `\x1b`
			case n == 'Z':
				rep = `$`
			case n == 'Q': // \Q...\E: RE2 reads it as PCRE does; copy it whole, so that what is inside is not translated
				end := strings.Index(src[i:], `\E`)
				if end < 0 {
					end = len(src) - i - 2
				}
				out.WriteString(src[i : i+end+2])
				i += end + 2
				continue
			}
			if rep != "" {
				out.WriteString(rep)
			} else {
				out.WriteString(src[i : i+2])
			}
			i += 2
			continue
		case inClass:
			if ch == ']' {
				inClass = false
			}
		case ch == '[':
			inClass = true
			out.WriteByte(ch)
			i++
			if i < len(src) && src[i] == '^' {
				out.WriteByte('^')
				i++
			}
			if i < len(src) && src[i] == ']' { // a ] first in a class is itself, in PCRE
				out.WriteString(`\]`)
				i++
			}
			continue
		case ch == '(' && strings.HasPrefix(src[i:], "(?'"):
			if end := strings.Index(src[i+3:], "'"); end > 0 { // (?'name'...) is (?P<name>...)
				out.WriteString("(?P<" + src[i+3:i+3+end] + ">")
				i += 3 + end + 1
				continue
			}
		}
		out.WriteByte(ch)
		i++
	}
	return out.String(), flags
}

var escapedChar = regexp.MustCompile(`\\.`)

func hasUpper(s string) bool {
	for _, r := range escapedChar.ReplaceAllString(s, "") {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// searchError is a message for the person who typed the term, and so is a sentence: it begins with a capital and may end with a stop.
type searchError string

func (e searchError) Error() string { return string(e) }

// CompileSearchPattern turns what was typed into a pattern. regex false: the term is text, to be found as it is. matchCase false: case
// counts only if the term has a capital ("smart case"). The error is meant to be shown to the person who typed the term.
func CompileSearchPattern(term string, regex, matchCase bool) (*regexp.Regexp, error) {
	if utf8.RuneCountInString(term) < SearchMinTerm || strings.TrimSpace(term) == "" {
		return nil, searchError(fmt.Sprintf("Type at least %d characters.", SearchMinTerm))
	}
	source, flags := regexp.QuoteMeta(term), ""
	if regex {
		source, flags = translatePCRE(term)
	}
	insensitive := strings.Contains(flags, "i") || (!matchCase && !hasUpper(term))
	var f string
	if insensitive {
		f += "i"
	}
	for _, c := range "sm" {
		if strings.ContainsRune(flags, c) {
			f += string(c)
		}
	}
	if f != "" {
		source = "(?" + f + ")" + source
	}
	re, err := regexp.Compile(source)
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		if strings.Contains(msg, "invalid or unsupported Perl syntax") {
			msg += " (this search uses Go's regular expressions: no lookaround, back-references or possessive quantifiers)"
		}
		return nil, searchError("Not a valid pattern: " + msg)
	}
	return re, nil
}

// ---- the search ------------------------------------------------------------------------------------------------------------------

// HighlightLine cuts the text of a line into pieces: what the pattern matched, and what is between. Only non-empty matches are hits.
// nil when there is none.
func HighlightLine(text string, re *regexp.Regexp) []SearchPiece {
	s := text
	if len(s) > maxSearchLine {
		cut := maxSearchLine
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	var pieces []SearchPiece
	at, hits := 0, 0
	for _, m := range re.FindAllStringIndex(s, -1) {
		if hits >= maxSearchPieces {
			break
		}
		if m[0] == m[1] {
			continue
		}
		if m[0] > at {
			pieces = append(pieces, SearchPiece{T: s[at:m[0]]})
		}
		pieces = append(pieces, SearchPiece{T: s[m[0]:m[1]], Hit: true})
		at = m[1]
		hits++
	}
	if hits == 0 {
		return nil
	}
	if at < len(text) {
		pieces = append(pieces, SearchPiece{T: text[at:]})
	}
	return pieces
}

// RunSearch finds the lines the pattern matches, with the line before and after each for context. maxLines <= 0 is SearchMaxMatchLines.
func RunSearch(index []SearchEntry, re *regexp.Regexp, maxLines int) SearchOutcome {
	if maxLines <= 0 {
		maxLines = SearchMaxMatchLines
	}
	var out SearchOutcome
	for _, entry := range index {
		if out.MatchedLines >= maxLines {
			out.Truncated = true
			break
		}
		// One list, the command's own text first (it can match, but is never offered as context: the heading is it).
		rows := make([]SearchLine, 0, 1+len(entry.Lines))
		rows = append(rows, entry.Input)
		rows = append(rows, entry.Lines...)
		hit := make([][]SearchPiece, len(rows))
		any := false
		for k := range rows {
			if out.MatchedLines >= maxLines {
				out.Truncated = true
				break
			}
			if rows[k].Text == "" {
				continue
			}
			if pieces := HighlightLine(rows[k].Text, re); pieces != nil {
				hit[k] = pieces
				out.MatchedLines++
				any = true
			}
		}
		if !any {
			continue
		}
		show := make([]bool, len(rows))
		for k, p := range hit {
			if p == nil {
				continue
			}
			show[k] = true
			if k-1 >= 1 { // [0] is the command's own text
				show[k-1] = true
			}
			if k+1 < len(rows) && k >= 1 {
				show[k+1] = true
			}
			if k == 0 && len(rows) > 1 {
				show[1] = true
			}
		}
		var blocks []SearchBlock
		var block *SearchBlock
		for k := range rows {
			if !show[k] {
				block = nil
				continue
			}
			if block == nil {
				blocks = append(blocks, SearchBlock{})
				block = &blocks[len(blocks)-1]
			}
			block.Rows = append(block.Rows, SearchRow{SearchLine: rows[k], Pieces: hit[k], Match: hit[k] != nil, Input: k == 0})
		}
		out.Results = append(out.Results, SearchResult{Idx: entry.Idx, Command: entry.Command, Time: entry.Time, Blocks: blocks})
	}
	return out
}
