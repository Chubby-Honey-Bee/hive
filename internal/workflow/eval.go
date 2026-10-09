package workflow

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// SafeEval evaluates a simple boolean expression against a state dict.
// Supports: comparisons (>=, <=, ==, !=, >, <), boolean ops (and/&&,
// or/||, not/!), string/number literals, and variable names from state.
//
// Workflow conditions are YAML strings that use Python/SQL-style
// single-quoted string literals (e.g. `eval_verdict == 'COMPLETE'`) and
// word-form boolean operators (`and`, `or`, `not`). Go's parser rejects
// both: single-quoted multi-char tokens are malformed rune literals, and
// `and`/`or`/`not` are treated as identifiers. We normalize the surface
// syntax to Go's equivalents before parsing. Every shipped workflow in
// workflows/ relies on this.
func SafeEval(expr string, state map[string]any) (any, error) {
	tree, err := parser.ParseExpr(normalizeExpression(expr))
	if err != nil {
		return nil, fmt.Errorf("invalid expression: %q — %w", expr, err)
	}
	return evalNode(tree, state)
}

// normalizeExpression rewrites 'single-quoted' string literals to
// "double-quoted" form and replaces word-form boolean operators
// (`and`/`or`/`not`) with their Go equivalents (`&&`/`||`/`!`) so
// go/parser.ParseExpr accepts the result. The scan is quote-aware:
// content inside an existing double-quoted literal is left untouched
// (so `msg == "x and y"` stays literal). Word operators are matched
// only at identifier boundaries (so `land`, `nordic`, `not_done` are
// not touched). Unterminated single-quoted literals pass through
// unchanged so the parser surfaces a clear error keyed to the
// original input.
func normalizeExpression(expr string) string {
	n := &normalizer{src: expr}
	n.out.Grow(len(expr))
	for n.i < len(n.src) {
		n.step()
	}
	return n.out.String()
}

// normalizer is normalizeExpression's scan: the source, the position
// reached, whether that position is inside a double-quoted literal, and the
// rewritten text so far.
type normalizer struct {
	src      string
	i        int
	inDouble bool
	out      strings.Builder
}

// step rewrites the token at the scan's position. An escaped byte pair and
// anything inside a double-quoted literal are kept as written.
func (n *normalizer) step() {
	switch {
	case n.atEscape():
		n.keep(2)
	case n.src[n.i] == '"':
		n.inDouble = !n.inDouble
		n.keep(1)
	case n.inDouble:
		n.keep(1)
	default:
		n.plain()
	}
}

// plain rewrites a token outside any double-quoted literal: a single-quoted
// literal, a word that may be an operator, or any other byte as it is.
func (n *normalizer) plain() {
	switch {
	case n.src[n.i] == '\'':
		n.singleQuoted()
	case n.atWordStart():
		n.word()
	default:
		n.keep(1)
	}
}

// atEscape reports whether the scan is at a backslash with a byte after it.
func (n *normalizer) atEscape() bool {
	return n.src[n.i] == '\\' && n.i+1 < len(n.src)
}

// atWordStart reports whether the scan is at the first byte of an
// identifier-like run.
func (n *normalizer) atWordStart() bool {
	return isIdentStart(n.src[n.i]) && atWordBoundary(n.src, n.i)
}

// keep copies the next k bytes as they are.
func (n *normalizer) keep(k int) {
	n.out.WriteString(n.src[n.i : n.i+k])
	n.i += k
}

// singleQuoted rewrites a 'single-quoted' literal in double quotes. One
// with no closing quote ends the scan, the rest of the source kept as
// written.
func (n *normalizer) singleQuoted() {
	end := strings.IndexByte(n.src[n.i+1:], '\'')
	if end < 0 {
		n.keep(len(n.src) - n.i)
		return
	}
	end += n.i + 1
	n.out.WriteByte('"')
	n.out.WriteString(n.src[n.i+1 : end])
	n.out.WriteByte('"')
	n.i = end + 1
}

// wordOperators are the word-form boolean operators and their Go forms.
var wordOperators = map[string]string{"and": "&&", "or": "||", "not": "!"}

// word consumes an identifier-like run and writes it, or the Go operator it
// names.
func (n *normalizer) word() {
	end := n.i + 1
	for end < len(n.src) && isIdentCont(n.src[end]) {
		end++
	}
	w := n.src[n.i:end]
	if op, ok := wordOperators[w]; ok {
		w = op
	}
	n.out.WriteString(w)
	n.i = end
}

func isIdentStart(c byte) bool {
	return isASCIILetter(c) || c == '_'
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// atWordBoundary reports whether position i in expr begins a new
// identifier run (i.e., the previous non-backslash byte is not part
// of an identifier). Prevents `not_done` from being rewritten as
// `!_done`.
func atWordBoundary(expr string, i int) bool {
	if i == 0 {
		return true
	}
	prev := expr[i-1]
	return !isIdentCont(prev)
}

// evalNode evaluates one expression node: a flat dispatch on the node's
// kind, each kind evaluated by its own function.
func evalNode(node ast.Expr, state map[string]any) (any, error) {
	switch n := node.(type) {
	case *ast.BasicLit:
		return evalLiteral(n), nil
	case *ast.Ident:
		return evalIdent(n, state)
	case *ast.UnaryExpr:
		return evalUnary(n, state)
	case *ast.BinaryExpr:
		return evalBinary(n, state)
	case *ast.ParenExpr:
		return evalNode(n.X, state)
	case *ast.SelectorExpr:
		return evalSelector(n, state)
	case *ast.CallExpr:
		return evalCall(n, state)
	}
	return nil, fmt.Errorf("unsupported expression node: %T", node)
}

// evalLiteral is a literal's value: an int64, a float64, or a string
// without its quotes.
func evalLiteral(n *ast.BasicLit) any {
	switch n.Kind {
	case token.INT:
		var v int64
		fmt.Sscanf(n.Value, "%d", &v)
		return v
	case token.FLOAT:
		var v float64
		fmt.Sscanf(n.Value, "%f", &v)
		return v
	case token.STRING:
		return strings.Trim(n.Value, `"`)
	}
	return n.Value
}

// identLiterals are the names that are values, not state variables.
var identLiterals = map[string]any{
	"true": true, "True": true,
	"false": false, "False": false,
	"nil": nil, "null": nil, "None": nil,
}

// evalIdent is a literal name's value, or the state variable a name names.
func evalIdent(n *ast.Ident, state map[string]any) (any, error) {
	if v, ok := identLiterals[n.Name]; ok {
		return v, nil
	}
	v, ok := state[n.Name]
	if !ok {
		return nil, fmt.Errorf("unknown variable: %q", n.Name)
	}
	return v, nil
}

// evalUnary evaluates `!x` and `-x`.
func evalUnary(n *ast.UnaryExpr, state map[string]any) (any, error) {
	val, err := evalNode(n.X, state)
	if err != nil {
		return nil, err
	}
	switch n.Op {
	case token.NOT:
		return !toBool(val), nil
	case token.SUB:
		return negate(val)
	}
	return nil, fmt.Errorf("unsupported unary op: %v", n.Op)
}

// negate is -v for a numeric operand.
func negate(v any) (any, error) {
	f, err := toFloatE(v)
	if err != nil {
		return nil, fmt.Errorf("operand of unary -: %w", err)
	}
	return -f, nil
}

// evalBinary evaluates a binary expression. The boolean operators
// short-circuit: they don't evaluate (or require) the right operand unless it
// can change the result. This matches standard boolean semantics and means
// `false && x` / `true || x` do not error when x references an unbound
// variable. Every other operator needs both operands.
func evalBinary(n *ast.BinaryExpr, state map[string]any) (any, error) {
	left, err := evalNode(n.X, state)
	if err != nil {
		return nil, err
	}
	if isLogical(n.Op) {
		return evalLogical(n.Op, left, n.Y, state)
	}
	right, err := evalNode(n.Y, state)
	if err != nil {
		return nil, err
	}
	return compare(n.Op, left, right)
}

// isLogical reports whether op is && or ||.
func isLogical(op token.Token) bool {
	return op == token.LAND || op == token.LOR
}

// evalLogical evaluates && or || given the left operand's value: false for
// && and true for || decide the result alone; otherwise the result is the
// right operand's truth.
func evalLogical(op token.Token, left any, right ast.Expr, state map[string]any) (any, error) {
	l := toBool(left)
	if l == (op == token.LOR) {
		return l, nil
	}
	r, err := evalNode(right, state)
	if err != nil {
		return nil, err
	}
	return toBool(r), nil
}

// compare applies a comparison operator. `==` and `!=` compare the operands'
// %v renderings; the ordering operators need numbers (order).
func compare(op token.Token, left, right any) (any, error) {
	switch op {
	case token.EQL:
		return fmt.Sprintf("%v", left) == fmt.Sprintf("%v", right), nil
	case token.NEQ:
		return fmt.Sprintf("%v", left) != fmt.Sprintf("%v", right), nil
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return order(op, left, right)
	}
	return nil, fmt.Errorf("unsupported binary op: %v", op)
}

// orderings are the ordering operators over numbers.
var orderings = map[token.Token]func(a, b float64) bool{
	token.LSS: func(a, b float64) bool { return a < b },
	token.LEQ: func(a, b float64) bool { return a <= b },
	token.GTR: func(a, b float64) bool { return a > b },
	token.GEQ: func(a, b float64) bool { return a >= b },
}

// order applies an ordering operator. An operand that is not a number has no
// position on the number line, so the comparison is an error: `score > 5`
// with score="high" takes no branch rather than recording a comparison that
// did not happen.
func order(op token.Token, left, right any) (any, error) {
	lf, err := toFloatE(left)
	if err != nil {
		return nil, fmt.Errorf("left operand of %v: %w", op, err)
	}
	rf, err := toFloatE(right)
	if err != nil {
		return nil, fmt.Errorf("right operand of %v: %w", op, err)
	}
	return orderings[op](lf, rf), nil
}

// evalSelector evaluates `outputs.count` and similar member access. We
// resolve the LHS to a value, then index it by the literal RHS name. Maps
// and structured JSON deserialize as map[string]any in our state, so this is
// essentially `m[key]`.
func evalSelector(n *ast.SelectorExpr, state map[string]any) (any, error) {
	base, err := evalNode(n.X, state)
	if err != nil {
		return nil, err
	}
	v, isMap, found := memberOf(base, n.Sel.Name)
	if !isMap {
		return nil, fmt.Errorf("cannot index %s by %q (not a map)", exprText(n.X), n.Sel.Name)
	}
	if !found {
		return nil, fmt.Errorf("unknown field %q on %s", n.Sel.Name, exprText(n.X))
	}
	return v, nil
}

// memberOf looks name up in base: isMap reports whether base is a map that
// can be indexed, found whether it holds name.
func memberOf(base any, name string) (v any, isMap, found bool) {
	switch m := base.(type) {
	case map[string]any:
		v, found = m[name]
		return v, true, found
	case map[string]string:
		s, ok := m[name]
		return s, true, ok
	}
	return nil, false, false
}

// evalCall evaluates a call. Currently only `len(x)` is supported. Adding
// more builtins (lower, contains, ...) should be deliberate — accept:
// predicates are user-authored, so the surface stays small on purpose.
func evalCall(n *ast.CallExpr, state map[string]any) (any, error) {
	arg, err := lenArgument(n)
	if err != nil {
		return nil, err
	}
	v, err := evalNode(arg, state)
	if err != nil {
		return nil, err
	}
	return lengthOf(v)
}

// lenArgument is the one argument of a len() call, or why the call is not
// one.
func lenArgument(n *ast.CallExpr) (ast.Expr, error) {
	if ident, ok := n.Fun.(*ast.Ident); !ok || ident.Name != "len" {
		return nil, fmt.Errorf("only len() is supported in expressions; got %s(...)", exprText(n.Fun))
	}
	if len(n.Args) != 1 {
		return nil, fmt.Errorf("len() takes exactly one argument, got %d", len(n.Args))
	}
	return n.Args[0], nil
}

// lengthOf is len(v) for a string, a list, a map or an absent value.
func lengthOf(v any) (any, error) {
	switch x := v.(type) {
	case string:
		return int64(len(x)), nil
	case []any:
		return int64(len(x)), nil
	case map[string]any:
		return int64(len(x)), nil
	case nil:
		return int64(0), nil
	}
	return nil, fmt.Errorf("len() not supported on %T", v)
}

// exprText is a tiny stringifier for expression nodes used only in
// error messages. We avoid pulling in go/printer for one-line errors.
func exprText(n ast.Expr) string {
	switch e := n.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprText(e.X) + "." + e.Sel.Name
	case *ast.BasicLit:
		return e.Value
	}
	return fmt.Sprintf("%T", n)
}

// falseStrings are the strings that are false.
var falseStrings = map[string]bool{"": true, "false": true, "False": true}

// toBool is a value's truth: a bool as it is, a number when it is not 0, a
// string unless it is empty or false, and anything else when present.
func toBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case int64, float64:
		return toFloat(b) != 0
	case string:
		return !falseStrings[b]
	}
	return v != nil
}

// toFloatE converts an expression operand to a number, or says why it
// cannot. A numeric string is accepted — workflow state arrives from YAML
// and JSON, where "5" and 5 are both common — but a non-numeric one is an
// error rather than a silent 0.
func toFloatE(v any) (float64, error) {
	switch n := v.(type) {
	case int64, float64, int:
		return toFloat(n), nil
	case string:
		return parseNumber(n)
	case nil:
		return 0, errors.New("value is absent")
	}
	return 0, fmt.Errorf("%T is not a number", v)
}

// parseNumber reads a numeric string, spaces around it allowed.
func parseNumber(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return f, nil
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}
