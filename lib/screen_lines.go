package lib

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The lines of what a terminal printed, as a person reads them.
//
// A stream of output is not text: a program (the shell's line editor, most of all) draws a line, moves the cursor back and draws part
// of it again. Bash does this at every paste, history recall and tab completion, and writes only what changed: "fetch " is on the screen,
// the pasted address is written after a run of ESC [ C that skips over it, and what is in the stream is the address alone. Read with
// the escapes dropped, the line is the address: the command the person typed is gone from the text, and so from a search.
//
// So the stream is run on a small model of a screen: a grid of rows, a cursor, and the movements and erasures a line editor uses
// (and those a program that draws on the screen uses, to leave out what they draw). It keeps every row that was on it, in the order
// they were: what scrolled off is the log of the session, and is what a search is made over.
//
// What it is not: a terminal. Lines are not wrapped (a long line is one row); colours and widths of wide characters are not followed.
// The scroll region is: a program that keeps the top of the screen (a title, a picture) and scrolls only the bottom, as a film made of
// text does, leaves what scrolls out of the region in the log, and the top where it is. Lines are only scrolled out of the region
// by a line feed on its last row. A program that paints the screen by addressing its cells (htop, top, a full-screen menu that does not switch to
// the alternate screen) is recognised by it: a row that was written to after the cursor was placed by address, and that the cursor never left by a line feed, is a painting
// and not a line of output, and is left out. (A prompt drawn at the top of a cleared screen is placed by address too, but it is
// ended by a line feed when the command is run.)

const (
	defaultScreenRows = 24
	maxScreenRows     = 500 // the most rows a screen is taken to have: a size that is not one is read as the default
)

// screenRow is a row of the log.
type screenRow struct {
	text    []rune
	painted bool // written to after the cursor was placed by address
	fed     bool // the cursor left the row by a line feed: it is a line of output
	frame   int  // the frame that last wrote to the row, or ended it
}

type screenLog struct {
	rows       []screenRow
	top        int // the first row of the screen: the rows above it have scrolled off
	h          int // the height of the screen
	r, col     int
	positioned bool // the cursor was placed by address, and has not been moved by a carriage return or a line feed since
	insert     bool // insert mode (ESC [ 4 h): a character typed pushes the rest of the row to the right
	alt        bool // the alternate screen is on: what is drawn there is not part of the log
	savedR     int
	savedCol   int
	savedPos   bool // what ESC 7 and ESC 8 keep with the cursor: whether it was placed by address
	frame      int  // the frame that is being read
	// the scroll region (ESC [ top ; bottom r), as rows of the screen from 0; the whole screen when regTop == 0 and regBot == h-1
	regTop, regBot int
}

func newScreenLog(rows int) *screenLog {
	if rows <= 0 || rows > maxScreenRows {
		rows = defaultScreenRows
	}
	return &screenLog{rows: make([]screenRow, 1), h: rows, regBot: rows - 1}
}

// region is true when only part of the screen scrolls.
func (s *screenLog) region() bool { return s.regTop > 0 || s.regBot < s.h-1 }

// scrollRegion moves the rows of the scroll region up by one: the first leaves the region, and goes to the log, above the screen; a
// blank row comes in at the bottom. The cursor stays on the row it is on, which now holds what was under it.
func (s *screenLog) scrollRegion() {
	first, last := s.top+s.regTop, s.top+s.regBot
	s.ensure(max(last, s.r))
	gone := s.rows[first]
	gone.fed = true
	s.rows = append(s.rows, screenRow{})
	copy(s.rows[s.top+1:], s.rows[s.top:]) // room for it in the log, in front of the screen
	s.rows[s.top] = gone
	s.top++
	s.r++
	if s.savedR >= s.top-1 {
		s.savedR++
	}
	for r := first + 1; r < last+1; r++ {
		s.rows[r] = s.rows[r+1]
	}
	s.rows[last+1] = screenRow{frame: s.frame}
}

func (s *screenLog) ensure(r int) {
	for len(s.rows) <= r {
		s.rows = append(s.rows, screenRow{})
	}
}

func (s *screenLog) put(ch rune) {
	s.ensure(s.r)
	row := &s.rows[s.r]
	for len(row.text) < s.col {
		row.text = append(row.text, ' ')
	}
	if s.col < len(row.text) && s.insert {
		row.text = append(row.text[:s.col], append([]rune{ch}, row.text[s.col:]...)...)
	} else if s.col < len(row.text) {
		row.text[s.col] = ch
	} else {
		row.text = append(row.text, ch)
	}
	row.frame = s.frame
	row.painted = row.painted || s.positioned
	s.col++
}

func (s *screenLog) lineFeed() {
	s.ensure(s.r)
	s.rows[s.r].frame = s.frame // the line is complete in this frame
	s.rows[s.r].fed = true
	if s.region() && s.r == s.top+s.regBot {
		s.scrollRegion()
	} else {
		s.r++
		s.ensure(s.r)
		if s.r > s.top+s.h-1 {
			s.top = s.r - s.h + 1
		}
	}
	s.col = 0
	s.positioned = false
}

// blank empties a row; from is the first column to go.
func (s *screenLog) blank(r, from int) {
	if r < 0 || r >= len(s.rows) {
		return
	}
	row := &s.rows[r]
	if from < len(row.text) {
		row.text = row.text[:from]
	}
	row.frame = s.frame
}

func (s *screenLog) csi(params string, final byte) {
	private := strings.HasPrefix(params, "?")
	if private {
		params = params[1:]
		if final == 'h' || final == 'l' {
			for _, n := range parseParams(params) {
				if n == 1049 || n == 47 || n == 1047 {
					s.alt = final == 'h'
				}
			}
		}
		return
	}
	if final == 'h' || final == 'l' {
		for _, n := range parseParams(params) {
			if n == 4 {
				s.insert = final == 'h' // a line editor puts the terminal in insert mode for the one character it types in the middle of a line
			}
		}
		return
	}
	if s.alt {
		return
	}
	nums := parseParams(params)
	k := paramOr(nums, 0, 1)
	switch final {
	case 'A':
		s.r = max(0, s.r-k)
	case 'B':
		s.r = min(s.r+k, max(s.r, s.top+s.h-1))
		s.ensure(s.r)
	case 'C':
		s.col += k
	case 'D':
		s.col = max(0, s.col-k)
	case 'E':
		s.r = min(s.r+k, max(s.r, s.top+s.h-1))
		s.ensure(s.r)
		s.col = 0
	case 'F':
		s.r, s.col = max(0, s.r-k), 0
	case 'G':
		s.col = k - 1
	case 'H', 'f':
		s.r = s.top + min(paramOr(nums, 0, 1)-1, s.h-1)
		s.col = max(0, paramOr(nums, 1, 1)-1)
		s.ensure(s.r)
		s.positioned = true
	case 'd':
		s.r = s.top + min(k-1, s.h-1)
		s.ensure(s.r)
		s.positioned = true
	case 'r': // setting the scroll region puts the cursor at the home position
		top, bot := paramOr(nums, 0, 1)-1, paramOr(nums, 1, s.h)-1
		if top < 0 || bot > s.h-1 || top >= bot { // not a region: the whole screen scrolls
			top, bot = 0, s.h-1
		}
		s.regTop, s.regBot = top, bot
		s.r, s.col = s.top, 0
		s.ensure(s.r)
	case 's':
		s.savedR, s.savedCol, s.savedPos = s.r, s.col, s.positioned
	case 'u':
		s.r, s.col, s.positioned = s.savedR, s.savedCol, s.savedPos
		s.ensure(s.r)
	case 'J':
		s.ensure(s.r)
		switch paramOr(nums, 0, 0) {
		case 0:
			s.blank(s.r, s.col)
			for r := s.r + 1; r < len(s.rows); r++ {
				s.blank(r, 0)
			}
		case 1:
			for r := s.top; r < s.r; r++ {
				s.blank(r, 0)
			}
			for c := 0; c <= s.col && c < len(s.rows[s.r].text); c++ {
				s.rows[s.r].text[c] = ' '
			}
		default: // the screen is cleared: what was on it is the log, and what comes is after it
			s.top = len(s.rows)
			s.r = s.top
			s.rows = append(s.rows, screenRow{frame: s.frame})
		}
	case 'K':
		s.ensure(s.r)
		row := &s.rows[s.r]
		switch paramOr(nums, 0, 0) {
		case 0:
			s.blank(s.r, s.col)
		case 1:
			for c := 0; c <= s.col && c < len(row.text); c++ {
				row.text[c] = ' '
			}
		default:
			s.blank(s.r, 0)
		}
	case 'P':
		s.ensure(s.r)
		row := &s.rows[s.r]
		if s.col < len(row.text) {
			end := min(s.col+k, len(row.text))
			row.text = append(row.text[:s.col], row.text[end:]...)
		}
	case '@':
		s.ensure(s.r)
		row := &s.rows[s.r]
		at := min(s.col, len(row.text))
		out := make([]rune, 0, len(row.text)+k)
		out = append(out, row.text[:at]...)
		for range k {
			out = append(out, ' ')
		}
		row.text = append(out, row.text[at:]...)
	case 'X':
		s.ensure(s.r)
		row := &s.rows[s.r]
		for c := s.col; c < s.col+k && c < len(row.text); c++ {
			row.text[c] = ' '
		}
	}
}

// run reads the output. frameAt gives the frame that a position of it is in.
func (s *screenLog) run(out string, frameAt func(pos int) int) {
	for i := 0; i < len(out); {
		s.frame = frameAt(i)
		ch := out[i]
		switch {
		case ch == 0x1b && i+1 < len(out):
			switch out[i+1] {
			case '[':
				end := skipEscape(out, i)
				if end-1 > i+1 && end <= len(out) && out[end-1] >= 0x40 && out[end-1] <= 0x7e {
					s.csi(out[i+2:end-1], out[end-1])
				}
				i = end
				continue
			case '7':
				s.savedR, s.savedCol, s.savedPos = s.r, s.col, s.positioned
			case '8':
				s.r, s.col, s.positioned = s.savedR, s.savedCol, s.savedPos
				s.ensure(s.r)
			case 'M':
				if !s.alt {
					s.r = max(0, s.r-1)
				}
			}
			i = skipEscape(out, i)
			continue
		case s.alt:
		case ch == '\n':
			s.lineFeed()
		case ch == '\r':
			s.col = 0 // a program that paints goes down a row with CR and cursor-down, not with a line feed: this is not the end of a painting
		case ch == '\b':
			s.col = max(0, s.col-1)
		case ch == '\t':
			s.col += 8 - s.col%8
		case ch >= ' ' && ch != 0x7f:
			r, size := utf8.DecodeRuneInString(out[i:])
			s.put(r)
			i += size
			continue
		}
		i++
	}
}

// lines are the rows of the log as text, with the frame each was last written in, less the paintings. Rows with nothing in them at the
// start and the end are not lines.
func (s *screenLog) lines() (text []string, frame []int) {
	for i, row := range s.rows {
		// a row the cursor never left by a line feed is not a line of output, if it was painted or has been erased; the last row is the
		// line that is being written
		if !row.fed && i < len(s.rows)-1 && (row.painted || len(row.text) == 0) {
			continue
		}
		text = append(text, strings.TrimRightFunc(string(row.text), unicode.IsSpace))
		frame = append(frame, row.frame)
	}
	for len(text) > 0 && text[0] == "" {
		text, frame = text[1:], frame[1:]
	}
	for len(text) > 0 && text[len(text)-1] == "" {
		text, frame = text[:len(text)-1], frame[:len(frame)-1]
	}
	return text, frame
}
