package main

import (
	"strings"
	"testing"

	libtrecs "trecs/lib"
)

const testToken = "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"

// feed pushes frames through a masker the way the sender does, and returns everything it sends, in order.
func feed(m *liveMasker, datas ...string) (out []libtrecs.TerminalFrame) {
	for i, d := range datas {
		out = append(out, m.Frame(libtrecs.TerminalFrame{Timestamp: int64(i * 10), Data: d})...)
	}
	return append(out, m.Flush()...)
}

func joined(fs []libtrecs.TerminalFrame) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.Data)
	}
	return b.String()
}

func TestOrdinaryOutputIsSentAsItIs(t *testing.T) {
	in := []string{"$ ls -l\r\n", "total 8\r\n", "-rw-r--r-- 1 me me 12 Oct 25 notes.txt\r\n", "$ "}
	got := joined(feed(newLiveMasker(), in...))
	if got != strings.Join(in, "") {
		t.Errorf("%q", got)
	}
}

func TestASecretInOneFrameIsMaskedAndTheShapeOfTheScreenIsKept(t *testing.T) {
	got := joined(feed(newLiveMasker(), "$ export GITHUB_TOKEN="+testToken+"\r\nok\r\n"))
	if strings.Contains(got, testToken) || strings.Contains(got, testToken[4:20]) {
		t.Fatalf("the secret got out: %q", got)
	}
	want := "$ export GITHUB_TOKEN=" + strings.Repeat("*", len(testToken)) + "\r\nok\r\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestASecretCutBetweenFramesIsNotSentInHalves(t *testing.T) {
	for cutAt := 1; cutAt < len(testToken); cutAt += 7 {
		line := "echo " + testToken + "\r\n"
		i := len("echo ") + cutAt
		got := joined(feed(newLiveMasker(), line[:i], line[i:]))
		if strings.Contains(got, testToken[:12]) || strings.Contains(got, testToken[len(testToken)-12:]) {
			t.Errorf("cut at %d: %q", cutAt, got)
		}
		if len(got) != len(line) {
			t.Errorf("cut at %d: the screen changed shape: %q", cutAt, got)
		}
	}
}

func TestASecretTypedOneCharacterAtATimeIsMasked(t *testing.T) {
	var datas []string
	for _, r := range "export T=" + testToken + "\r\n" {
		datas = append(datas, string(r))
	}
	got := joined(feed(newLiveMasker(), datas...))
	if strings.Contains(got, testToken[8:30]) {
		t.Errorf("%q", got)
	}
}

func TestAPrivateKeyIsMaskedFromItsHeaderToItsFooterWhateverTheFramesAre(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\r\nMIIEowIBAAKCAQEAxkZqY0r4hCj8vQ0m9wqk4Xz3mT0VtJmVwq7r5y1nX9w8k2sLqD3pEoHfYb6zU7aN\r\nabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ\r\n-----END RSA PRIVATE KEY-----\r\n"
	for _, size := range []int{1, 5, 17, 40, len(key)} {
		var datas []string
		text := "$ cat id_rsa\r\n" + key + "$ echo done\r\n"
		for i := 0; i < len(text); i += size {
			datas = append(datas, text[i:min(i+size, len(text))])
		}
		got := joined(feed(newLiveMasker(), datas...))
		for _, leak := range []string{"MIIEowIBAAK", "abcdefghijklmnop", "ZqY0r4hCj8"} {
			if strings.Contains(got, leak) {
				t.Errorf("frames of %d: part of the key got out (%q): %q", size, leak, got)
			}
		}
		if !strings.HasPrefix(got, "$ cat id_rsa\r\n") || !strings.HasSuffix(got, "$ echo done\r\n") {
			t.Errorf("frames of %d: what is around the key must be sent: %q", size, got)
		}
	}
}

func TestAKeyThatNeverEndsIsNotMaskedForEver(t *testing.T) {
	m := newLiveMasker()
	feed(m, "-----BEGIN PRIVATE KEY-----\r\n")
	var got string
	for i := 0; i < 400; i++ {
		got = joined(feed(m, strings.Repeat("x", 100)+"\r\n"))
	}
	if strings.Contains(got, "*") {
		t.Errorf("still masking after far more than a key: %q", got[:40])
	}
}

func TestAWordHeldBackKeepsTheTimeItBegan(t *testing.T) {
	m := newLiveMasker()
	if got := m.Frame(libtrecs.TerminalFrame{Timestamp: 100, Data: "ls "}); len(got) != 1 || got[0].Timestamp != 100 || got[0].Data != "ls " {
		t.Fatalf("%+v", got)
	}
	if got := m.Frame(libtrecs.TerminalFrame{Timestamp: 200, Data: "-l"}); got != nil {
		t.Fatalf("the word being typed is held: %+v", got)
	}
	if !m.Holding() {
		t.Error("holding")
	}
	got := m.Frame(libtrecs.TerminalFrame{Timestamp: 300, Data: "a\r\n"})
	if len(got) != 1 || got[0].Timestamp != 200 || got[0].Data != "-la\r\n" {
		t.Errorf("the word is sent with the time it began: %+v", got)
	}
	m.Frame(libtrecs.TerminalFrame{Timestamp: 400, Data: "$"})
	if got := m.Flush(); len(got) != 1 || got[0].Timestamp != 400 || got[0].Data != "$" || m.Holding() {
		t.Errorf("a quiet moment lets the last word go: %+v", got)
	}
}

func TestALongRunWithoutSpacesIsNotHeldForEver(t *testing.T) {
	got := feed(newLiveMasker(), strings.Repeat("=", 2000))
	if joined(got) != strings.Repeat("=", 2000) {
		t.Errorf("%d bytes", len(joined(got)))
	}
}

func TestTheShortAndCommonAreNotMasked(t *testing.T) {
	in := "password: ok\r\ntoken=1\r\n"
	if got := joined(feed(newLiveMasker(), in)); got != in {
		t.Errorf("%q", got)
	}
}
