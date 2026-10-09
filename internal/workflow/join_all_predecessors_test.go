package workflow

import "testing"

// A join waits for every predecessor to be completed or skipped, so it never
// dispatches with unresolved {placeholders} from the ones still running. A
// failed predecessor keeps the join pending; that case is the
// run-termination rule's to resolve. A settled join also counts a failed or
// rejected input as finished, and waits for one completed input. With both
// inputs skipped nothing that ran reaches the join, so in either mode it is
// blocked (the caller skips it): not run with its inputs' placeholders
// unfilled, and not left pending to strand the run.
func TestFindReadyNodes_JoinWaitsForAllPredecessors(t *testing.T) {
	incoming := map[string]map[string]bool{"join": {"a": true, "b": true}}
	states := func(a, b string) map[string]map[string]any {
		return map[string]map[string]any{
			"a": {"status": a}, "b": {"status": b}, "join": {"status": "pending"},
		}
	}
	// Unconditioned edges, so every edge fires; readiness is purely the
	// all-predecessors rule this test is about.
	outgoing := map[string][]Edge{"a": {{Target: "join"}}, "b": {{Target: "join"}}}
	has := func(names []string) bool {
		for _, r := range names {
			if r == "join" {
				return true
			}
		}
		return false
	}
	cases := []struct {
		a, b                     string
		plainReady, settledReady bool
		blocked                  bool
	}{
		{"completed", "pending", false, false, false},
		{"completed", "running", false, false, false},
		{"completed", "failed", false, true, false},
		{"completed", "rejected", false, true, false},
		{"failed", "rejected", false, false, false},
		{"skipped", "failed", false, false, false},
		{"completed", "completed", true, true, false},
		{"completed", "skipped", true, true, false},
		{"skipped", "skipped", false, false, true},
	}
	for _, settled := range []bool{false, true} {
		join := map[string]any{"type": "agent"}
		if settled {
			join["join"] = "settled"
		}
		defn := map[string]any{"nodes": map[string]any{
			"a":    map[string]any{"type": "agent"},
			"b":    map[string]any{"type": "agent"},
			"join": join,
		}}
		for _, c := range cases {
			r, b := findReadyNodes(defn, map[string]any{}, states(c.a, c.b), incoming, outgoing)
			wantReady := c.plainReady
			if settled {
				wantReady = c.settledReady
			}
			if has(r) != wantReady || has(b) != c.blocked {
				t.Errorf("settled=%v a=%s b=%s: ready=%v blocked=%v, want ready=%v blocked=%v",
					settled, c.a, c.b, has(r), has(b), wantReady, c.blocked)
			}
		}
	}
}
