package lib

import "strings"

// sanitizeForVT removes the escape sequences that are addressed to a terminal's
// private features, which the screen emulator (vt10x) mistakes for ones it
// knows. The one that matters is the kitty keyboard protocol, which a modern
// shell (fish 4) turns on when its terminal answers its query:
//
//	ESC [ = 5 u     set keyboard flags        ESC [ > 1 u    push flags
//	ESC [ < 1 u     pop flags                 ESC [ ? u      ask for the flags
//
// vt10x reads any CSI that ends in "u" as "restore cursor" (a bare ESC[u) and
// ignores the prefix and parameters, so each of these sent the cursor to the
// saved position - the top left corner, if nothing was saved - and what was
// typed next was printed over whatever was there.
//
// Dropped: a CSI whose parameters start with <, = or > (private by definition:
// the keyboard protocol, the terminal's version query, modifyOtherKeys, secondary
// device attributes), a CSI ? ... u (the keyboard query) and a mode query
// (CSI ... $ p). Everything else, including a real ESC[u, is passed on.
//
// A sequence cut by the end of a frame is held back and joined to the next, so
// the whole of it can be judged; the emulator itself would cope with a cut
// sequence, but the decision here needs all of it.
func sanitizeForVT(carry, data string) (out, newCarry string) {
	data = carry + data
	var b strings.Builder
	for i := 0; i < len(data); {
		if data[i] != 0x1b {
			b.WriteByte(data[i])
			i++
			continue
		}
		if i+1 >= len(data) { // a lone ESC at the very end: what follows is in the next frame
			return b.String(), data[i:]
		}
		if data[i+1] != '[' {
			b.WriteByte(data[i])
			i++
			continue
		}
		j := i + 2
		for j < len(data) && data[j] >= 0x30 && data[j] <= 0x3f {
			j++
		}
		paramEnd := j
		for j < len(data) && data[j] >= 0x20 && data[j] <= 0x2f {
			j++
		}
		if j >= len(data) { // cut off before its final byte
			return b.String(), data[i:]
		}
		if data[j] < 0x40 || data[j] > 0x7e { // not a CSI after all: leave it to the emulator
			b.WriteString(data[i : j+1])
			i = j + 1
			continue
		}
		params, inter, final := data[i+2:paramEnd], data[paramEnd:j], data[j]
		private := params != "" && strings.ContainsRune("<=>", rune(params[0]))
		//nolint
		if !(private || (params != "" && params[0] == '?' && final == 'u') || (strings.Contains(inter, "$") && final == 'p')) {
			b.WriteString(data[i : j+1])
		}
		i = j + 1
	}
	return b.String(), ""
}
