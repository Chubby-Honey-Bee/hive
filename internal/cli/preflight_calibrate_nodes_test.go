package cli

import "testing"

// Preflight names every calibrate node with its rebuild and scope, since
// each runs the calibration recompute with no model in the loop. A
// workflow with none gets no line.
func TestPreflight_ListsCalibrateNodes(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{
		"b-cal": map[string]any{"type": "calibrate", "rebuild": true, "scope": "d1=2"},
		"a-cal": map[string]any{"type": "calibrate"},
		"c-cal": map[string]any{"type": "calibrate", "scope": ""},
		"think": map[string]any{"type": "agent", "prompt": "p"},
	}}
	r := newPreflightReport()
	r.listCalibrateNodes(defn)
	if len(r.results) != 1 || r.results[0].level != checkPass {
		t.Fatalf("results = %+v, want one pass line", r.results)
	}
	want := "a-cal: rebuild=false scope=every scope; b-cal: rebuild=true scope=d1=2; c-cal: rebuild=false scope=global"
	if r.results[0].msg != want {
		t.Fatalf("msg = %q, want %q", r.results[0].msg, want)
	}

	r = newPreflightReport()
	r.listCalibrateNodes(map[string]any{"nodes": map[string]any{"think": map[string]any{"type": "agent"}}})
	if len(r.results) != 0 {
		t.Fatalf("a workflow with no calibrate node printed %+v", r.results)
	}
}
