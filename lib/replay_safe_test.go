package lib

import "testing"

func TestSafeForTerminalDropsWhatAsksOrActsAndKeepsWhatDraws(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"text and colour", "a\x1b[1;31mred\x1b[0m\r\n", "a\x1b[1;31mred\x1b[0m\r\n"},
		{"cursor movement", "\x1b[2J\x1b[H\x1b[10;5H", "\x1b[2J\x1b[H\x1b[10;5H"},
		{"cursor shape", "\x1b[5 q", "\x1b[5 q"},
		{"device attributes", "a\x1b[cb\x1b[>0cc\x1b[=c", "abc"},
		{"cursor position report request", "a\x1b[6nb\x1b[?6nc", "abc"},
		{"window report", "a\x1b[21tb\x1b[18tc", "abc"},
		{"version", "a\x1b[>0qb", "ab"},
		{"mode query", "a\x1b[?2004$pb", "ab"},
		{"clipboard", "a\x1b]52;c;SGVsbG8=\x07b\x1b]52;c;?\x1b\\c", "abc"},
		{"colour query", "a\x1b]11;?\x07b", "ab"},
		{"colour set", "\x1b]4;1;rgb:ff/00/00\x07", "\x1b]4;1;rgb:ff/00/00\x07"},
		{"title", "\x1b]0;my title\x07", "\x1b]0;my title\x07"},
		{"hyperlink", "\x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\", "\x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\"},
		{"prompt mark", "\x1b]133;A\x07$ ", "\x1b]133;A\x07$ "},
		{"inline image", "\x1b]1337;File=inline=1:AAAA\x07", "\x1b]1337;File=inline=1:AAAA\x07"},
		{"iterm2 report", "a\x1b]1337;ReportCellSize\x07b", "ab"},
		{"sixel", "\x1bPq#0;2;0;0;0~-\x1b\\", "\x1bPq#0;2;0;0;0~-\x1b\\"},
		{"dcs query", "a\x1bP$qm\x1b\\b\x1bP+q544e\x1b\\c", "abc"},
		{"apc", "a\x1b_Gi=1;AAAA\x1b\\b", "ab"},
		{"decid", "a\x1bZb", "ab"},
		{"c1 as utf-8", "a\u009b6nb\u009d52;c;QQ==\u0007c", "a6nb52;c;QQ==\u0007c"},
	}
	for _, c := range cases {
		got, carry := SafeForTerminal("", c.in)
		if got != c.want || carry != "" {
			t.Errorf("%s: %q -> %q (carry %q), want %q", c.name, c.in, got, carry, c.want)
		}
	}
}

func TestSafeForTerminalJudgesASequenceCutBetweenFramesAsAWhole(t *testing.T) {
	for _, cut := range []int{1, 2, 3, 5, 9} {
		in := "x\x1b]52;c;SGVsbG8=\x07y"
		a, carry := SafeForTerminal("", in[:cut])
		b, carry := SafeForTerminal(carry, in[cut:])
		if a+b != "xy" || carry != "" {
			t.Errorf("cut at %d: %q + %q (carry %q)", cut, a, b, carry)
		}
	}
}
