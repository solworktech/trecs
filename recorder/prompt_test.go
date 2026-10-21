package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOnlyYESConfirmsAScript(t *testing.T) {
	for in, want := range map[string]bool{
		"YES\n": true, "YES\r\n": true, "  YES \n": true,
		"yes\n": false, "Y\n": false, "\n": false, "": false, "no\n": false, "YESS\n": false, "x": false,
	} {
		var out bytes.Buffer
		if got := confirmScript("commands.txt", strings.NewReader(in), &out, true, 0); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
		if !strings.Contains(out.String(), "commands.txt") || !strings.Contains(out.String(), "Type 'YES' to continue, any other input to exit") {
			t.Errorf("the warning and the question should be shown: %q", out.String())
		}
	}
}

func TestAScriptNeedsNoConfirmationWhereThereIsNoOneToAsk(t *testing.T) {
	var out bytes.Buffer
	if !confirmScript("ci.txt", strings.NewReader(""), &out, false, 0) {
		t.Error("stopped, with nobody to ask")
	}
	if !strings.Contains(out.String(), "WARNING") || strings.Contains(out.String(), "Type 'YES'") {
		t.Errorf("warned, but did not ask: %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("colour sent to something that is not a terminal: %q", out.String())
	}
}

func TestOnlyYEnterAndLowerYAreYes(t *testing.T) {
	for _, c := range []byte("yY\r\n") {
		if !yesKey(c) {
			t.Errorf("%q is a yes", c)
		}
	}
	for _, c := range []byte("nNq x\x03\x1b0") {
		if yesKey(c) {
			t.Errorf("%q is not a yes", c)
		}
	}
}

func TestServerPrecedence(t *testing.T) {
	for _, c := range []struct{ flag, saved, env, built, want string }{
		{"https://flag", "https://saved", "https://env", "https://built", "https://flag"},
		{"", "https://saved", "https://env", "https://built", "https://saved"},
		{"", "", "https://env", "https://built", "https://env"},
		{"", "", "", "https://built", "https://built"},
		{"", "", "", "", ""},
	} {
		if got := resolveServer(c.flag, c.saved, c.env, c.built); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestTheAuditWarningsAreCountedBeforeTheQuestion(t *testing.T) {
	var out bytes.Buffer
	confirmScript("/tmp/in.sh", strings.NewReader("no\n"), &out, true, 3)
	s := out.String()
	line := "3 warnings were triggered when auditing /tmp/in.sh. Review carefully before typing 'YES'."
	i, j := strings.Index(s, line), strings.Index(s, "Type 'YES'")
	if i < 0 || j < 0 || i > j {
		t.Errorf("the count should come before the question: %q", s)
	}
	out.Reset()
	confirmScript("/tmp/in.sh", strings.NewReader("no\n"), &out, true, 1)
	if !strings.Contains(out.String(), "1 warning was triggered when auditing /tmp/in.sh.") {
		t.Errorf("%q", out.String())
	}
	out.Reset()
	confirmScript("/tmp/in.sh", strings.NewReader("no\n"), &out, true, 0)
	if strings.Contains(out.String(), "triggered") {
		t.Errorf("no warnings, but a count: %q", out.String())
	}
}
