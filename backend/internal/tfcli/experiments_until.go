package tfcli

// The --until condition language of `tf experiments wait`
// (docs/dev/agent-features.md §4):
//
//	expr    := and ( "or" and )*
//	and     := primary ( "and" primary )*
//	primary := "(" expr ")" | cond
//	cond    := ident op value
//	ident   := "step" | "status" | "points"
//	         | ( "metric" | "last" | "min" | "max" ) ":" name
//	op      := "==" | "!=" | ">=" | "<=" | ">" | "<"
//	value   := number | word | quoted
//
// "and" binds tighter than "or"; keywords are case-insensitive. A metric name
// runs up to whitespace, a parenthesis, an operator character or a quote, so
// `metric:val/CER<0.2` works unquoted; a name containing any of those is
// quoted with "..." or '...' (backslash escapes the quote and itself inside
// double quotes): `metric:"val loss" < 0.2`.
//
// Semantics, evaluated against an apitypes.ExpRun:
//   - step is last_step, points is num_points, status the derived status
//     (running / finished / failed / stale; only == and != apply, and the
//     value must be one of those four -- a typo is an error, not a condition
//     that is silently never true);
//   - metric:<m> (alias last:<m>) is the last logged value, min:<m> and
//     max:<m> the smallest and largest value over the run;
//   - a comparison on a metric the run has not logged is false, whatever the
//     operator (including !=): "unknown" never satisfies a wait.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// untilError is a parse error with the byte offset it was found at.
type untilError struct {
	input string
	pos   int // byte offset into input
	msg   string
}

func (e *untilError) Error() string {
	return fmt.Sprintf("%s at column %d", e.msg, e.pos+1)
}

// caret renders the input with a ^ under the offending column, for the
// terminal.
func (e *untilError) caret() string {
	col := len([]rune(e.input[:min(e.pos, len(e.input))]))
	return "  " + e.input + "\n  " + strings.Repeat(" ", col) + "^"
}

// ------------------------------------------------------------------- lexer

type untilTokKind int

const (
	tokEOF untilTokKind = iota
	tokLParen
	tokRParen
	tokOp
	tokWord   // bare word: keyword, identifier, number or bare value
	tokString // quoted string, unquoted
)

type untilTok struct {
	kind untilTokKind
	text string
	pos  int
}

func (t untilTok) describe() string {
	switch t.kind {
	case tokEOF:
		return "end of input"
	case tokString:
		return strconv.Quote(t.text)
	}
	return fmt.Sprintf("%q", t.text)
}

// isWordBreak reports the characters that end a bare word.
func isWordBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '(', ')', '=', '!', '<', '>', '"', '\'':
		return true
	}
	return false
}

func lexUntil(input string) ([]untilTok, error) {
	var toks []untilTok
	i := 0
	for i < len(input) {
		c := input[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			i++
		case '(':
			toks = append(toks, untilTok{tokLParen, "(", i})
			i++
		case ')':
			toks = append(toks, untilTok{tokRParen, ")", i})
			i++
		case '=', '!', '<', '>':
			start := i
			op := string(c)
			if i+1 < len(input) && input[i+1] == '=' {
				op += "="
			}
			i += len(op)
			if op == "=" || op == "!" {
				return nil, &untilError{input, start, fmt.Sprintf("unknown operator %q (use == or !=)", op)}
			}
			toks = append(toks, untilTok{tokOp, op, start})
		case '"', '\'':
			start := i
			quote := c
			i++
			var b strings.Builder
			closed := false
			for i < len(input) {
				ch := input[i]
				if ch == '\\' && quote == '"' && i+1 < len(input) && (input[i+1] == '"' || input[i+1] == '\\') {
					b.WriteByte(input[i+1])
					i += 2
					continue
				}
				if ch == quote {
					closed = true
					i++
					break
				}
				b.WriteByte(ch)
				i++
			}
			if !closed {
				return nil, &untilError{input, start, "unterminated quoted string"}
			}
			toks = append(toks, untilTok{tokString, b.String(), start})
		default:
			start := i
			for i < len(input) && !isWordBreak(input[i]) {
				i++
			}
			toks = append(toks, untilTok{tokWord, input[start:i], start})
		}
	}
	toks = append(toks, untilTok{tokEOF, "", len(input)})
	return toks, nil
}

// ------------------------------------------------------------------ syntax

// untilExpr is a parsed --until condition.
type untilExpr interface {
	eval(run *apitypes.ExpRun) bool
	// mentionsStatus reports whether any comparison looks at status.
	mentionsStatus() bool
	String() string
}

type untilBinary struct {
	and         bool // false = or
	left, right untilExpr
}

func (b *untilBinary) eval(run *apitypes.ExpRun) bool {
	if b.and {
		return b.left.eval(run) && b.right.eval(run)
	}
	return b.left.eval(run) || b.right.eval(run)
}

func (b *untilBinary) mentionsStatus() bool {
	return b.left.mentionsStatus() || b.right.mentionsStatus()
}

func (b *untilBinary) String() string {
	op := "or"
	if b.and {
		op = "and"
	}
	return "(" + b.left.String() + " " + op + " " + b.right.String() + ")"
}

// untilField is what a comparison reads from a run.
type untilField int

const (
	fieldStep untilField = iota
	fieldPoints
	fieldStatus
	fieldLast
	fieldMin
	fieldMax
)

type untilCond struct {
	field  untilField
	metric string // for fieldLast / fieldMin / fieldMax
	op     string
	num    float64 // numeric fields
	str    string  // status
}

var knownRunStatuses = []string{
	string(apitypes.RunStatusRunning), string(apitypes.RunStatusFinished),
	string(apitypes.RunStatusFailed), string(apitypes.RunStatusStale),
}

func (c *untilCond) eval(run *apitypes.ExpRun) bool {
	if c.field == fieldStatus {
		eq := string(run.Status) == c.str
		if c.op == "==" {
			return eq
		}
		return !eq
	}
	v, ok := c.value(run)
	if !ok {
		return false
	}
	switch c.op {
	case "==":
		return v == c.num
	case "!=":
		return v != c.num
	case ">=":
		return v >= c.num
	case "<=":
		return v <= c.num
	case ">":
		return v > c.num
	case "<":
		return v < c.num
	}
	return false
}

// value is the number this comparison reads, false when the run does not
// have it (a metric never logged).
func (c *untilCond) value(run *apitypes.ExpRun) (float64, bool) {
	var m map[string]float64
	switch c.field {
	case fieldStep:
		return float64(run.LastStep), true
	case fieldPoints:
		return float64(run.NumPoints), true
	case fieldLast:
		m = run.Summary
	case fieldMin:
		m = run.SummaryMin
	case fieldMax:
		m = run.SummaryMax
	}
	v, ok := m[c.metric]
	return v, ok
}

func (c *untilCond) mentionsStatus() bool { return c.field == fieldStatus }

func (c *untilCond) String() string {
	var lhs string
	switch c.field {
	case fieldStep:
		lhs = "step"
	case fieldPoints:
		lhs = "points"
	case fieldStatus:
		return "status" + c.op + c.str
	case fieldLast:
		lhs = "metric:" + quoteMetricIfNeeded(c.metric)
	case fieldMin:
		lhs = "min:" + quoteMetricIfNeeded(c.metric)
	case fieldMax:
		lhs = "max:" + quoteMetricIfNeeded(c.metric)
	}
	return lhs + c.op + strconv.FormatFloat(c.num, 'g', -1, 64)
}

func quoteMetricIfNeeded(name string) string {
	if name == "" {
		return `""`
	}
	for i := 0; i < len(name); i++ {
		if isWordBreak(name[i]) || name[i] == '\\' {
			return strconv.Quote(name)
		}
	}
	return name
}

// ------------------------------------------------------------------ parser

type untilParser struct {
	input string
	toks  []untilTok
	i     int
}

// parseUntil parses a --until expression. Errors are *untilError.
func parseUntil(input string) (untilExpr, error) {
	toks, err := lexUntil(input)
	if err != nil {
		return nil, err
	}
	p := &untilParser{input: input, toks: toks}
	if p.peek().kind == tokEOF {
		return nil, p.errAt(p.peek(), "empty condition")
	}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEOF {
		if t.kind == tokRParen {
			return nil, p.errAt(t, "unbalanced \")\"")
		}
		return nil, p.errAt(t, fmt.Sprintf("expected \"and\", \"or\" or end of input, got %s", t.describe()))
	}
	return e, nil
}

func (p *untilParser) peek() untilTok { return p.toks[p.i] }

func (p *untilParser) next() untilTok {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

func (p *untilParser) errAt(t untilTok, msg string) error {
	return &untilError{input: p.input, pos: t.pos, msg: msg}
}

func (p *untilParser) atKeyword(kw string) bool {
	t := p.peek()
	return t.kind == tokWord && strings.EqualFold(t.text, kw)
}

func (p *untilParser) parseOr() (untilExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.atKeyword("or") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &untilBinary{and: false, left: left, right: right}
	}
	return left, nil
}

func (p *untilParser) parseAnd() (untilExpr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.atKeyword("and") {
		p.next()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		left = &untilBinary{and: true, left: left, right: right}
	}
	return left, nil
}

func (p *untilParser) parsePrimary() (untilExpr, error) {
	t := p.peek()
	if t.kind == tokLParen {
		p.next()
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if closing := p.peek(); closing.kind != tokRParen {
			return nil, p.errAt(closing, fmt.Sprintf("expected \")\" to close the \"(\" at column %d, got %s", t.pos+1, closing.describe()))
		}
		p.next()
		return e, nil
	}
	return p.parseCond()
}

func (p *untilParser) parseCond() (untilExpr, error) {
	t := p.next()
	if t.kind != tokWord {
		return nil, p.errAt(t, fmt.Sprintf("expected a condition such as step>=1000 or status!=running, got %s", t.describe()))
	}
	c := &untilCond{}
	prefix, name, hasColon := strings.Cut(t.text, ":")
	switch strings.ToLower(prefix) {
	case "step":
		c.field = fieldStep
	case "points":
		c.field = fieldPoints
	case "status":
		c.field = fieldStatus
	case "metric", "last":
		c.field = fieldLast
	case "min":
		c.field = fieldMin
	case "max":
		c.field = fieldMax
	default:
		return nil, p.errAt(t, fmt.Sprintf("unknown field %q (want step, status, points, metric:<name>, min:<name> or max:<name>)", t.text))
	}
	switch c.field {
	case fieldStep, fieldPoints, fieldStatus:
		if hasColon {
			return nil, p.errAt(t, fmt.Sprintf("%q takes no \":<name>\"", prefix))
		}
	default:
		if !hasColon {
			return nil, p.errAt(t, fmt.Sprintf("%q needs a metric name, as in %s:loss", prefix, strings.ToLower(prefix)))
		}
		if name == "" {
			// metric:"val loss" -- the name is the quoted string that
			// follows. It must follow immediately: `metric: "x"` would
			// read as a missing name followed by a stray string otherwise.
			if s := p.peek(); s.kind == tokString && s.pos == t.pos+len(t.text) {
				p.next()
				name = s.text
			}
		}
		if name == "" {
			return nil, p.errAt(t, fmt.Sprintf("%q needs a metric name, as in %s:loss", t.text, strings.ToLower(prefix)))
		}
		c.metric = name
	}

	op := p.next()
	if op.kind != tokOp {
		return nil, p.errAt(op, fmt.Sprintf("expected a comparison operator (== != >= <= > <) after %q, got %s", t.text, op.describe()))
	}
	c.op = op.text

	val := p.next()
	if val.kind != tokWord && val.kind != tokString {
		return nil, p.errAt(val, fmt.Sprintf("expected a value after %q, got %s", c.op, val.describe()))
	}
	if c.field == fieldStatus {
		if c.op != "==" && c.op != "!=" {
			return nil, p.errAt(op, fmt.Sprintf("status can only be compared with == or !=, not %s", c.op))
		}
		s := strings.ToLower(val.text)
		known := false
		for _, k := range knownRunStatuses {
			if s == k {
				known = true
			}
		}
		if !known {
			return nil, p.errAt(val, fmt.Sprintf("unknown status %q (want %s)", val.text, strings.Join(knownRunStatuses, ", ")))
		}
		c.str = s
		return c, nil
	}
	n, err := strconv.ParseFloat(val.text, 64)
	if err != nil || val.kind == tokString {
		return nil, p.errAt(val, fmt.Sprintf("expected a number after %q, got %s", c.op, val.describe()))
	}
	c.num = n
	return c, nil
}
