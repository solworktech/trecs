package lib

import (
	"strconv"
	"strings"
)

// A recording is text that was written to one terminal and is about to be written to another, which does what it says. The
// players that draw on their own emulator (the editor, the web player) only read it; `trecs play` hands it to the terminal
// it is run in. What a terminal can be told by the text it prints includes things that have nothing to do with showing it: to put
// something in the clipboard (OSC 52), to answer a question - what are you, what is your title, what is at the cursor - by typing
// the answer into the program that is running, which is the shell the person is at (device attributes, status and window reports,
// XTVERSION, the queries of DCS), to change its font. A recording that was downloaded can ask for any of that. SafeForTerminal
// removes it, and leaves everything that is drawn: text, colours, cursor movement, titles, hyperlinks, sixel graphics and inline images.

// maxPendingEscape is the most a sequence cut by the end of a frame is held for: one that is longer than this has no end worth
// waiting for, and is dropped.
const maxPendingEscape = 16 << 20

// SafeForTerminal returns data without the sequences that would make the terminal do more than draw. A sequence that is cut by
// the end of data is held back, and given in newCarry to be passed in with the next data.
func SafeForTerminal(carry, data string) (out, newCarry string) {
	data = carry + data
	var b strings.Builder
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == 0xc2 && i+1 < len(data) && data[i+1] >= 0x80 && data[i+1] <= 0x9f:
			i += 2 // a C1 control, as UTF-8: a terminal in UTF-8 mode takes it for its 7-bit form (U+009B for ESC [)
			continue
		case c == 0xc2 && i+1 >= len(data):
			return b.String(), data[i:]
		case c != 0x1b:
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(data) {
			return b.String(), data[i:]
		}
		switch data[i+1] {
		case '[': // CSI
			j := i + 2
			for j < len(data) && data[j] >= 0x30 && data[j] <= 0x3f {
				j++
			}
			k := j
			for k < len(data) && data[k] >= 0x20 && data[k] <= 0x2f {
				k++
			}
			if k >= len(data) {
				if len(data)-i > maxPendingEscape {
					return b.String(), ""
				}
				return b.String(), data[i:]
			}
			end := k + 1
			if data[k] < 0x40 || data[k] > 0x7e { // not a sequence: whatever it is, it is passed on as it came
				b.WriteString(data[i:k])
				i = k
				continue
			}
			if !dangerousCSI(data[i+2:j], data[j:k], data[k]) {
				b.WriteString(data[i:end])
			}
			i = end
		case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: strings, up to BEL (OSC only) or ST
			osc := data[i+1] == ']'
			at, n := -1, 0
			for j := i + 2; j < len(data); j++ {
				if osc && data[j] == 0x07 {
					at, n = j, 1
					break
				}
				if data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\' {
					at, n = j, 2
					break
				}
			}
			if at < 0 {
				if len(data)-i > maxPendingEscape {
					return b.String(), ""
				}
				return b.String(), data[i:]
			}
			body := data[i+2 : at]
			if keepString(data[i+1], body) {
				b.WriteString(data[i : at+n])
			}
			i = at + n
		case 'Z': // DECID: asks what the terminal is
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), ""
}

// dangerousCSI says whether a control sequence asks the terminal something, or something of it, that is answered into the input.
func dangerousCSI(params, inter string, final byte) bool {
	switch final {
	case 'c': // device attributes, primary, secondary and tertiary
		return true
	case 'n': // device status reports: the cursor position, the printer, the keyboard...
		return true
	case 't': // window operations: reports of the size, the position, the title; and the changes of them
		return true
	case 'x': // DECREQTPARM
		return !strings.Contains(inter, " ")
	case 'q':
		return strings.HasPrefix(params, ">") // XTVERSION; "CSI Ps SP q" is the cursor's shape
	case 'u':
		return strings.HasPrefix(params, "?") // the kitty keyboard protocol's query
	case 'p':
		return strings.Contains(inter, "$") // DECRQM: is a mode set
	case 'y':
		return strings.Contains(inter, "*") // DECRQCRA: the checksum of an area
	}
	return false
}

// keepString says whether a string (OSC, DCS, APC, PM, SOS) is drawing and may go on: a title, a hyperlink, a prompt mark, an
// inline image, a sixel graphic. The rest is dropped.
func keepString(kind byte, body string) bool {
	switch kind {
	case 'P': // DCS: only sixel, "P1;P2;P3 q data"
		k := 0
		for k < len(body) && (body[k] == ';' || (body[k] >= '0' && body[k] <= '9')) {
			k++
		}
		return k < len(body) && body[k] == 'q'
	case ']':
		num, rest, _ := strings.Cut(body, ";")
		n, err := strconv.Atoi(num)
		if err != nil {
			return false
		}
		switch {
		case n == 0 || n == 1 || n == 2 || n == 7 || n == 8 || n == 9 || n == 133 || n == 777:
			return true // titles, the directory, hyperlinks, notifications, prompt marks
		case n == 1337:
			return strings.HasPrefix(rest, "File=") // the inline image; the rest of iTerm2's private commands report back
		case n == 4 || n == 5 || (n >= 10 && n <= 19) || (n >= 104 && n <= 119):
			return !strings.Contains(rest, "?") // setting a colour is drawing; asking for it is a question
		}
		return false // 52 (the clipboard), 50 (fonts), and what is not known
	}
	return false
}
