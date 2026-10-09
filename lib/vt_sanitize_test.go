package lib

import (
	"regexp"
	"strings"
	"testing"
)

// the colour/style tags VTScreen.Render draws with, which a plain-text assertion does not want
var tviewTagRe = regexp.MustCompile(`\[[^\[\]]*\]`)

func TestSanitizeForVT(t *testing.T) {
	dropped := []string{
		"\x1b[=5u", "\x1b[>1u", "\x1b[<1u", "\x1b[?u", // the kitty keyboard protocol
		"\x1b[>4;1m", "\x1b[>4;0m", "\x1b[>0q", "\x1b[>c", "\x1b[=c", // modifyOtherKeys, terminal version, device attributes
		"\x1b[?2026$p", // a mode query
	}
	for _, seq := range dropped {
		if out, carry := sanitizeForVT("", "a"+seq+"b"); out != "ab" || carry != "" {
			t.Errorf("%q must be dropped: got %q, carry %q", seq, out, carry)
		}
	}
	kept := []string{
		"\x1b[u", "\x1b[s", // a real restore/save cursor
		"\x1b[31m", "\x1b[m", "\x1b[1;32m", "\x1b[?25h", "\x1b[?2004h", "\x1b[?1049h", "\x1b[2;3H", "\x1b[18C", "\x1b[K", "\x1b[3A",
		"\x1b]0;title\x07", "\x1b=", "\x1b>", "\x1b(B", "plain \r\n text",
	}
	for _, seq := range kept {
		if out, _ := sanitizeForVT("", "a"+seq+"b"); out != "a"+seq+"b" {
			t.Errorf("%q must be passed on untouched, got %q", seq, out)
		}
	}
	// a sequence cut by the end of a frame is judged whole
	out, carry := sanitizeForVT("", "x\x1b[=")
	if out != "x" || carry != "\x1b[=" {
		t.Errorf("cut sequence: out %q carry %q", out, carry)
	}
	if out, carry := sanitizeForVT(carry, "5uy"); out != "y" || carry != "" {
		t.Errorf("its second half: out %q carry %q (the whole ESC[=5u is dropped)", out, carry)
	}
	if out, carry := sanitizeForVT("", "x\x1b"); out != "x" || carry != "\x1b" {
		t.Errorf("a lone ESC at the end is held: out %q carry %q", out, carry)
	}
	if out, _ := sanitizeForVT("\x1b", "[31mred"); out != "\x1b[31mred" {
		t.Errorf("held ESC then [31m: %q", out)
	}
}

// fish 4 turns on the kitty keyboard protocol (ESC[=5u) when its terminal answers
// its query. vt10x took that for "restore cursor", sent the cursor to the top
// left, and everything typed afterwards was printed over the screen's first row:
// the "Welcome to fish" line came out as "lelcome to fish", the typed command
// landed on top of the output... This is the user's own recording.
func TestVTScreenIgnoresTheKittyKeyboardProtocol(t *testing.T) {
	frames, _, meta, err := LoadFrames("testdata/fish-kitty-keyboard.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	screen := NewVTScreenSized(meta.Width, meta.Height)
	for _, f := range frames[:6] { // greeting, the prompt, and the first keystroke
		screen.Feed(f.Data)
	}
	rows := strings.Split(tviewTagRe.ReplaceAllString(screen.Render(), ""), "\n")
	if got := strings.TrimRight(rows[0], " "); got != "Welcome to fish, the friendly interactive shell" {
		t.Errorf("the first row was overwritten: %q", got)
	}
	var prompt string
	for _, r := range rows {
		if strings.HasPrefix(r, "jesse@port") {
			prompt = strings.TrimRight(r, " ")
		}
	}
	if !strings.HasSuffix(prompt, "> l") {
		t.Errorf("the typed character must follow the prompt, got %q", prompt)
	}
}
