package cli

import "testing"

// Preflight names every command node and its argv, since each runs a
// program with no model in the loop. A workflow with none gets no line.
func TestPreflight_ListsCommandNodes(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{
		"b-scan": map[string]any{"type": "command", "argv": []any{"chb", "hive", "next", "--project", "{project}"}},
		"a-test": map[string]any{"type": "command", "argv": []any{"go", "test", "./..."}},
		"think":  map[string]any{"type": "agent", "prompt": "p"},
	}}
	r := newPreflightReport()
	r.listCommandNodes(defn)
	if len(r.results) != 1 || r.results[0].level != checkPass {
		t.Fatalf("results = %+v, want one pass line", r.results)
	}
	want := "a-test: go test ./...; b-scan: chb hive next --project {project}"
	if r.results[0].msg != want {
		t.Fatalf("msg = %q, want %q", r.results[0].msg, want)
	}

	r = newPreflightReport()
	r.listCommandNodes(map[string]any{"nodes": map[string]any{"think": map[string]any{"type": "agent"}}})
	if len(r.results) != 0 {
		t.Fatalf("a workflow with no command node printed %+v", r.results)
	}
}

// A command node has no prompt to ask for its outputs: they are keys of the
// JSON its program prints. Its accept: over two or more outputs passes the
// check an agent node's would fail.
func TestPreflight_AcceptCheckSkipsCommandNodes(t *testing.T) {
	node := func(ntype string) map[string]any {
		return map[string]any{"nodes": map[string]any{"scan": map[string]any{
			"type": ntype, "argv": []any{"chb", "hive", "next"}, "prompt": "",
			"outputs": []any{"phase", "actions"}, "accept": []any{"outputs.phase != 'terminal'"},
		}}}
	}
	for ntype, want := range map[string]checkLevel{"command": checkPass, "agent": checkFail} {
		r := newPreflightReport()
		r.checkAcceptAsksForItsOutputs(node(ntype))
		if len(r.results) != 1 || r.results[0].level != want {
			t.Fatalf("%s node: results = %+v, want one line at level %v", ntype, r.results, want)
		}
	}
}
