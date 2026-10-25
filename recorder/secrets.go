package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"trecs/cloud"
	"trecs/secrets"

	libtrecs "trecs/lib"
)

// A terminal recording keeps everything that was on the screen: the tokens that were exported, the key that was catted, the
// password in a URL. Nothing leaves the machine before it is checked for them (the rules are gitleaks', see package secrets),
// and what is found is shown - masked: the report is not another place the secret is written to.

// secretHit is a secret found in a recording, and where.
type secretHit struct {
	where string // "output of command 3"
	secrets.Finding
}

// scanRecording looks for secrets in what was typed and what was printed, command by command, and in the annotations.
func scanRecording(terminal string) ([]secretHit, error) {
	commands, err := libtrecs.ParseRecording(terminal)
	if err != nil {
		return nil, err
	}
	_, annotations, _, err := libtrecs.LoadFrames(terminal)
	if err != nil {
		return nil, err
	}
	var hits []secretHit
	add := func(where, text string) {
		for _, f := range secrets.Default().Scan(text) {
			hits = append(hits, secretHit{where, f})
		}
	}
	for i, c := range commands {
		add(fmt.Sprintf("typed in command %d", i+1), c.InputText)
		add(fmt.Sprintf("output of command %d", i+1), c.OutputText)
	}
	for _, a := range annotations {
		add("an annotation", a.Text)
	}
	return hits, nil
}

// reportSecrets says what was found, with each secret masked.
func reportSecrets(w io.Writer, hits []secretHit) {
	say(w, "%s\n", red(w, fmt.Sprintf("Found %d possible secret(s) in the recording:", len(hits))))
	for _, h := range hits {
		say(w, "%s\n", red(w, fmt.Sprintf("  %s, line %d: %s [%s] %s", h.where, h.Line, shortDescription(h.Description), h.RuleID, secrets.Mask(h.Secret))))
	}
}

// shortDescription is the first sentence of a rule's description, which is what is wanted in a list.
func shortDescription(d string) string {
	if i := strings.Index(d, ", "); i > 0 && i < 80 {
		d = d[:i]
	}
	return strings.TrimRight(d, ".")
}

// letSecretsGo decides whether a recording in which secrets were found may be uploaded. Nothing found: yes. With allow
// (-allow-secrets): yes, having said so. Otherwise a person at a terminal is asked, and only the exact answer YES goes on; with
// nobody to ask (CI, a pipe) the answer is no.
func letSecretsGo(hits []secretHit, allow bool, in io.Reader, out io.Writer, interactive, shown bool) bool {
	if len(hits) == 0 {
		return true
	}
	if !shown {
		reportSecrets(out, hits)
	}
	switch {
	case allow:
		say(out, "%s\n", "Going on, as -allow-secrets was given.")
		return true
	case !interactive:
		say(out, "%s\n", "Not uploaded. Take the secrets out first (trecs-edit), or give -allow-secrets if they are fine to share.")
		return false
	}
	say(out, "%s", yellow(out, "Type 'YES' to upload it anyway, any other input to abort: "))
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) == "YES" {
		return true
	}
	say(out, "%s\n", "Not uploaded.")
	return false
}

// checkBeforeUpload scans a recording and applies letSecretsGo. A recording that cannot be read is not uploaded either.
func checkBeforeUpload(terminal string, allow bool) bool {
	hits, err := scanRecording(terminal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not check %s for secrets (%v): not uploaded.\n", terminal, err)
		return false
	}
	return letSecretsGo(hits, allow, os.Stdin, os.Stderr, stdinIsTerminal(), false)
}

// runScan is `scan PATH`: the check on its own. It exits 1 when something is found.
func runScan(args []string) {
	if len(args) != 1 {
		fail("usage: %s scan <session directory | terminal.jsonl>", os.Args[0])
	}
	terminal, _, err := cloud.ResolveSessionFiles(args[0])
	if err != nil {
		fail("%v", err)
	}
	hits, err := scanRecording(terminal)
	if err != nil {
		fail("%v", err)
	}
	if len(hits) == 0 {
		fmt.Println("No secrets found.")
		return
	}
	reportSecrets(os.Stdout, hits)
	os.Exit(1)
}
