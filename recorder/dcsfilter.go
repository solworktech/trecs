package main

import (
	"strings"

	libtrecs "trecs/lib"
)

// dcsFilter decides which Device Control Strings (ESC P ... ESC \) of the shell's output go into the recording.
//
// Most are queries and answers between a program and the terminal (vim asking what the terminal can do, tmux's
// pass-through) which mean nothing on playback and used to disturb it: those are dropped, as they always have been.
// A sixel graphic - ESC P p1;p2;p3 q data ESC \ - is the output itself, a picture, and is kept, so that the players
// can draw it. One bigger than libtrecs.MaxSixelBytes is dropped, with a mark in its place (markSixelDropped), so a
// recording is not made enormous by one large image.
//
// The output reaches the recorder in chunks of whatever the read returned, and a graphic is often longer than one:
// the filter keeps what it has of a sequence that is not complete until the rest arrives, so a graphic is recorded
// whole or not at all, and one cut by a chunk is judged as it would have been uncut.
type dcsFilter struct {
	max      int
	pending  string // the start of a sequence whose end has not come
	dropping bool   // inside a string being dropped: everything up to its terminator goes
}

// markSixelDropped stands in the recording for a graphic that was too big to keep: an OSC the emulators ignore.
const markSixelDropped = "\x1b]7733;sixel-dropped\a"

func newDCSFilter() *dcsFilter { return &dcsFilter{max: libtrecs.MaxSixelBytes} }

// Filter returns what of the chunk goes into the recording.
func (f *dcsFilter) Filter(in string) string {
	data := f.pending + in
	f.pending = ""
	var out strings.Builder
	// a lone ESC at the end may be the start of the terminator or of a new string: wait for the next chunk
	hold := func(s string) string {
		if strings.HasSuffix(s, "\x1b") {
			f.pending = "\x1b"
			return s[:len(s)-1]
		}
		return s
	}
	for len(data) > 0 {
		if f.dropping {
			i := strings.Index(data, "\x1b\\")
			if i < 0 {
				hold(data)
				return out.String()
			}
			data = data[i+2:]
			f.dropping = false
			continue
		}
		i := strings.Index(data, "\x1bP")
		if i < 0 {
			out.WriteString(hold(data))
			return out.String()
		}
		out.WriteString(data[:i])
		rest := data[i+2:]

		// what kind of string it is shows after its parameters
		j := 0
		for j < len(rest) && (rest[j] == ';' || (rest[j] >= '0' && rest[j] <= '9')) {
			j++
		}
		if j == len(rest) {
			f.pending = data[i:]
			return out.String()
		}
		sixel := rest[j] == 'q'
		end := strings.Index(rest, "\x1b\\")
		switch {
		case !sixel && end < 0:
			f.dropping = true
			hold(rest)
			return out.String()
		case !sixel:
			// not recorded
		case end < 0:
			if len(rest) > f.max {
				out.WriteString(markSixelDropped)
				f.dropping = true
				hold(rest)
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
	if p == "\x1b" {
		return p
	}
	return ""
}
