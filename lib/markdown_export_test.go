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
