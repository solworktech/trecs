// Package secrets finds credentials in text: API keys, tokens, private keys, passwords in URLs...
//
// It runs the rules of gitleaks (github.com/gitleaks/gitleaks, MIT licence - see LICENSE.gitleaks), whose default
// configuration is embedded as rules.json (regenerate it with gen_rules.py). gitleaks is not imported: it is built to scan git
// repositories, and its packages bring a few dozen modules (archive formats, a WASM runtime, a config framework) that
// a recorder of two small static binaries has no use for. What is needed of it - a pattern, the keywords that make it worth
// running, the entropy a real secret has, and what to ignore - is small, and is done here the way gitleaks does it:
//
//  1. a rule runs only if one of its keywords is in the text (a cheap test that skips nearly all of them);
//  2. for each match of its pattern the secret is the rule's secretGroup, else the first capture group that matched,
//     else the whole match;
//  3. a secret whose Shannon entropy is not above the rule's floor is dropped (a word is not a key);
//  4. a secret that the global or the rule's own allowlist accepts - a regex on the secret, the match or the line, or a stop
//     word in it ("example", "changeme"...) - is dropped.
package secrets

import (
	_ "embed"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed rules.json
var rulesJSON []byte

// Finding is a secret found in a text.
type Finding struct {
	RuleID      string  // gitleaks' id for the kind of secret: "aws-access-token"
	Description string  // what it is, in words
	Line        int     // the line of the text it begins on, from 1
	Secret      string  // the secret itself: not to be shown or stored as it is (see Mask)
	Entropy     float64 // its Shannon entropy, bits per character
}

// Mask is the secret with its middle hidden, enough to recognise it by and not to use.
func Mask(secret string) string {
	r := []rune(secret)
	switch {
	case len(r) >= 16:
		return string(r[:4]) + "…" + string(r[len(r)-2:])
	case len(r) >= 8:
		return string(r[:2]) + "…"
	}
	return "…"
}

type allowlistJSON struct {
	Regexes     []string `json:"regexes"`
	Stopwords   []string `json:"stopwords"`
	RegexTarget string   `json:"regexTarget"`
	Condition   string   `json:"condition"`
}

type ruleJSON struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Regex       string          `json:"regex"`
	Keywords    []string        `json:"keywords"`
	Entropy     float64         `json:"entropy"`
	SecretGroup int             `json:"secretGroup"`
	Allowlists  []allowlistJSON `json:"allowlists"`
}

type rulesFile struct {
	Allowlist allowlistJSON `json:"allowlist"`
	Rules     []ruleJSON    `json:"rules"`
}

// allowlist is an allowlistJSON with its patterns compiled.
type allowlist struct {
	regexes   []*regexp.Regexp
	stopwords []string
	target    string // "secret" (the default), "match" or "line"
	and       bool   // every kind of test given must accept, instead of any
}

func compileAllowlist(a allowlistJSON) allowlist {
	out := allowlist{target: a.RegexTarget, and: strings.EqualFold(a.Condition, "AND")}
	if out.target == "" {
		out.target = "secret"
	}
	for _, p := range a.Regexes {
		if re, err := regexp.Compile(p); err == nil {
			out.regexes = append(out.regexes, re)
		}
	}
	for _, w := range a.Stopwords {
		out.stopwords = append(out.stopwords, strings.ToLower(w))
	}
	return out
}

// allows says whether the allowlist accepts a finding, given the secret, the whole match, and its line.
func (a allowlist) allows(secret, match, line string) bool {
	if len(a.regexes) == 0 && len(a.stopwords) == 0 {
		return false
	}
	target := secret
	switch a.target {
	case "match":
		target = match
	case "line":
		target = line
	}
	byRegex := false
	for _, re := range a.regexes {
		if re.MatchString(target) {
			byRegex = true
			break
		}
	}
	byWord := false
	lower := strings.ToLower(secret)
	for _, w := range a.stopwords {
		if strings.Contains(lower, w) {
			byWord = true
			break
		}
	}
	if a.and {
		return (len(a.regexes) == 0 || byRegex) && (len(a.stopwords) == 0 || byWord)
	}
	return byRegex || byWord
}

// rule is a rule that compiles its pattern when it is first needed: most are never, and compiling all of them is a tenth of a second.
type rule struct {
	ruleJSON
	keywords   []string
	once       sync.Once
	re         *regexp.Regexp
	allowlists []allowlist
}

func (r *rule) compiled() *regexp.Regexp {
	r.once.Do(func() {
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			return // a pattern this regexp cannot take: the rule is left out
		}
		r.re = re
		for _, a := range r.Allowlists {
			r.allowlists = append(r.allowlists, compileAllowlist(a))
		}
	})
	return r.re
}

// Scanner finds secrets in text. It is safe for concurrent use.
type Scanner struct {
	rules  []*rule
	global allowlist
}

var (
	defaultOnce    sync.Once
	defaultScanner *Scanner
)

// Default is the scanner with gitleaks' default rules.
func Default() *Scanner {
	defaultOnce.Do(func() {
		var f rulesFile
		if err := json.Unmarshal(rulesJSON, &f); err != nil {
			panic("secrets: rules.json: " + err.Error()) // a build with a broken file in it
		}
		s := &Scanner{global: compileAllowlist(f.Allowlist)}
		for _, rj := range f.Rules {
			r := &rule{ruleJSON: rj}
			for _, k := range rj.Keywords {
				r.keywords = append(r.keywords, strings.ToLower(k))
			}
			s.rules = append(s.rules, r)
		}
		defaultScanner = s
	})
	return defaultScanner
}

// Rules is how many rules there are.
func (s *Scanner) Rules() int { return len(s.rules) }

// Scan returns the secrets in text, in the order they occur. The same secret found by the same rule twice is one finding (a command is
// echoed and then its output says it again).
func (s *Scanner) Scan(text string) []Finding {
	if len(text) < 8 {
		return nil
	}
	lower := strings.ToLower(text)
	type pos struct {
		at int
		f  Finding
	}
	var found []pos
	seen := map[string]bool{}
	for _, r := range s.rules {
		if !hasKeyword(lower, r.keywords) {
			continue
		}
		re := r.compiled()
		if re == nil {
			continue
		}
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			match := strings.Trim(text[m[0]:m[1]], "\n")
			secret := match
			if r.SecretGroup > 0 && 2*r.SecretGroup+1 < len(m) && m[2*r.SecretGroup] >= 0 {
				secret = text[m[2*r.SecretGroup]:m[2*r.SecretGroup+1]]
			} else if r.SecretGroup == 0 {
				for g := 1; 2*g+1 < len(m); g++ {
					if m[2*g] >= 0 && m[2*g+1] > m[2*g] {
						secret = text[m[2*g]:m[2*g+1]]
						break
					}
				}
			}
			entropy := shannon(secret)
			if r.Entropy != 0 && entropy <= r.Entropy {
				continue
			}
			line := lineAround(text, m[0])
			if s.global.allows(secret, match, line) {
				continue
			}
			allowed := false
			for _, a := range r.allowlists {
				if a.allows(secret, match, line) {
					allowed = true
					break
				}
			}
			if allowed {
				continue
			}
			key := r.ID + "\x00" + secret
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, pos{m[0], Finding{
				RuleID: r.ID, Description: r.Description, Secret: secret, Entropy: entropy,
				Line: 1 + strings.Count(text[:m[0]], "\n"),
			}})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].at < found[j].at })
	var out []Finding
	for i, p := range found {
		if p.f.RuleID == genericRule {
			covered := false
			for j, q := range found {
				if j != i && q.f.RuleID != genericRule && (strings.Contains(q.f.Secret, p.f.Secret) || strings.Contains(p.f.Secret, q.f.Secret)) {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
		}
		out = append(out, p.f)
	}
	return out
}

// genericRule is the catch-all rule: it fires on anything that looks like a value next to a word like "token", including what a
// specific rule has just recognised - which is then one secret, and not two.
const genericRule = "generic-api-key"

func hasKeyword(lowerText string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	for _, k := range keywords {
		if strings.Contains(lowerText, k) {
			return true
		}
	}
	return false
}

// lineAround is the line of text that offset is in.
func lineAround(text string, offset int) string {
	start := strings.LastIndexByte(text[:offset], '\n') + 1
	end := strings.IndexByte(text[offset:], '\n')
	if end < 0 {
		return text[start:]
	}
	return text[start : offset+end]
}

// shannon is the Shannon entropy of s, in bits per character.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	inv := 1 / float64(len(s))
	var e float64
	for _, c := range counts {
		f := float64(c) * inv
		e -= f * math.Log2(f)
	}
	return e
}
