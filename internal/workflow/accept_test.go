package workflow

import (
	"fmt"
	"go/ast"
	"go/parser"
	"strings"
	"testing"
)

func TestAcceptRejection_Error_WithEvalError(t *testing.T) {
	r := &AcceptRejection{
		NodeName:  "fix-1",
		Predicate: "outputs.broken == ?",
		EvalError: "syntax error",
	}
	got := r.Error()
	for _, want := range []string{"fix-1", "outputs.broken", "syntax error", "failed to evaluate"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q; expected substring %q", got, want)
		}
	}
}

func TestAcceptRejection_Error_WithEvaluatedValue(t *testing.T) {
	r := &AcceptRejection{
		NodeName:       "fix-2",
		Predicate:      "outputs.compile_ok == true",
		EvaluatedValue: "false",
	}
	got := r.Error()
	for _, want := range []string{"fix-2", "outputs.compile_ok", "evaluated to", "false"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q; expected substring %q", got, want)
		}
	}
}

// A false predicate whose item gives a reason rejects with the reason, its
// {outputs.<key>} placeholders filled from the outputs and the rest from
// state, one no value fills left as written, and Error() leads with it. A
// predicate given as a string rejects with no reason.
func TestEvaluateAccept_Reason(t *testing.T) {
	state := map[string]any{"ready": true, "limit": 5}
	outputs := map[string]any{"n": 3, "verdict": "oppose"}
	reason := "n was {outputs.n} under {limit}; {outputs.verdict} stays, {missing} too"
	defn := func(first any) map[string]any {
		return map[string]any{"nodes": map[string]any{"fix-1": map[string]any{"accept": []any{
			first, map[string]any{"predicate": "outputs.n >= limit", "reason": reason},
		}}}}
	}

	rej := evaluateAccept(defn("ready"), "fix-1", state, outputs)
	if rej == nil {
		t.Fatal("outputs.n below limit passed")
	}
	want := fmt.Sprintf("n was %v under %v; %v stays, {missing} too", outputs["n"], state["limit"], outputs["verdict"])
	if rej.Reason != want {
		t.Errorf("Reason = %q; want %q", rej.Reason, want)
	}
	if got := rej.Error(); !strings.HasPrefix(got, want+" (") || !strings.Contains(got, rej.Predicate) {
		t.Errorf("Error() = %q; want the reason, then the predicate", got)
	}

	plain := evaluateAccept(defn("ready == false"), "fix-1", state, outputs)
	if plain == nil {
		t.Fatal("ready == false passed")
	}
	if plain.Reason != "" {
		t.Errorf("a string predicate gave the reason %q", plain.Reason)
	}
	if got, want := plain.Error(), fmt.Sprintf("accept: predicate %q on node %q evaluated to %s", plain.Predicate, plain.NodeName, plain.EvaluatedValue); got != want {
		t.Errorf("Error() = %q; want %q", got, want)
	}
}

// accept: is a list of predicate strings or {predicate, reason} maps; an
// item AcceptItems would skip is refused, naming the node and the item.
func TestCheckNodeFields_AcceptItems(t *testing.T) {
	agent := func(accept any) map[string]any {
		return map[string]any{"n": map[string]any{"type": "agent", "accept": accept}}
	}
	cases := []struct {
		name  string
		nodes map[string]any
		want  string // "" when the definition passes
	}{
		{"strings", agent([]any{"a == 1", "b == 2"}), ""},
		{"a predicate with a reason", agent([]any{"a == 1", map[string]any{"predicate": "b == 2", "reason": "b was {b}"}}), ""},
		{"a predicate alone in a map", agent([]any{map[string]any{"predicate": "b == 2"}}), ""},
		{"not a list", agent("a == 1"), `node "n": accept: must be a list`},
		{"a number", agent([]any{"a == 1", 5}), `node "n": accept: item 1 must be a predicate string or a {predicate, reason} map`},
		{"a map with no predicate", agent([]any{map[string]any{"reason": "r"}}), `node "n": accept: item 0 has no predicate string`},
		{"a misspelled key", agent([]any{map[string]any{"predicate": "a == 1", "reasons": "r"}}), `node "n": accept: item 0: unknown key "reasons"`},
		{"a reason that is not a string", agent([]any{map[string]any{"predicate": "a == 1", "reason": []any{"r"}}}), `node "n": accept: item 0: reason must be a string`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckNodeFields(map[string]any{"nodes": c.nodes})
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestNodeOnRejectBlock_Present(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"fix-1": map[string]any{
				"type": "agent",
				"on_reject": map[string]any{
					"model":                 "opus",
					"max_repair_iterations": 1,
				},
			},
		},
	}
	got := NodeOnRejectBlock(defn, "fix-1")
	if got == nil {
		t.Fatal("expected on_reject block, got nil")
	}
	if got["model"] != "opus" {
		t.Errorf("model = %v; want opus", got["model"])
	}
}

func TestNodeOnRejectBlock_Absent(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"fix-1": map[string]any{"type": "agent"},
		},
	}
	if got := NodeOnRejectBlock(defn, "fix-1"); got != nil {
		t.Errorf("expected nil for missing on_reject, got %v", got)
	}
}

func TestNodeOnRejectBlock_NoNodesMap(t *testing.T) {
	if got := NodeOnRejectBlock(map[string]any{}, "x"); got != nil {
		t.Errorf("expected nil when defn has no nodes, got %v", got)
	}
}

func TestNodeOnRejectBlock_NodeMissing(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{}}
	if got := NodeOnRejectBlock(defn, "ghost"); got != nil {
		t.Errorf("expected nil for missing node, got %v", got)
	}
}

func TestNodeOnRejectBlock_MalformedNode(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{"x": "not-a-map"}}
	if got := NodeOnRejectBlock(defn, "x"); got != nil {
		t.Errorf("expected nil for non-map node, got %v", got)
	}
}

func TestExprText_Ident(t *testing.T) {
	e := mustParseExpr(t, "x")
	if got := exprText(e); got != "x" {
		t.Errorf("exprText(ident) = %q; want %q", got, "x")
	}
}

func TestExprText_SelectorExpr(t *testing.T) {
	e := mustParseExpr(t, "outputs.count")
	if got := exprText(e); got != "outputs.count" {
		t.Errorf("exprText(selector) = %q; want %q", got, "outputs.count")
	}
}

func TestExprText_NestedSelectorExpr(t *testing.T) {
	e := mustParseExpr(t, "a.b.c")
	if got := exprText(e); got != "a.b.c" {
		t.Errorf("exprText(nested) = %q; want %q", got, "a.b.c")
	}
}

func TestExprText_BasicLit(t *testing.T) {
	e := mustParseExpr(t, "42")
	if got := exprText(e); got != "42" {
		t.Errorf("exprText(basic lit) = %q; want %q", got, "42")
	}
}

func TestExprText_Fallback(t *testing.T) {
	e := mustParseExpr(t, "1 + 2")
	got := exprText(e)
	if !strings.Contains(got, "BinaryExpr") {
		t.Errorf("exprText(binary) = %q; expected type-name fallback", got)
	}
}

func mustParseExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	expr, err := parser.ParseExpr(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return expr
}
