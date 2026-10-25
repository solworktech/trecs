package main

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"trecs/secrets"

	libtrecs "trecs/lib"
)

// A live stream is seen while it is made, so what is on the screen is checked for secrets as it goes by, and what viewers get has
// them masked (the file on this machine keeps what was really there). It is a best effort and is said to be one: a secret is only
// found if the rules recognise it as it is printed, and a terminal's escape sequences, drawn between the characters of a word, can
// hide it from them.
//
// A secret is a word, or several lines of one (a private key). So the last word of what has been read is held back until the next
// word begins - or the output has been quiet for a moment, or the word is long enough that it is not one - so that a secret cut in two
// by the way the shell's output was read is not sent in two halves, each of which looks like nothing. A key is masked from its header to
// its footer, line after line, because a block cut up into frames is never in one piece to be recognised.

const (
	maskTailBytes = 1024  // how much of what was already sent is looked at again with what comes next
	maskHoldMax   = 512   // the longest word that is held back to be checked whole
	maskKeyMax    = 16384 // a key longer than this is not one; the masking of it stops
	maskMinSecret = 6     // a "secret" shorter than this is not masked: it would be taking letters out of words
)

var (
	keyBeginRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
	keyEndRe   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
)

// liveMasker masks the frames of a stream. It is not safe for concurrent use.
type liveMasker struct {
	scan *secrets.Scanner

	tail    string // the end of what was already sent, as it was (not masked): the start of a secret may be in it
	hold    string // the last word, not sent yet
	holdTS  int64  // when its first character was read
	inKey   bool   // in the middle of a private key
	keyLen  int
	keyTail string // the end of the key so far, to find a footer cut between two words

	masked int // how many secrets were masked (the stream is told of the first)
}

func newLiveMasker() *liveMasker { return &liveMasker{scan: secrets.Default()} }

// Frame takes one frame and gives back the frames to send for it: none (all of it is a word still being typed), or one.
func (m *liveMasker) Frame(f libtrecs.TerminalFrame) []libtrecs.TerminalFrame {
	text := m.hold + f.Data
	startTS := f.Timestamp
	if m.hold != "" {
		startTS = m.holdTS
	}
	cut := wordStart(text)
	if len(text)-cut > maskHoldMax { // not a word: nothing is held back for it
		cut = len(text)
	}
	if cut <= len(m.hold) {
		// nothing of the new data is complete, and the word being held is still the same one
		if m.hold == "" {
			m.holdTS = f.Timestamp
		}
		m.hold = text
		return nil
	}
	sendText := text[:cut]
	if cut < len(text) {
		m.hold, m.holdTS = text[cut:], f.Timestamp
	} else {
		m.hold = ""
	}
	return m.emit(f, startTS, sendText)
}

// Flush gives back the word that is held, masked (the output has been quiet for a moment, or the stream is ending).
func (m *liveMasker) Flush() []libtrecs.TerminalFrame {
	if m.hold == "" {
		return nil
	}
	text, ts := m.hold, m.holdTS
	m.hold = ""
	return m.emit(libtrecs.TerminalFrame{}, ts, text)
}

// Holding says whether a word is being held back.
func (m *liveMasker) Holding() bool { return m.hold != "" }

func (m *liveMasker) emit(like libtrecs.TerminalFrame, ts int64, text string) []libtrecs.TerminalFrame {
	out := m.mask(text)
	if out == "" {
		return nil
	}
	f := like
	f.Timestamp, f.Data = ts, out
	return []libtrecs.TerminalFrame{f}
}

// wordStart is where the last word of text begins: after the last white space. (An escape sequence is not white space, so a coloured word
// is held with its colour; one that is only a colour change at the end of a line is held for the moment it takes to be flushed.)
func wordStart(text string) int {
	for i := len(text) - 1; i >= 0; i-- {
		switch text[i] {
		case ' ', '\t', '\n', '\r':
			return i + 1
		}
	}
	return 0
}

// mask returns text with its secrets hidden. The letters of a secret become *, one for one, so that the screen is the same shape.
func (m *liveMasker) mask(text string) string {
	var b strings.Builder
	rest := text
	for rest != "" {
		if m.inKey {
			// the footer may be cut between two words, which are sent apart: it is looked for with the end of what came before
			window := m.keyTail + rest
			if loc := keyEndRe.FindStringIndex(window); loc != nil && loc[1] > len(m.keyTail) {
				n := loc[1] - len(m.keyTail)
				b.WriteString(stars(rest[:n]))
				m.inKey, m.keyLen, m.keyTail = false, 0, ""
				rest = rest[n:]
				continue
			}
			m.keyLen += len(rest)
			m.keyTail = lastBytes(window, 48)
			if m.keyLen > maskKeyMax { // never ended: not a key
				m.inKey, m.keyLen, m.keyTail = false, 0, ""
				b.WriteString(m.maskWords(rest))
			} else {
				b.WriteString(stars(rest))
			}
			break
		}
		// the header may be cut between words too: it is looked for with the end of what was sent before
		window := m.tail + rest
		loc := keyBeginRe.FindStringIndex(window)
		if loc == nil || loc[1] <= len(m.tail) {
			b.WriteString(m.maskWords(rest))
			break
		}
		start := max(loc[0]-len(m.tail), 0)
		b.WriteString(m.maskWords(rest[:start]))
		m.inKey, m.keyLen, m.keyTail, m.tail = true, 0, "", ""
		m.masked++
		rest = rest[start:] // the header's words that are in this text go with the key
	}
	return b.String()
}

// maskWords hides what the rules find in text, looking at the end of what was sent before it as well. That end is kept as it was
// (not masked), so that a secret cut between two frames is still found whole.
func (m *liveMasker) maskWords(text string) string {
	if text == "" {
		return ""
	}
	window := m.tail + text
	out := window
	for _, f := range m.scan.Scan(window) {
		if utf8.RuneCountInString(f.Secret) < maskMinSecret {
			continue
		}
		if strings.Contains(text, f.Secret) {
			m.masked++
		}
		out = strings.ReplaceAll(out, f.Secret, stars(f.Secret))
	}
	m.tail = lastBytes(window, maskTailBytes)
	// out has as many characters as window: its end, as many as text has, is what is sent
	n := utf8.RuneCountInString(text)
	r := []rune(out)
	if n > len(r) {
		n = len(r)
	}
	return string(r[len(r)-n:])
}

// stars is s with every character that is not a line break or a space turned into *.
func stars(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\n', '\r', ' ', '\t':
			b.WriteRune(r)
		default:
			b.WriteByte('*')
		}
	}
	return b.String()
}

// lastBytes is the last n bytes of s, starting at a whole character.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
