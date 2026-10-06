package lib

import (
	"net/url"
	"regexp"
	"strings"
)

// Prompt marks (OSC 133), the shell-integration convention used by kitty, iTerm2,
// WezTerm, VS Code and others: the shell itself says where each prompt, each
// command's input and each command's output begin and end:
//
//	ESC ] 133 ; A        a prompt starts (A;k=s: a continuation prompt - PS2)
//	ESC ] 133 ; B        the prompt ends; what follows is what the user types
//	ESC ] 133 ; C        Enter was pressed and the command starts executing
//	ESC ] 133 ; D ; N    the command finished with status N
//
// With marks nothing has to be guessed from what the prompt looks like: not its
// characters, not whether it has a window title, not what PS2 is - which is what
// the heuristics in groupIntoCommands do for recordings made without them.

var (
	markRe = regexp.MustCompile("\x1b\\]133;([A-D])([^\x07\x1b]*)(?:\x07|\x1b\\\\)")
	// the start of a mark cut off at the end of a frame
	partialMarkRe = regexp.MustCompile("\x1b(?:\\](?:1(?:3(?:3(?:;[^\x07\x1b]*)?)?)?)?)?$")
)

// hasPromptMarks reports whether the recording carries prompt marks that say
// enough: the start of a prompt (A) and either its end (B) - which separates the
// prompt from what was typed after it - or the command line itself, which the
// shell reports with the start of execution (C;cmdline_url=, what fish 4 does:
// it marks A, C and D but never B). Marks that say less than that leave a
// recording to be read the way an unmarked one is.
func hasPromptMarks(frames []TerminalFrame) bool {
	a, b, cmdline := false, false, false
	for _, f := range frames {
		a = a || strings.Contains(f.Data, "\x1b]133;A")
		b = b || strings.Contains(f.Data, "\x1b]133;B")
		cmdline = cmdline || strings.Contains(f.Data, "\x1b]133;C;cmdline_url=")
	}
	return a && (b || cmdline)
}

var cmdlineMarkRe = regexp.MustCompile("\x1b\\]133;C;cmdline_url=([^\x07\x1b]*)(?:\x07|\x1b\\\\)")

// markedCommandLine is the command line the shell itself reported when it
// started the command (fish 4: "C;cmdline_url=<percent-encoded>") - exact, with
// no guessing from what was echoed, whatever the line editor did to the screen.
func markedCommandLine(outputFrames []TerminalFrame) (string, bool) {
	for _, f := range outputFrames {
		if mm := cmdlineMarkRe.FindStringSubmatch(f.Data); mm != nil {
			if text, err := url.PathUnescape(mm[1]); err == nil {
				return strings.TrimSpace(text), true
			}
		}
	}
	return "", false
}

// setMarkedCommandLine updates the command line in that mark, for a command whose
// text was edited: a recording read back would otherwise take the old one.
func setMarkedCommandLine(outputFrames []TerminalFrame, text string) {
	for i, f := range outputFrames {
		if cmdlineMarkRe.MatchString(f.Data) {
			enc := url.PathEscape(text)
			outputFrames[i].Data = cmdlineMarkRe.ReplaceAllStringFunc(f.Data, func(string) string {
				return "\x1b]133;C;cmdline_url=" + enc + "\x07"
			})
			return
		}
	}
}

// commandInputText is the command's typed text: the shell's own report of it
// when there is one, else read off the line.
func commandInputText(cmd *Command) string {
	if text, ok := markedCommandLine(cmd.OutputFrames); ok {
		return text
	}
	return reconstructInput(cmd.InputFrames, cmd.PromptFrame, cmd.ContinuationPrompt)
}

// markPiece is a stretch of a frame: plain text (kind 0) or one mark.
type markPiece struct {
	kind     byte   // 0 for text, else 'A'..'D'
	param    string // what follows the letter, e.g. ";k=s" or ";0"
	data     string // the bytes, mark included
	frameIdx int
	frame    TerminalFrame // timestamp, width and height of the frame it came from
}

// splitAtMarks cuts frames into text and marks. The pieces' data concatenate to
// exactly the bytes of the frames (a mark cut by the end of a frame is carried to
// the front of the next one), so nothing is lost or repeated when they are
// written back out.
func splitAtMarks(frames []TerminalFrame) []markPiece {
	var out []markPiece
	carry := ""
	for i, f := range frames {
		d := carry + f.Data
		carry = ""
		if loc := partialMarkRe.FindStringIndex(d); loc != nil {
			carry, d = d[loc[0]:], d[:loc[0]]
		}
		last := 0
		for _, m := range markRe.FindAllStringSubmatchIndex(d, -1) {
			if m[0] > last {
				out = append(out, markPiece{data: d[last:m[0]], frameIdx: i, frame: f})
			}
			out = append(out, markPiece{kind: d[m[2]], param: d[m[4]:m[5]], data: d[m[0]:m[1]], frameIdx: i, frame: f})
			last = m[1]
		}
		if last < len(d) {
			out = append(out, markPiece{data: d[last:], frameIdx: i, frame: f})
		}
	}
	if carry != "" && len(frames) > 0 {
		out = append(out, markPiece{data: carry, frameIdx: len(frames) - 1, frame: frames[len(frames)-1]})
	}
	return out
}

func (p markPiece) asFrame() TerminalFrame {
	f := p.frame
	f.Data = p.data
	return f
}

// groupByMarks groups a recording that carries prompt marks into commands.
func groupByMarks(frames []TerminalFrame) []Command {
	const (
		stIdle = iota
		stPrompt
		stRedraw // the same prompt drawn again before the command ran (resize, Ctrl-L...)
		stAfterD // the command is over: what the shell writes now belongs to its next prompt
		stPS2
		stInput
		stOutput
	)
	var (
		commands      []Command
		cur           *Command
		st            = stIdle
		sawC          bool
		sawD          bool
		lead          strings.Builder // what the shell wrote before a prompt: before the first one, or after the last command ended
		leadFrames    []TerminalFrame
		promptB       strings.Builder
		promptTS      TerminalFrame
		promptHasText bool
		ps2B          strings.Builder
	)

	finish := func(nextFirst int) {
		if cur == nil {
			return
		}
		if cur.PromptFrame.Data == "" { // no B: the prompt is everything up to here
			cur.PromptFrame = promptTS
			cur.PromptFrame.Data = promptB.String()
		}
		cur.LastRawFrameIndex = max(cur.FirstRawFrameIndex, nextFirst-1)
		cur.EndTime = frames[cur.LastRawFrameIndex].Timestamp
		cur.InputText = commandInputText(cur)
		// Kept if it ran, or if something was typed (a Ctrl-D at an empty prompt
		// leaves just "exit"); an empty Enter or Ctrl-C leaves nothing.
		if sawC || cur.InputText != "" {
			commands = append(commands, *cur)
		}
		cur = nil
	}

	for _, p := range splitAtMarks(frames) {
		fr := p.asFrame()
		switch {
		case p.kind == 'A' && !strings.HasPrefix(p.param, ";k=s"):
			if cur != nil && st == stPrompt && !promptHasText { // marked twice (fish 4 marks it itself, so does our integration)
				promptB.WriteString(p.data)
				continue
			}
			if cur != nil && (st == stPrompt || st == stInput || st == stRedraw) && !sawC && !sawD {
				cur.InputFrames = append(cur.InputFrames, fr)
				st = stRedraw
				continue
			}
			finish(p.frameIdx)
			cur = &Command{HasPrompt: true, StartTime: p.frame.Timestamp, FirstRawFrameIndex: p.frameIdx}
			sawC, sawD, st = false, false, stPrompt
			promptB.Reset()
			promptB.WriteString(lead.String())
			lead.Reset()
			leadFrames = nil
			promptB.WriteString(p.data)
			promptTS = p.frame
			promptHasText = false
			ps2B.Reset()
		case p.kind == 'A': // a continuation prompt
			if cur == nil {
				continue
			}
			cur.InputFrames = append(cur.InputFrames, fr)
			ps2B.Reset() // each continuation line draws its own
			st = stPS2
		case p.kind == 'B':
			switch {
			case cur != nil && st == stPrompt:
				promptB.WriteString(p.data)
				cur.PromptFrame = promptTS
				cur.PromptFrame.Data = promptB.String()
				st = stInput
			case cur != nil && (st == stPS2 || st == stRedraw):
				cur.InputFrames = append(cur.InputFrames, fr)
				st = stInput
			case cur != nil && st == stInput:
				cur.InputFrames = append(cur.InputFrames, fr) // the prompt repainted: fish does it on every keystroke
			case cur != nil && st == stOutput:
				cur.OutputFrames = append(cur.OutputFrames, fr)
			case st == stAfterD:
				lead.WriteString(p.data)
				leadFrames = append(leadFrames, fr)
			}
		case p.kind == 'C' && st == stAfterD: // a second report of the same thing (a doubled mark)
			lead.WriteString(p.data)
			leadFrames = append(leadFrames, fr)
		case p.kind == 'C':
			if cur != nil {
				cur.OutputFrames = append(cur.OutputFrames, fr)
				sawC, st = true, stOutput
			}
		case p.kind == 'D':
			sawD = true
			switch {
			case cur != nil && st != stAfterD:
				cur.OutputFrames = append(cur.OutputFrames, fr)
				st = stAfterD
			default:
				lead.WriteString(p.data)
				leadFrames = append(leadFrames, fr)
			}
		default: // text
			switch st {
			case stIdle:
				lead.WriteString(p.data)
			case stPrompt:
				promptB.WriteString(p.data)
				promptHasText = promptHasText || visibleText(p.data) != ""
			case stPS2:
				ps2B.WriteString(p.data)
				cur.InputFrames = append(cur.InputFrames, fr)
				cur.ContinuationPrompt = visibleText(ps2B.String())
			case stInput, stRedraw:
				cur.InputFrames = append(cur.InputFrames, fr)
			case stOutput:
				cur.OutputFrames = append(cur.OutputFrames, fr)
			case stAfterD:
				// fish, after a command, prints a marker where the output did not end in a
				// newline, pads the line with spaces to the terminal's width and moves on:
				// the shell's doing, for the prompt that follows - not the command's output.
				lead.WriteString(p.data)
				leadFrames = append(leadFrames, fr)
			}
		}
	}
	if cur != nil && st == stAfterD { // the recording ends here: the shell's last words stay with the last command
		cur.OutputFrames = append(cur.OutputFrames, leadFrames...)
	}
	finish(len(frames))
	markTypedAhead(commands)
	for i := range commands {
		commands[i].OutputTextRaw = commandOutputRaw(&commands[i])
		commands[i].OutputText = deriveOutputText(&commands[i])
	}
	return commands
}

// markTypedAhead finds what was typed while a command was still running. The
// terminal echoes those keystrokes as they come, so they land in the middle of the
// running command's output (one character per frame, at the pace of typing) - and
// the shell reads them when the command is over and shows them again at its prompt.
// They are the start of the NEXT command, not output of this one. A run of
// single-character frames that spells the beginning of the next command's text is
// such an echo; those frames stay in the recording but are left out of the text
// taken from the output.
func markTypedAhead(commands []Command) {
	for i := 0; i+1 < len(commands); i++ {
		next := commands[i+1].InputText
		frames := commands[i].OutputFrames
		for j := 0; j < len(frames); {
			k, run := j, ""
			for k < len(frames) && len(frames[k].Data) == 1 && frames[k].Data[0] >= 0x20 && frames[k].Data[0] < 0x7f {
				run += frames[k].Data
				k++
			}
			if len(run) >= 2 && strings.HasPrefix(next, run) {
				for x := j; x < k; x++ {
					commands[i].TypedAhead = append(commands[i].TypedAhead, x)
				}
			}
			j = max(k, j+1)
		}
	}
}

// commandOutputRaw is what the command wrote, without the keystrokes typed while it ran.
func commandOutputRaw(cmd *Command) string {
	if len(cmd.TypedAhead) == 0 {
		return reconstructOutput(cmd.OutputFrames)
	}
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
	return reconstructOutput(kept)
}
