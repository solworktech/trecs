// Package scriptaudit reads a script before it is run and says what in it can do harm, or is not what it looks like.
//
// It is a seat belt, not a sandbox. A script is parsed (bash, POSIX sh, ksh; any other shell, fish included, is read
// more roughly), and the commands in it are checked against a list of what is dangerous to run without having been
// looked at: a download that is run, a recursive remove of a variable, a write to a disk or to a startup file, a secret that is sent
// away, a machine that is shut down... A finding is a Block (nothing should run before a person has agreed) or a Warn (said, but
// not in the way). What the script does with eval, a variable or a command that it writes itself cannot be known from reading
// it, so a script made to get past this will: where that matters, the answer is to run it where it can do no harm.
//
// The shell is parsed with mvdan.cc/sh (BSD-3-Clause).
package scriptaudit

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// Severity is how serious a finding is.
type Severity int

const (
	// Warn is said, and does not stop anything.
	Warn Severity = iota + 1
	// Block stops the script from being run until a person has said it may be (-allow-risky).
	Block
)

func (s Severity) String() string {
	if s == Block {
		return "BLOCK"
	}
	return "warn"
}

// Finding is something in a script.
type Finding struct {
	Line     int // from 1; 0 for the file as a whole
	Severity Severity
	Rule     string
	Message  string
	Text     string // the command, as it is written
}

func (f Finding) String() string {
	return fmt.Sprintf("line %d: %s [%s] %s: %s", f.Line, f.Severity, f.Rule, f.Message, f.Text)
}

// Dialect is the language a script is read as.
type Dialect int

const (
	Bash Dialect = iota
	POSIX
	Korn
	Other // fish, and what is not known: read by splitting lines, not by parsing
)

// DialectFor is the dialect of a shell given its name or path.
func DialectFor(shell string) Dialect {
	switch strings.TrimPrefix(path.Base(strings.ReplaceAll(shell, "\\", "/")), "-") {
	case "bash", "zsh":
		return Bash
	case "sh", "dash", "ash":
		return POSIX
	case "ksh", "mksh", "ksh93":
		return Korn
	}
	return Other
}

// Audit reads a script and returns what it found, in the order of the lines.
func Audit(script string, d Dialect) []Finding {
	var cmds []pipeline
	var forkbombs []Finding
	lang := map[Dialect]syntax.LangVariant{Bash: syntax.LangBash, POSIX: syntax.LangPOSIX, Korn: syntax.LangMirBSDKorn}[d]
	parsed := false
	if d != Other {
		if f, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(script), ""); err == nil {
			cmds, forkbombs = fromAST(f, script)
			parsed = true
		}
	}
	if !parsed {
		cmds = fromLines(script)
		forkbombs = roughForkBombs(script)
	}

	var out []Finding
	out = append(out, forkbombs...)
	out = append(out, textFindings(script)...)
	for _, p := range cmds {
		out = append(out, checkPipeline(p)...)
	}
	if !parsed && d != Other {
		out = append(out, Finding{Severity: Warn, Rule: "unparsed", Message: "the file could not be read as a script in this shell's language, so it was checked roughly"})
	}
	return dedupe(out)
}

// Blocking says whether any of the findings is a Block.
func Blocking(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == Block {
			return true
		}
	}
	return false
}

func dedupe(fs []Finding) []Finding {
	seen := map[string]bool{}
	var out []Finding
	for _, f := range fs {
		k := fmt.Sprintf("%d|%s|%s", f.Line, f.Rule, f.Text)
		if !seen[k] {
			seen[k] = true
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ---- what a script is made of, whichever way it was read ----------------------------------------------------------

// word is one word of a command. lit says it is all literal text: no variable, no command substitution.
type word struct {
	text string
	lit  bool
	subs []string // the commands inside a $(...) or <(...) in it, as text
}

type redirect struct {
	op     string // ">", ">>", "<", "&>"...
	target word
}

type simple struct {
	line       int
	words      []word
	redirs     []redirect
	background bool
	text       string
}

type pipeline struct {
	line   int
	stages []simple
}

// fromAST collects the pipelines of a parsed script, and any function that calls itself from a pipeline or in the background.
func fromAST(f *syntax.File, src string) (ps []pipeline, bombs []Finding) {
	pr := syntax.NewPrinter()
	render := func(n syntax.Node) string {
		var b bytes.Buffer
		_ = pr.Print(&b, n)
		return strings.TrimSpace(b.String())
	}
	mkWord := func(w *syntax.Word) word {
		out := word{lit: true}
		var sb strings.Builder
		var parts func(ps []syntax.WordPart)
		parts = func(ps []syntax.WordPart) {
			for _, p := range ps {
				switch p := p.(type) {
				case *syntax.Lit:
					sb.WriteString(p.Value)
				case *syntax.SglQuoted:
					sb.WriteString(p.Value)
				case *syntax.DblQuoted:
					parts(p.Parts)
				case *syntax.CmdSubst:
					out.lit = false
					out.subs = append(out.subs, render(p))
					sb.WriteString("$(…)")
				case *syntax.ProcSubst:
					out.lit = false
					out.subs = append(out.subs, render(p))
					sb.WriteString("<(…)")
				default:
					out.lit = false
					sb.WriteString(render(p))
				}
			}
		}
		parts(w.Parts)
		out.text = sb.String()
		return out
	}
	mkSimple := func(st *syntax.Stmt) (simple, bool) {
		s := simple{line: int(st.Pos().Line()), background: st.Background, text: sourceOf(st, src, render)}
		call, ok := st.Cmd.(*syntax.CallExpr)
		if ok {
			for _, a := range call.Args {
				s.words = append(s.words, mkWord(a))
			}
		}
		for _, r := range st.Redirs {
			if r.Word != nil {
				s.redirs = append(s.redirs, redirect{op: r.Op.String(), target: mkWord(r.Word)})
			}
		}
		return s, ok || len(s.redirs) > 0
	}
	var flatten func(st *syntax.Stmt) []*syntax.Stmt
	flatten = func(st *syntax.Stmt) []*syntax.Stmt {
		if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && (b.Op == syntax.Pipe || b.Op == syntax.PipeAll) {
			return append(flatten(b.X), flatten(b.Y)...)
		}
		return []*syntax.Stmt{st}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Stmt:
			if _, ok := n.Cmd.(*syntax.BinaryCmd); ok {
				stages := flatten(n)
				if len(stages) < 2 {
					return true
				}
				p := pipeline{line: int(n.Pos().Line())}
				for _, st := range stages {
					if s, ok := mkSimple(st); ok {
						p.stages = append(p.stages, s)
					}
				}
				p.stages[len(p.stages)-1].background = p.stages[len(p.stages)-1].background || n.Background
				ps = append(ps, p)
				return true
			}
			if s, ok := mkSimple(n); ok {
				ps = append(ps, pipeline{line: s.line, stages: []simple{s}})
			}
		case *syntax.FuncDecl:
			name := n.Name.Value
			calls := 0
			syntax.Walk(n.Body, func(m syntax.Node) bool {
				if b, ok := m.(*syntax.BinaryCmd); ok && b.Op == syntax.Pipe {
					for _, st := range flatten(&syntax.Stmt{Cmd: b}) {
						if c, ok := st.Cmd.(*syntax.CallExpr); ok && len(c.Args) > 0 && mkWord(c.Args[0]).text == name {
							calls++
						}
					}
				}
				return true
			})
			if calls >= 2 {
				bombs = append(bombs, Finding{Line: int(n.Pos().Line()), Severity: Block, Rule: "fork-bomb", Message: "a function that runs itself twice in a pipeline: a fork bomb, which takes the machine down", Text: render(n)})
			}
		}
		return true
	})
	return ps, bombs
}

// sourceOf is a statement as it was written (not as the printer would write it), a pipeline stage being cut out of its line.
func sourceOf(n syntax.Node, src string, render func(syntax.Node) string) string {
	a, b := int(n.Pos().Offset()), int(n.End().Offset())
	if a >= 0 && b <= len(src) && a < b {
		return strings.TrimSpace(src[a:b])
	}
	return render(n)
}

// fromLines is the rough reading, for a shell that is not parsed: each line split into pipelines at ; && || and |, words at spaces
// (quotes kept together). Anything with a $ or a ( in it is not literal.
func fromLines(script string) []pipeline {
	var ps []pipeline
	for i, line := range strings.Split(script, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		for _, seg := range splitTop(trim, []string{"&&", "||", ";"}) {
			stages := splitTop(seg, []string{"|"})
			p := pipeline{line: i + 1}
			for _, st := range stages {
				p.stages = append(p.stages, roughSimple(i+1, st))
			}
			ps = append(ps, p)
		}
	}
	return ps
}

// splitTop splits at the separators that are not in quotes or in brackets.
func splitTop(s string, seps []string) []string {
	var parts []string
	var quote rune
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		c := rune(s[i])
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		case c == '\\':
			i++
			continue
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			depth--
		}
		if depth > 0 {
			continue
		}
		for _, sep := range seps {
			if strings.HasPrefix(s[i:], sep) {
				if sep == "|" && (strings.HasPrefix(s[i:], "||") || (i > 0 && s[i-1] == '|')) {
					continue
				}
				parts = append(parts, s[start:i])
				start = i + len(sep)
				i += len(sep) - 1
				break
			}
		}
	}
	return append(parts, s[start:])
}

func roughSimple(line int, text string) simple {
	s := simple{line: line, text: strings.TrimSpace(text)}
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		t := cur.String()
		cur.Reset()
		s.words = append(s.words, word{text: t, lit: !strings.ContainsAny(t, "$(`")})
	}
	for _, c := range text {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	if n := len(s.words); n > 0 && s.words[n-1].text == "&" {
		s.background = true
		s.words = s.words[:n-1]
	}
	// redirections: ">" ">>" ">file" ">>file"
	var words []word
	for i := 0; i < len(s.words); i++ {
		w := s.words[i]
		if m := redirRe.FindStringSubmatch(w.text); m != nil {
			target := word{text: m[2], lit: !strings.ContainsAny(m[2], "$(`")}
			if m[2] == "" && i+1 < len(s.words) {
				target = s.words[i+1]
				i++
			}
			s.redirs = append(s.redirs, redirect{op: m[1], target: target})
			continue
		}
		words = append(words, w)
	}
	s.words = words
	return s
}

var redirRe = regexp.MustCompile(`^(&>>|&>|>>|>\||>)(.*)$`)

// roughForkBombs looks for the classic :(){ :|:& };: by its text. It is only
// used when the script could not be parsed, because the parsed reading (see
// fromAST) catches this and every other self-calling pipeline.
func roughForkBombs(script string) []Finding {
	var out []Finding
	for i, line := range strings.Split(script, "\n") {
		if forkBomb.MatchString(line) {
			out = append(out, Finding{Line: i + 1, Severity: Block, Rule: "fork-bomb", Message: "a function that runs itself twice in a pipeline: a fork bomb, which takes the machine down", Text: strings.TrimSpace(line)})
		}
	}
	return out
}

// textFindings are what is wrong with the text itself: characters that make it read differently from what it does.
func textFindings(script string) []Finding {
	var out []Finding
	for i, line := range strings.Split(script, "\n") {
		for _, r := range line {
			if unicode.Is(unicode.Cf, r) {
				out = append(out, Finding{Line: i + 1, Severity: Block, Rule: "hidden-text", Message: fmt.Sprintf("an invisible or direction-changing character (%U): the line may read differently from what it does", r), Text: strings.TrimSpace(line)})
				break
			}
		}
	}
	return out
}
