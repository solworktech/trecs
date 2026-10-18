package main

import (
	"strings"

	libtrecs "trecs/lib"
)

// dcsFilter decides which graphics and which Device Control Strings (ESC P ... ESC \) of the shell's output go into the
// recording.
//
// Most Device Control Strings are queries and answers between a program and the terminal (vim asking what the terminal can
// do, tmux's pass-through) which mean nothing on playback and used to disturb it: those are dropped, as they always have been.
// A sixel graphic - ESC P p1;p2;p3 q data ESC \ - is the output itself, a picture, and is kept, so that the players can
// draw it; so is an inline image, the iTerm2 protocol's ESC ] 1337 ; File=inline=1;... : data BEL, which is what `tdf`
// and `imgcat` send to terminals that speak it. One bigger than libtrecs.MaxSixelBytes is dropped, with a mark in its place
// (markSixelDropped), so a recording is not made enormous by one large image.
//
// The output reaches the recorder in chunks of whatever the read returned, and a graphic is often longer than one:
// the filter keeps what it has of a sequence that is not complete until the rest arrives, so a graphic is recorded
// whole or not at all, and one cut by a chunk is judged as it would have been uncut.
type dcsFilter struct {
	max         int
	pending     string // the start of a sequence whose end has not come
	dropping    bool   // inside a string being dropped: everything up to its terminator goes
	droppingOSC bool   // ... which is an OSC (ended by BEL too)
}

// markSixelDropped stands in the recording for a graphic that was too big to keep: an OSC the emulators ignore.
const markSixelDropped = "\x1b]7733;sixel-dropped\a"

// oscImage is how an inline image begins.
const oscImage = "\x1b]1337;File="

func newDCSFilter() *dcsFilter { return &dcsFilter{max: libtrecs.MaxSixelBytes} }

// stringEnd finds where a string ends: its ST (ESC \), or for an OSC its BEL too. The index of the terminator and its length.
func stringEnd(s string, osc bool) (at, n int) {
	for i := 0; i < len(s); i++ {
		if osc && s[i] == 0x07 {
			return i, 1
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
			return i, 2
		}
	}
	return -1, 0
}

// Filter returns what of the chunk goes into the recording.
func (f *dcsFilter) Filter(in string) string {
	data := f.pending + in
	f.pending = ""
	var out strings.Builder
	// the end of the chunk may be the start of a sequence (a lone ESC, "ESC ] 13"...) or of a terminator: wait for the next chunk
	hold := func(s string) string {
		for k := min(len(oscImage)-1, len(s)); k >= 1; k-- {
			if strings.HasSuffix(s, oscImage[:k]) {
				f.pending = oscImage[:k]
				return s[:len(s)-k]
			}
		}
		return s
	}
	for len(data) > 0 {
		if f.dropping {
			at, n := stringEnd(data, f.droppingOSC)
			if at < 0 {
				if strings.HasSuffix(data, "\x1b") {
					f.pending = "\x1b"
				}
				return out.String()
			}
			data = data[at+n:]
			f.dropping = false
			continue
		}
		i, j := strings.Index(data, "\x1bP"), strings.Index(data, oscImage)
		if j >= 0 && (i < 0 || j < i) { // an inline image
			out.WriteString(data[:j])
			rest := data[j+2:] // from "1337;File="
			at, n := stringEnd(rest, true)
			switch {
			case at < 0 && len(rest) > f.max:
				out.WriteString(markSixelDropped)
				f.dropping, f.droppingOSC = true, true
				if strings.HasSuffix(rest, "\x1b") {
					f.pending = "\x1b"
				}
				return out.String()
			case at < 0:
				f.pending = data[j:]
				return out.String()
			case at > f.max:
				out.WriteString(markSixelDropped)
			case libtrecs.ITermShown(rest[:at]):
				out.WriteString(data[j : j+2+at+n])
			}
			data = rest[at+n:]
			continue
		}
		if i < 0 {
			out.WriteString(hold(data))
			return out.String()
		}
		out.WriteString(data[:i])
		rest := data[i+2:]

		// what kind of string it is shows after its parameters
		k := 0
		for k < len(rest) && (rest[k] == ';' || (rest[k] >= '0' && rest[k] <= '9')) {
			k++
		}
		if k == len(rest) {
			f.pending = data[i:]
			return out.String()
		}
		sixel := rest[k] == 'q'
		end := strings.Index(rest, "\x1b\\")
		switch {
		case !sixel && end < 0:
			f.dropping, f.droppingOSC = true, false
			if strings.HasSuffix(rest, "\x1b") {
				f.pending = "\x1b"
			}
			return out.String()
		case !sixel:
			// not recorded
		case end < 0:
			if len(rest) > f.max {
				out.WriteString(markSixelDropped)
				f.dropping, f.droppingOSC = true, false
				if strings.HasSuffix(rest, "\x1b") {
					f.pending = "\x1b"
				}
				return out.String()
			}
			f.pending = data[i:]
			return out.String()
		case end > f.max:
			out.WriteString(markSixelDropped)
		default:
			out.WriteString(data[i : i+2+end+2])
		}
		data = rest[end+2:]
	}
	return out.String()
}

// holding reports whether the filter has kept part of a sequence for the next chunk.
func (f *dcsFilter) holding() bool { return f.pending != "" || f.dropping }

// Flush is what the filter still holds when the output ends: a lone ESC is output like any other; the start of a
// string that never ended is not.
func (f *dcsFilter) Flush() string {
	p := f.pending
	f.pending = ""
	if p != "" && len(p) < len(oscImage) && strings.HasPrefix(oscImage, p) { // a start of something that never came: output like any other
		return p
	}
	return ""
}
