package secrets

import (
	"strings"
	"testing"
)

func ids(fs []Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.RuleID)
	}
	return strings.Join(out, ",")
}

func TestEveryRuleCompiles(t *testing.T) {
	s := Default()
	if s.Rules() < 200 {
		t.Fatalf("only %d rules", s.Rules())
	}
	for _, r := range s.rules {
		if r.compiled() == nil {
			t.Errorf("rule %s: its pattern does not compile", r.ID)
		}
	}
}

func TestFindsKnownKindsOfSecret(t *testing.T) {
	cases := map[string]string{
		"github-pat":          "export GITHUB_TOKEN=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",
		"private-key":         "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAxkZqY0r4hCj8vQ0m9wqk4Xz3mT0VtJmVwq7r5y1nX9w8k2sLqD3pEoHfYb6zU7aN\n-----END RSA PRIVATE KEY-----",
		"slack-bot-token":     "SLACK=xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx",
		"stripe-access-token": "STRIPE_KEY=sk_live_4eC39HqLyjWDarjtT1zdp7dc ",
	}
	for want, text := range cases {
		got := Default().Scan("$ " + text + "\nok\n")
		if !strings.Contains(ids(got), want) {
			t.Errorf("%s: found %q in %q", want, ids(got), text)
		}
	}
}

func TestOrdinaryTerminalTextHasNoSecrets(t *testing.T) {
	text := "$ ls -la\ntotal 12\ndrwxr-xr-x 2 jesse jesse 4096 Oct 20 21:00 .\n$ echo $HOME\n/home/jesse\n$ git log --oneline\n" +
		"3f2a9c1 Fix the thing\n$ export TOKEN=$MY_TOKEN\n$ curl -u user:password https://example.com\n$ echo AKIAIOSFODNN7EXAMPLE\n"
	if got := Default().Scan(text); len(got) != 0 {
		t.Errorf("found %q (%v)", ids(got), got)
	}
}

func TestTheSameSecretTwiceIsOneFindingAndTheLineIsGiven(t *testing.T) {
	tok := "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"
	got := Default().Scan("one\ntwo\n$ echo " + tok + "\n" + tok + "\n")
	if len(got) != 1 || got[0].Line != 3 || got[0].Secret != tok {
		t.Errorf("got %+v", got)
	}
}

func TestMaskHidesTheMiddle(t *testing.T) {
	if got := Mask("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"); got != "ghp_…Ux" {
		t.Errorf("got %q", got)
	}
	if got := Mask("short"); got != "…" {
		t.Errorf("got %q", got)
	}
}

func BenchmarkScanMegabyte(b *testing.B) {
	text := strings.Repeat("drwxr-xr-x 2 jesse jesse 4096 Oct 20 21:00 some-directory-name\n", 16000)
	for i := 0; i < b.N; i++ {
		Default().Scan(text)
	}
}

func TestTheCatchAllRuleDoesNotRepeatWhatASpecificRuleFound(t *testing.T) {
	got := Default().Scan("export GITHUB_TOKEN=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\n")
	if ids(got) != "github-pat" {
		t.Errorf("found %q", ids(got))
	}
}
