package lib

import (
	"strings"
	"testing"
)

func TestCleanOutputLeavesNoCarriageReturn(t *testing.T) {
	cases := map[string]string{
		"a\r\nb\r\n":                          "a\nb",
		"\x1b[01;32mgreen\x1b[0m text\r\n":    "green text",
		"50%\r100%\r\nDone\r\n":               "100%\nDone", // a progress bar shows its final state
		"Distributor ID:\tDebian\r\n":         "Distributor ID: Debian",
		"x\x1b(B\x1b[m y\r\n":                 "x y",
		"\r\n\r\ntrailing   \r\n\r\n":         "trailing",
		"-rw-r--r-- 1 root\r\n\x1b]0;t\x07ok": "-rw-r--r-- 1 root\nok",
	}
	for in, want := range cases {
		got := CleanOutput(in)
		if got != want {
			t.Errorf("CleanOutput(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "\r") {
			t.Errorf("CleanOutput(%q) still has a carriage return", in)
		}
	}
}

func TestMarkdownFromACommandsSession(t *testing.T) {
	cmds, err := ParseRecording("testdata/marks-script.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	cmds = append(cmds, cmds[0]) // the same command twice: the second anchor must differ
	cmds[1].Annotation.Text = "  Uses a continuation.\n\n- not a list\n<b>"
	md := MarkdownFromCommands("My session", cmds)

	if strings.Contains(md, "\r") {
		t.Error("the Markdown must not contain a carriage return (^M)")
	}
	for _, want := range []string{
		"# My session\n",
		"1. [`ls /etc/hostname`](#ls-etchostname)\n",
		"7. [`ls /etc/hostname`](#ls-etchostname-1)\n", // a repeated command gets -1
		"### `ls /etc/hostname`\n\n```console\n/etc/hostname\n```",
		// one line in the heading, the command as typed in a bash block
		"### `printf '%s\\n' -author \"Jesse\" -i https://example.com/README.md -o out.pdf -theme dark -with-footer`",
		"```bash\nprintf '%s\\n' -author \"Jesse\" -i \\\n    https://example.com/README.md -o out.pdf \\\n    -theme dark -with-footer\n```",
		"Uses a continuation.\n\n\\- not a list  \n&lt;b>",
		"```console\n> not a continuation prompt\n```",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %q\n---\n%s", want, md)
		}
	}
	if strings.Contains(md, "### `true`\n\n```") {
		t.Error("a command with no output has no output block")
	}
}

func TestMarkdownNotesFullScreenSessions(t *testing.T) {
	cmd := Command{InputText: "vi notes.txt", OutputTextRaw: "\x1b[?1049h\x1b[H screen \x1b[?1049l"}
	if md := MarkdownFromCommands("", []Command{cmd}); !strings.Contains(md, "_(interactive full-screen session") || strings.Contains(md, "```console") {
		t.Errorf("got:\n%s", md)
	}
}

func TestMarkdownIncludesTheCueCardsWhereTheyBelong(t *testing.T) {
	cmds, err := ParseRecording("testdata/marks-script.jsonl")
	if err != nil || len(cmds) < 3 {
		t.Fatalf("%d commands, %v", len(cmds), err)
	}
	cmds[0].IntroCard, cmds[0].HasIntroCard = Annotation{Text: "Welcome to the **demo**."}, true
	cmds[1].Annotation, cmds[1].HasAnnotation = Annotation{Text: "A note."}, true
	cmds[1].IntroCard, cmds[1].HasIntroCard = Annotation{Text: "Now, an *intro*:\n\n[Download Trecs](https://github.com/solworktech/trecs/releases)"}, true
	cmds[1].PostCard, cmds[1].HasPostCard = Annotation{Text: "# not a heading\n- not a list\n<b>"}, true
	last := len(cmds) - 1
	cmds[last].PostCard, cmds[last].HasPostCard = Annotation{Text: "Thanks, and goodbye."}, true
	cmds[2].IntroCard, cmds[2].HasIntroCard = Annotation{Text: "   \n "}, true // no text: nothing

	md := MarkdownFromCommands("My session", cmds)

	heading := func(i int) int {
		h := "### " + mdInlineCode(CollapseCommand(cmds[i].InputText)) + "\n"
		at := strings.Index(md, h)
		if at < 0 {
			t.Fatalf("no heading for command %d (%q)\n%s", i, h, md)
		}
		return at
	}
	at := func(s string) int {
		i := strings.Index(md, s)
		if i < 0 {
			t.Errorf("Markdown lacks %q\n---\n%s", s, md)
		}
		return i
	}

	// An intro is a quote before the command's heading, its Markdown as it was written.
	intro0 := at("> Welcome to the **demo**.")
	if intro0 < 0 || intro0 > heading(0) {
		t.Errorf("the first intro is at %d, its command's heading at %d", intro0, heading(0))
	}
	intro1 := at("> Now, an *intro*:\n>\n> [Download Trecs](https://github.com/solworktech/trecs/releases)")
	if intro1 < heading(0) || intro1 > heading(1) {
		t.Errorf("the second intro must be between the first command and the second's heading: %d (%d, %d)", intro1, heading(0), heading(1))
	}
	// A note is still a paragraph after the heading; a post card is a quote after the output, before the next command.
	note := at("\nA note.\n")
	post1 := at("> \\# not a heading  \n> \\- not a list  \n> &lt;b>")
	if note < heading(1) || post1 < note || post1 > heading(2) {
		t.Errorf("heading %d, note %d, post card %d, next heading %d", heading(1), note, post1, heading(2))
	}
	if strings.Contains(md, "> A note.") {
		t.Error("the note became a quote")
	}
	// The last command's post card is the end of the document.
	if end := at("> Thanks, and goodbye."); end < heading(last) || !strings.HasSuffix(md, "> Thanks, and goodbye.\n") {
		t.Errorf("the last card is not at the end:\n%s", md)
	}
	// A card without text is nothing - not an empty quote: the third command has one, and between the end of the second's post card
	// and the third's heading there is nothing at all.
	postCard := "> \\# not a heading  \n> \\- not a list  \n> &lt;b>"
	if between := md[post1+len(postCard) : heading(2)]; strings.TrimSpace(between) != "" {
		t.Errorf("something between the post card and the next heading: %q", between)
	}
	// A recording without cards is exported as it was.
	plain, _ := ParseRecording("testdata/marks-script.jsonl")
	for i := range cmds {
		cmds[i].IntroCard, cmds[i].HasIntroCard = Annotation{}, false
		cmds[i].PostCard, cmds[i].HasPostCard = Annotation{}, false
		cmds[i].Annotation, cmds[i].HasAnnotation = Annotation{}, false
	}
	if MarkdownFromCommands("My session", cmds) != MarkdownFromCommands("My session", plain) {
		t.Error("with its cards taken away, the export is not the export of the recording")
	}
}
