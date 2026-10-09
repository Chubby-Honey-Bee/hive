package workflow

import (
	"slices"
	"strings"
	"testing"
)

func TestSafeEval_Bool(t *testing.T) {
	cases := []struct {
		expr  string
		state map[string]any
		want  bool
	}{
		{"true", nil, true},
		{"false", nil, false},
		{"True", nil, true},
		{"False", nil, false},
		{"!true", nil, false},
		{"!false", nil, true},
		{"true && true", nil, true},
		{"true && false", nil, false},
		{"true || false", nil, true},
		{"false || false", nil, false},
		{"true and false", nil, false},
		{"true or false", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := SafeEval(tc.expr, tc.state)
			if err != nil {
				t.Fatalf("SafeEval(%q): %v", tc.expr, err)
			}
			if toBool(got) != tc.want {
				t.Errorf("SafeEval(%q) = %v; want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestSafeEval_NumericComparisons(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"5 > 3", true},
		{"5 < 3", false},
		{"5 == 5", true},
		{"5 == 6", false},
		{"5 != 6", true},
		{"5 >= 5", true},
		{"5 <= 4", false},
		{"-3 < 0", true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := SafeEval(tc.expr, nil)
			if err != nil {
				t.Fatalf("SafeEval: %v", err)
			}
			if toBool(got) != tc.want {
				t.Errorf("SafeEval(%q) = %v; want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestSafeEval_StateAccess(t *testing.T) {
	state := map[string]any{
		"count":      int64(5),
		"compile_ok": true,
		"name":       "fix-1",
	}
	cases := []struct {
		expr string
		want bool
	}{
		{"count >= 3", true},
		{"count == 5", true},
		{"compile_ok", true},
		{"compile_ok == true", true},
		{`name == "fix-1"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := SafeEval(tc.expr, state)
			if err != nil {
				t.Fatalf("SafeEval: %v", err)
			}
			if toBool(got) != tc.want {
				t.Errorf("SafeEval(%q) = %v; want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestSafeEval_OutputsSelector(t *testing.T) {
	state := map[string]any{
		"outputs": map[string]any{
			"compile_ok": true,
			"tests_pass": false,
		},
	}
	got, err := SafeEval("outputs.compile_ok && outputs.tests_pass", state)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if toBool(got) {
		t.Errorf("expected false (tests_pass = false)")
	}

	got, err = SafeEval("outputs.compile_ok", state)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if !toBool(got) {
		t.Error("expected true")
	}
}

func TestSafeEval_LenBuiltin(t *testing.T) {
	state := map[string]any{
		"items": []any{"a", "b", "c"},
	}
	got, err := SafeEval("len(items) == 3", state)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if !toBool(got) {
		t.Error("expected len(items) == 3 to be true")
	}
}

func TestSafeEval_UnknownVariable(t *testing.T) {
	_, err := SafeEval("ghost == true", nil)
	if err == nil {
		t.Fatal("expected error for unknown variable")
	}
	if !strings.Contains(err.Error(), "ghost") && !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error = %v; expected mention of variable name", err)
	}
}

func TestSafeEval_SyntaxError(t *testing.T) {
	_, err := SafeEval("compile_ok ===", nil)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestEvaluateAccept_NoPredicates(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"fix-1": map[string]any{"type": "agent"},
		},
	}
	got := evaluateAccept(defn, "fix-1", nil, map[string]any{})
	if got != nil {
		t.Errorf("expected nil for no predicates; got %v", got)
	}
}

func TestEvaluateAccept_AllPass(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"fix-1": map[string]any{
				"accept": []any{
					"outputs.compile_ok == true",
					"outputs.tests_pass == true",
				},
			},
		},
	}
	outputs := map[string]any{"compile_ok": true, "tests_pass": true}
	got := evaluateAccept(defn, "fix-1", map[string]any{}, outputs)
	if got != nil {
		t.Errorf("expected nil rejection; got %+v", got)
	}
}

func TestEvaluateAccept_Rejects(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"fix-1": map[string]any{
				"accept": []any{
					"outputs.compile_ok == true",
					"outputs.tests_pass == true",
				},
			},
		},
	}
	outputs := map[string]any{"compile_ok": true, "tests_pass": false}
	got := evaluateAccept(defn, "fix-1", map[string]any{}, outputs)
	if got == nil {
		t.Fatal("expected rejection")
	}
	if got.NodeName != "fix-1" {
		t.Errorf("NodeName = %q; want fix-1", got.NodeName)
	}
}

func TestEvaluateAccept_EvalError(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"fix-1": map[string]any{
				"accept": []any{"outputs.does_not_exist == true"},
			},
		},
	}
	got := evaluateAccept(defn, "fix-1", map[string]any{}, map[string]any{})
	if got == nil {
		t.Fatal("expected rejection on missing variable")
	}
	if got.EvalError == "" {
		t.Errorf("EvalError empty; want non-empty for unknown variable")
	}
}

// An accept: item is a predicate string or a {predicate, reason} map, read
// in order; an empty predicate is skipped.
func TestAcceptItems_HappyPath(t *testing.T) {
	list := []any{"a == b", map[string]any{"predicate": "c >= 3", "reason": "c is {c}"}, "", map[string]any{"reason": "no predicate"}}
	var want []AcceptItem
	for _, x := range list {
		switch v := x.(type) {
		case string:
			want = append(want, AcceptItem{Predicate: v})
		case map[string]any:
			p, _ := v["predicate"].(string)
			r, _ := v["reason"].(string)
			want = append(want, AcceptItem{Predicate: p, Reason: r})
		}
	}
	want = slices.DeleteFunc(want, func(it AcceptItem) bool { return it.Predicate == "" })
	if got := AcceptItems(map[string]any{"accept": list}); !slices.Equal(got, want) {
		t.Errorf("AcceptItems = %+v; want %+v", got, want)
	}
}

func TestAcceptItems_NoNode(t *testing.T) {
	if got := AcceptItems(nil); len(got) != 0 {
		t.Errorf("expected none; got %v", got)
	}
}

func TestSafeEval_NumericLiterals(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"3 < 5", true},
		{"3.5 > 2.5", true},
		{"-1 < 0", true},
		{"1 + 1 != 2", false}, // wait — '+' is not supported, would error
	}
	// drop the '+' case since arithmetic isn't part of the safe-eval subset
	for _, tc := range cases[:3] {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := SafeEval(tc.expr, nil)
			if err != nil {
				t.Fatalf("SafeEval(%q): %v", tc.expr, err)
			}
			if toBool(got) != tc.want {
				t.Errorf("SafeEval(%q) = %v; want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestSafeEval_StringLiteralLength(t *testing.T) {
	got, err := SafeEval(`len("hello") == 5`, nil)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if !toBool(got) {
		t.Error("expected len(\"hello\") == 5")
	}
}

func TestSafeEval_ParenExpr(t *testing.T) {
	state := map[string]any{"a": true, "b": false}
	got, err := SafeEval("(a || b) && a", state)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if !toBool(got) {
		t.Error("expected (true || false) && true == true")
	}
}

func TestSafeEval_NegatedNumeric(t *testing.T) {
	got, err := SafeEval("-5 < -3", nil)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if !toBool(got) {
		t.Error("expected -5 < -3 == true")
	}
}

func TestSafeEval_LenOnDifferentTypes(t *testing.T) {
	state := map[string]any{
		"s":     "abc",
		"slice": []any{1, 2},
		"m":     map[string]any{"a": 1, "b": 2, "c": 3},
		"n":     nil,
	}
	cases := []struct {
		expr string
		want int64
	}{
		{"len(s)", 3},
		{"len(slice)", 2},
		{"len(m)", 3},
		{"len(n)", 0},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := SafeEval(tc.expr, state)
			if err != nil {
				t.Fatalf("SafeEval(%q): %v", tc.expr, err)
			}
			n, ok := got.(int64)
			if !ok {
				t.Fatalf("got = %T(%v); want int64", got, got)
			}
			if n != tc.want {
				t.Errorf("len = %d; want %d", n, tc.want)
			}
		})
	}
}

func TestSafeEval_LenOnUnsupportedType(t *testing.T) {
	state := map[string]any{"x": 42}
	_, err := SafeEval("len(x)", state)
	if err == nil {
		t.Fatal("expected error for len(int)")
	}
}

func TestSafeEval_OnlyLenSupported(t *testing.T) {
	_, err := SafeEval(`upper("abc")`, nil)
	if err == nil {
		t.Fatal("expected error for unsupported builtin")
	}
	if !strings.Contains(err.Error(), "only len()") && !strings.Contains(err.Error(), "len") {
		t.Errorf("error = %v; expected mention of len()", err)
	}
}

func TestSafeEval_LenWrongArity(t *testing.T) {
	_, err := SafeEval(`len()`, nil)
	if err == nil {
		t.Fatal("expected error for len() with 0 args")
	}
	_, err = SafeEval(`len("a", "b")`, nil)
	if err == nil {
		t.Fatal("expected error for len() with 2 args")
	}
}

func TestSafeEval_SelectorOnNonMap(t *testing.T) {
	state := map[string]any{"x": 5} // x is not a map
	_, err := SafeEval("x.y", state)
	if err == nil {
		t.Fatal("expected error for selector on non-map")
	}
}

func TestSafeEval_SelectorMissingField(t *testing.T) {
	state := map[string]any{"outputs": map[string]any{"a": 1}}
	_, err := SafeEval("outputs.missing", state)
	if err == nil {
		t.Fatal("expected error for missing field")
	}
}

func TestSafeEval_StringMapSelector(t *testing.T) {
	state := map[string]any{"m": map[string]string{"k": "v"}}
	got, err := SafeEval(`m.k == "v"`, state)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if !toBool(got) {
		t.Errorf("expected map[string]string field access to work")
	}
}

func TestSafeEval_NilLiteral(t *testing.T) {
	state := map[string]any{"x": nil}
	got, err := SafeEval("x == nil", state)
	if err != nil {
		t.Fatalf("SafeEval: %v", err)
	}
	if !toBool(got) {
		t.Error("expected x == nil to be true")
	}
}

func TestSafeEval_UnsupportedBinaryOp(t *testing.T) {
	// Bitwise-and isn't in our subset.
	_, err := SafeEval("1 & 2", nil)
	if err == nil {
		t.Fatal("expected error for unsupported binary op")
	}
}

func TestSafeEval_UnsupportedUnaryOp(t *testing.T) {
	// Bitwise-complement isn't in our subset.
	_, err := SafeEval("^1", nil)
	if err == nil {
		t.Fatal("expected error for unsupported unary op")
	}
}
