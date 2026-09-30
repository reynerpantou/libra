// Package formula parses and evaluates the metric formulas businesses
// configure, such as `search_gmv / users` or `search_clicks / search_impressions`.
//
// A formula is plain arithmetic (+ - * /, unary minus, parentheses, numbers)
// over identifiers. Identifiers name measures, other metrics, or the built-in
// `users` (the number of exposed units). The report layer expands metric
// references and then evaluates the formula together with its gradient, which
// is what the delta method needs to put a confidence interval on any formula.
package formula

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Node is a parsed formula expression.
type Node interface{ node() }

type Num struct{ Value float64 }
type Ident struct{ Name string }
type Neg struct{ X Node }
type Bin struct {
	Op   byte // one of + - * /
	L, R Node
}

func (Num) node()   {}
func (Ident) node() {}
func (Neg) node()   {}
func (Bin) node()   {}

// MaxLength caps formula source size; formulas are short by nature.
const MaxLength = 1000

// Parse turns formula source into an expression tree.
func Parse(src string) (Node, error) {
	if len(src) > MaxLength {
		return nil, fmt.Errorf("formula is longer than %d characters", MaxLength)
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	if p.peek().kind == tEOF {
		return nil, errors.New("formula is empty")
	}
	n, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, fmt.Errorf("unexpected %q at position %d", t.text, t.pos+1)
	}
	return n, nil
}

// ValidIdent reports whether s can be used as a measure or metric key.
func ValidIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z'):
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// ---- lexer ----

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tIdent
	tOp
	tLParen
	tRParen
)

type token struct {
	kind tokKind
	text string
	num  float64
	pos  int
}

func lex(src string) ([]token, error) {
	var out []token
	rs := []rune(src)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '+' || r == '-' || r == '*' || r == '/':
			out = append(out, token{kind: tOp, text: string(r), pos: i})
			i++
		case r == '(':
			out = append(out, token{kind: tLParen, text: "(", pos: i})
			i++
		case r == ')':
			out = append(out, token{kind: tRParen, text: ")", pos: i})
			i++
		case (r >= '0' && r <= '9') || r == '.':
			j := i
			for j < len(rs) && ((rs[j] >= '0' && rs[j] <= '9') || rs[j] == '.') {
				j++
			}
			// optional exponent: 1e6, 2.5E-3
			if j < len(rs) && (rs[j] == 'e' || rs[j] == 'E') {
				k := j + 1
				if k < len(rs) && (rs[k] == '+' || rs[k] == '-') {
					k++
				}
				if k < len(rs) && rs[k] >= '0' && rs[k] <= '9' {
					for k < len(rs) && rs[k] >= '0' && rs[k] <= '9' {
						k++
					}
					j = k
				}
			}
			text := string(rs[i:j])
			v, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q at position %d", text, i+1)
			}
			out = append(out, token{kind: tNum, text: text, num: v, pos: i})
			i = j
		case r == '_' || unicode.IsLetter(r):
			j := i
			for j < len(rs) && (rs[j] == '_' || unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j])) {
				j++
			}
			text := string(rs[i:j])
			if !ValidIdent(text) {
				return nil, fmt.Errorf("invalid name %q at position %d (use lowercase letters, digits and _)", text, i+1)
			}
			out = append(out, token{kind: tIdent, text: text, pos: i})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at position %d", r, i+1)
		}
	}
	return append(out, token{kind: tEOF, pos: len(rs)}), nil
}

// ---- parser (precedence climbing) ----

type parser struct {
	toks  []token
	i     int
	depth int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

func prec(op string) int {
	switch op {
	case "+", "-":
		return 1
	case "*", "/":
		return 2
	}
	return 0
}

func (p *parser) expr(minPrec int) (Node, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 200 {
		return nil, errors.New("formula is nested too deeply")
	}
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tOp || prec(t.text) <= minPrec {
			return left, nil
		}
		p.next()
		right, err := p.expr(prec(t.text))
		if err != nil {
			return nil, err
		}
		left = Bin{Op: t.text[0], L: left, R: right}
	}
}

func (p *parser) unary() (Node, error) {
	t := p.peek()
	if t.kind == tOp && t.text == "-" {
		p.next()
		p.depth++
		defer func() { p.depth-- }()
		if p.depth > 200 {
			return nil, errors.New("formula is nested too deeply")
		}
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return Neg{X: x}, nil
	}
	if t.kind == tOp && t.text == "+" {
		p.next()
		return p.unary()
	}
	return p.primary()
}

func (p *parser) primary() (Node, error) {
	t := p.next()
	switch t.kind {
	case tNum:
		return Num{Value: t.num}, nil
	case tIdent:
		if p.peek().kind == tLParen {
			return nil, fmt.Errorf("functions are not supported (%s at position %d)", t.text, t.pos+1)
		}
		return Ident{Name: t.text}, nil
	case tLParen:
		n, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tRParen {
			return nil, fmt.Errorf("missing ) at position %d", c.pos+1)
		}
		return n, nil
	case tEOF:
		return nil, errors.New("formula ends unexpectedly")
	default:
		return nil, fmt.Errorf("unexpected %q at position %d", t.text, t.pos+1)
	}
}

// ---- tree utilities ----

// Idents lists the distinct identifiers in n, in order of first appearance.
func Idents(n Node) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(Node)
	walk = func(n Node) {
		switch x := n.(type) {
		case Ident:
			if !seen[x.Name] {
				seen[x.Name] = true
				out = append(out, x.Name)
			}
		case Neg:
			walk(x.X)
		case Bin:
			walk(x.L)
			walk(x.R)
		}
	}
	walk(n)
	return out
}

// Substitute replaces identifiers for which f returns a node.
func Substitute(n Node, f func(name string) (Node, bool)) Node {
	switch x := n.(type) {
	case Ident:
		if r, ok := f(x.Name); ok {
			return r
		}
		return x
	case Neg:
		return Neg{X: Substitute(x.X, f)}
	case Bin:
		return Bin{Op: x.Op, L: Substitute(x.L, f), R: Substitute(x.R, f)}
	}
	return n
}

// String prints n with the minimal parentheses needed.
func String(n Node) string {
	var b strings.Builder
	write(&b, n, 0)
	return b.String()
}

func write(b *strings.Builder, n Node, parentPrec int) {
	switch x := n.(type) {
	case Num:
		b.WriteString(strconv.FormatFloat(x.Value, 'g', -1, 64))
	case Ident:
		b.WriteString(x.Name)
	case Neg:
		b.WriteString("-")
		write(b, x.X, 3)
	case Bin:
		p := prec(string(x.Op))
		if p < parentPrec {
			b.WriteString("(")
		}
		write(b, x.L, p)
		b.WriteString(" " + string(x.Op) + " ")
		// Right operand of - and / needs parens at equal precedence.
		write(b, x.R, p+1)
		if p < parentPrec {
			b.WriteString(")")
		}
	}
}

// ---- evaluation ----

// Eval evaluates n with identifier values from vars. Unknown identifiers and
// division by zero yield NaN.
func Eval(n Node, vars map[string]float64) float64 {
	switch x := n.(type) {
	case Num:
		return x.Value
	case Ident:
		if v, ok := vars[x.Name]; ok {
			return v
		}
		return math.NaN()
	case Neg:
		return -Eval(x.X, vars)
	case Bin:
		l, r := Eval(x.L, vars), Eval(x.R, vars)
		switch x.Op {
		case '+':
			return l + r
		case '-':
			return l - r
		case '*':
			return l * r
		case '/':
			if r == 0 {
				return math.NaN()
			}
			return l / r
		}
	}
	return math.NaN()
}

// EvalGrad evaluates n at the point x (identifier i has value x[index[i]])
// and returns the value together with the gradient with respect to x
// (forward-mode automatic differentiation).
func EvalGrad(n Node, x []float64, index map[string]int) (float64, []float64) {
	k := len(x)
	var ev func(Node) (float64, []float64)
	ev = func(n Node) (float64, []float64) {
		g := make([]float64, k)
		switch t := n.(type) {
		case Num:
			return t.Value, g
		case Ident:
			i, ok := index[t.Name]
			if !ok {
				return math.NaN(), g
			}
			g[i] = 1
			return x[i], g
		case Neg:
			v, dv := ev(t.X)
			for i := range g {
				g[i] = -dv[i]
			}
			return -v, g
		case Bin:
			a, da := ev(t.L)
			b, db := ev(t.R)
			switch t.Op {
			case '+':
				for i := range g {
					g[i] = da[i] + db[i]
				}
				return a + b, g
			case '-':
				for i := range g {
					g[i] = da[i] - db[i]
				}
				return a - b, g
			case '*':
				for i := range g {
					g[i] = da[i]*b + a*db[i]
				}
				return a * b, g
			case '/':
				if b == 0 {
					for i := range g {
						g[i] = math.NaN()
					}
					return math.NaN(), g
				}
				for i := range g {
					g[i] = (da[i]*b - a*db[i]) / (b * b)
				}
				return a / b, g
			}
		}
		return math.NaN(), g
	}
	return ev(n)
}

// Kind describes how a formula scales with the number of units.
type Kind string

const (
	// KindRatio formulas don't change when every total doubles — rates and
	// per-user values like CTR or GMV/user. Variants compare directly.
	KindRatio Kind = "ratio"
	// KindTotal formulas double when every total doubles — sums like GMV.
	// Variants of different sizes can't compare totals, so the report
	// compares them per exposed unit and shows the totals alongside.
	KindTotal Kind = "total"
	// KindMixed formulas are neither; they're compared per unit with a note.
	KindMixed Kind = "mixed"
)

// Classify works out a formula's Kind numerically: every identifier is a
// total over units, so scaling all of them by 2 reveals the degree.
func Classify(n Node) Kind {
	names := Idents(n)
	if len(names) == 0 {
		return KindRatio
	}
	points := [][]float64{{1.3, 2.7, 0.9, 4.1, 1.7}, {3.1, 0.6, 2.2, 1.1, 5.3}, {0.7, 1.9, 3.3, 2.4, 0.8}}
	var kind Kind
	for _, pt := range points {
		a, b := map[string]float64{}, map[string]float64{}
		for i, name := range names {
			v := pt[i%len(pt)] * float64(i/len(pt)+1)
			a[name], b[name] = v, 2*v
		}
		fa, fb := Eval(n, a), Eval(n, b)
		if math.IsNaN(fa) || math.IsNaN(fb) || math.Abs(fa) < 1e-12 {
			continue
		}
		r := fb / fa
		var k Kind
		switch {
		case math.Abs(r-1) < 1e-9:
			k = KindRatio
		case math.Abs(r-2) < 1e-9:
			k = KindTotal
		default:
			return KindMixed
		}
		if kind != "" && kind != k {
			return KindMixed
		}
		kind = k
	}
	if kind == "" {
		return KindMixed
	}
	return kind
}
