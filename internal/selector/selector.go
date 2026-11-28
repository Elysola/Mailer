package selector

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/elysola/mailer/internal/core"
)

// The selector language is the shared boolean expression syntax of route
// `when` clauses and processor `when` preconditions:
//
//	fields.otp && from =~ "@github.com" || subject contains "verification"
//	has(attachments) && !(source == "alerts")
//
// Operators: && || ! ( ) == != =~ !~ contains  plus the predicates
// has(x) / present(x) testing non-emptiness. Field references are dotted
// paths into the envelope (see resolveField). Bare strings in single or
// double quotes are literals.

// MatchSelector evaluates expr against env, returning false on any syntax or
// evaluation error: an unroutable expression must fail closed (the route is
// simply not taken) rather than sending everything everywhere.
func MatchSelector(expr string, env core.Envelope) bool {
	ok, err := evalSelector(expr, env)
	if err != nil {
		return false
	}
	return ok
}

// CheckSelector parses expr once, surfacing syntax errors so the config
// loader can reject bad routes at startup instead of silently never matching.
func CheckSelector(expr string) error {
	_, err := parseSelector(expr)
	return err
}

func evalSelector(expr string, env core.Envelope) (bool, error) {
	node, err := parseSelector(expr)
	if err != nil {
		return false, err
	}
	return node.eval(env)
}

// ---- AST ---------------------------------------------------------------------

type selNode interface {
	eval(env core.Envelope) (bool, error)
}

type orNode struct{ l, r selNode }

func (n orNode) eval(env core.Envelope) (bool, error) {
	l, err := n.l.eval(env)
	if err != nil || l {
		return l, err
	}
	return n.r.eval(env)
}

type andNode struct{ l, r selNode }

func (n andNode) eval(env core.Envelope) (bool, error) {
	l, err := n.l.eval(env)
	if err != nil || !l {
		return l, err
	}
	return n.r.eval(env)
}

type notNode struct{ inner selNode }

func (n notNode) eval(env core.Envelope) (bool, error) {
	v, err := n.inner.eval(env)
	return !v, err
}

type predNode struct{ name, arg string } // has(...) / present(...)

func (n predNode) eval(env core.Envelope) (bool, error) {
	v, ok := resolveOperand(n.arg, env)
	if !ok {
		return false, nil
	}
	return v != "", nil
}

type cmpNode struct {
	left  string // field reference or literal
	op    string
	right string
}

func (n cmpNode) eval(env core.Envelope) (bool, error) {
	lv, lok := resolveOperand(n.left, env)
	rv, rok := resolveOperand(n.right, env)
	if !lok {
		return false, nil
	}
	if n.op == "" {
		// A bare field reference is a truthiness test.
		return lv != "", nil
	}
	if !rok {
		return false, nil
	}
	switch n.op {
	case "==":
		return lv == rv, nil
	case "!=":
		return lv != rv, nil
	case "contains":
		return strings.Contains(lv, rv), nil
	case "=~", "!~":
		re, err := regexp.Compile(rv)
		if err != nil {
			return false, fmt.Errorf("bad regexp %q: %w", rv, err)
		}
		if n.op == "=~" {
			return re.MatchString(lv), nil
		}
		return !re.MatchString(lv), nil
	}
	return false, fmt.Errorf("unknown operator %q", n.op)
}

// resolveOperand turns an operand token into a string value. Quoted tokens
// are literals; everything else is a field reference into the envelope.
func resolveOperand(tok string, env core.Envelope) (string, bool) {
	if len(tok) >= 2 {
		if (tok[0] == '\'' && tok[len(tok)-1] == '\'') || (tok[0] == '"' && tok[len(tok)-1] == '"') {
			return tok[1 : len(tok)-1], true
		}
	}
	return resolveField(tok, env)
}

// resolveField reads a dotted path from the envelope. These are the exact
// names routes and LLM `when` conditions use:
//
//	from, to, cc, subject, account, source, preview, date
//	fields.<key>        — processor-written fields
//	headers.<header>    — case-insensitive mail headers
//	attachments         — count as decimal string
//	has_attachment      — "true"/"false"
func resolveField(path string, env core.Envelope) (string, bool) {
	m := env.Message
	switch {
	case path == "from":
		return m.From, true
	case path == "subject":
		return m.Subject, true
	case path == "account":
		return m.Account, true
	case path == "source":
		return m.Source, true
	case path == "preview":
		return m.Preview, true
	case path == "to":
		return strings.Join(m.To, ","), true
	case path == "cc":
		return strings.Join(m.Cc, ","), true
	case path == "attachments":
		return fmt.Sprintf("%d", len(m.Attachments)), len(m.Attachments) > 0
	case path == "has_attachment":
		if len(m.Attachments) > 0 {
			return "true", true
		}
		return "false", true
	case path == "date":
		if m.Date.IsZero() {
			return "", false
		}
		return m.Date.Local().Format("2006-01-02 15:04:05"), true
	case strings.HasPrefix(path, "fields."):
		key := strings.TrimPrefix(path, "fields.")
		v, ok := env.Fields[key]
		return v, ok
	case strings.HasPrefix(path, "headers."):
		key := strings.ToLower(strings.TrimPrefix(path, "headers."))
		for h, v := range m.Headers {
			if strings.ToLower(h) == key {
				return v, true
			}
		}
		return "", false
	}
	return "", false
}

// ---- parser -------------------------------------------------------------------

type lexer struct {
	input string
	pos   int
}

type token struct {
	kind string // "ident", "string", "op", "lparen", "rparen", "comma", "eof"
	text string
}

func (l *lexer) next() token {
	for l.pos < len(l.input) && (l.input[l.pos] == ' ' || l.input[l.pos] == '\t') {
		l.pos++
	}
	if l.pos >= len(l.input) {
		return token{kind: "eof"}
	}
	c := l.input[l.pos]
	switch {
	case c == '(':
		l.pos++
		return token{kind: "lparen", text: "("}
	case c == ')':
		l.pos++
		return token{kind: "rparen", text: ")"}
	case c == ',':
		l.pos++
		return token{kind: "comma", text: ","}
	case c == '\'' || c == '"':
		quote := c
		start := l.pos
		l.pos++
		for l.pos < len(l.input) && l.input[l.pos] != quote {
			if l.input[l.pos] == '\\' && l.pos+1 < len(l.input) {
				l.pos++
			}
			l.pos++
		}
		if l.pos >= len(l.input) {
			return token{kind: "bad", text: "unterminated string"}
		}
		l.pos++
		return token{kind: "string", text: l.input[start:l.pos]}
	case c == '&' && l.pos+1 < len(l.input) && l.input[l.pos+1] == '&':
		l.pos += 2
		return token{kind: "op", text: "&&"}
	case c == '|' && l.pos+1 < len(l.input) && l.input[l.pos+1] == '|':
		l.pos += 2
		return token{kind: "op", text: "||"}
	case c == '!':
		if l.pos+1 < len(l.input) && (l.input[l.pos+1] == '=' || l.input[l.pos+1] == '~') {
			op := l.input[l.pos : l.pos+2]
			l.pos += 2
			return token{kind: "op", text: op}
		}
		l.pos++
		return token{kind: "op", text: "!"}
	case c == '=' && l.pos+1 < len(l.input) && (l.input[l.pos+1] == '=' || l.input[l.pos+1] == '~'):
		op := l.input[l.pos : l.pos+2]
		l.pos += 2
		return token{kind: "op", text: op}
	}
	// Identifier / unquoted operand: letters, digits, dots, underscores,
	// hyphens. Anything richer must be a quoted literal.
	start := l.pos
	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if c == ' ' || c == '\t' || c == '(' || c == ')' || c == ',' ||
			c == '&' || c == '|' || c == '!' || c == '=' {
			break
		}
		l.pos++
	}
	if l.pos == start {
		l.pos++
		return token{kind: "bad", text: fmt.Sprintf("unexpected character %q", c)}
	}
	return token{kind: "ident", text: l.input[start:l.pos]}
}

type parser struct {
	lex  *lexer
	peek token
	err  error
}

func parseSelector(input string) (selNode, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty expression")
	}
	p := &parser{lex: &lexer{input: input}}
	p.advance()
	node := p.parseOr()
	if p.err != nil {
		return nil, p.err
	}
	if p.peek.kind != "eof" {
		return nil, fmt.Errorf("trailing input %q", p.peek.text)
	}
	return node, nil
}

func (p *parser) advance() {
	p.peek = p.lex.next()
	if p.peek.kind == "bad" && p.err == nil {
		p.err = fmt.Errorf("%s", p.peek.text)
	}
}

func (p *parser) parseOr() selNode {
	l := p.parseAnd()
	for p.err == nil && p.peek.kind == "op" && p.peek.text == "||" {
		p.advance()
		r := p.parseAnd()
		l = orNode{l: l, r: r}
	}
	return l
}

func (p *parser) parseAnd() selNode {
	l := p.parseUnary()
	for p.err == nil && p.peek.kind == "op" && p.peek.text == "&&" {
		p.advance()
		r := p.parseUnary()
		l = andNode{l: l, r: r}
	}
	return l
}

func (p *parser) parseUnary() selNode {
	if p.err != nil {
		return nil
	}
	if p.peek.kind == "op" && p.peek.text == "!" {
		p.advance()
		return notNode{inner: p.parseUnary()}
	}
	if p.peek.kind == "lparen" {
		p.advance()
		node := p.parseOr()
		if p.err == nil {
			if p.peek.kind != "rparen" {
				p.err = fmt.Errorf("missing closing paren")
				return nil
			}
			p.advance()
		}
		return node
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() selNode {
	if p.err != nil {
		return nil
	}
	tok := p.peek
	if tok.kind != "ident" && tok.kind != "string" {
		p.err = fmt.Errorf("expected field reference, got %q", tok.text)
		return nil
	}
	p.advance()

	// Predicate call: has(x) / present(x)
	if p.peek.kind == "lparen" && (tok.text == "has" || tok.text == "present") {
		p.advance()
		arg := p.peek
		if arg.kind != "ident" && arg.kind != "string" {
			p.err = fmt.Errorf("%s() expects a field reference", tok.text)
			return nil
		}
		p.advance()
		if p.peek.kind != "rparen" {
			p.err = fmt.Errorf("missing closing paren in %s()", tok.text)
			return nil
		}
		p.advance()
		return predNode{name: tok.text, arg: arg.text}
	}

	// `contains` is a word operator and therefore lexes as ident, unlike the
	// symbolic operators; accept either kind as long as the text matches one
	// of the five comparison operators. A peek that is none of them means the
	// reference is bare (truthiness) or garbage the next parse level rejects.
	text := p.peek.text
	if text != "==" && text != "!=" && text != "=~" && text != "!~" && text != "contains" {
		// Bare field reference: truthiness.
		return cmpNode{left: tok.text}
	}
	if text == "contains" && p.peek.kind != "ident" {
		p.err = fmt.Errorf("malformed contains operator")
		return nil
	}
	if text != "contains" && p.peek.kind != "op" {
		p.err = fmt.Errorf("malformed operator %q", text)
		return nil
	}
	op := p.peek.text
	p.advance()
	right := p.peek
	if right.kind != "ident" && right.kind != "string" {
		p.err = fmt.Errorf("expected value after %s", op)
		return nil
	}
	p.advance()
	return cmpNode{left: tok.text, op: op, right: right.text}
}
