package workflow

import "testing"

func TestBuildGraph_DecisionNodeTrueEdgeImplicit(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"d":    map[string]any{"type": "decision", "condition": "x > 0", "true_edge": "ok", "false_edge": "fail"},
			"ok":   map[string]any{"type": "agent"},
			"fail": map[string]any{"type": "agent"},
		},
	}
	incoming, outgoing, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if !incoming["ok"]["d"] {
		t.Error("expected decision-implicit edge d → ok")
	}
	if !incoming["fail"]["d"] {
		t.Error("expected decision-implicit edge d → fail")
	}
	// outgoing[d] should have two edges, one with condition, one negated.
	if len(outgoing["d"]) != 2 {
		t.Errorf("len(outgoing[d]) = %d; want 2", len(outgoing["d"]))
	}
	hasCond, hasNeg := false, false
	for _, e := range outgoing["d"] {
		if e.Condition == "x > 0" {
			hasCond = true
		}
		if e.Condition == "!(x > 0)" {
			hasNeg = true
		}
	}
	if !hasCond {
		t.Error("missing positive-cond edge")
	}
	if !hasNeg {
		t.Error("missing negated-cond edge")
	}
}

func TestBuildGraph_DecisionNodeWithExistingEdgeNoDuplicate(t *testing.T) {
	// If true_edge already has an explicit edge in `edges`, don't duplicate.
	defn := map[string]any{
		"nodes": map[string]any{
			"d":  map[string]any{"type": "decision", "condition": "x", "true_edge": "ok"},
			"ok": map[string]any{"type": "agent"},
		},
		"edges": []any{
			map[string]any{"from": "d", "to": "ok"},
		},
	}
	_, outgoing, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	// Should have exactly 1 edge, not 2.
	if len(outgoing["d"]) != 1 {
		t.Errorf("len = %d; want 1 (no duplicate)", len(outgoing["d"]))
	}
}

func TestBuildGraph_DecisionFalseEdgeWithoutCondition(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"d":    map[string]any{"type": "decision", "false_edge": "fail"},
			"fail": map[string]any{"type": "agent"},
		},
	}
	_, outgoing, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	// false_edge with empty condition → "false" literal.
	if len(outgoing["d"]) != 1 || outgoing["d"][0].Condition != "false" {
		t.Errorf("expected single edge with cond='false'; got %v", outgoing["d"])
	}
}

func TestBuildGraph_DecisionMissingTargetSilent(t *testing.T) {
	// If true_edge points at a non-existent node, the edge is silently
	// skipped (not added). The validator catches this; BuildGraph
	// returns clean.
	defn := map[string]any{
		"nodes": map[string]any{
			"d": map[string]any{"type": "decision", "true_edge": "ghost"},
		},
	}
	_, outgoing, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if len(outgoing["d"]) != 0 {
		t.Errorf("expected no edges for missing target; got %v", outgoing["d"])
	}
}

func TestBuildGraph_NonMapNodeSkipped(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"a":     map[string]any{"type": "agent"},
			"weird": "not-a-map",
		},
	}
	incoming, _, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	// "weird" is iterated for the initial map population, but skipped
	// during decision processing. It should still appear in incoming
	// because the first loop adds every node name.
	if _, ok := incoming["a"]; !ok {
		t.Error("a should be in incoming")
	}
}

func TestBuildGraph_NonMapEdgeSkipped(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{"a": map[string]any{"type": "agent"}},
		"edges": []any{
			"not-a-map",
			map[string]any{"from": "a", "to": "a"},
		},
	}
	_, _, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
}
