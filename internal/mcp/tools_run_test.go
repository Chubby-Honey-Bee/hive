package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// TestRunTotalsAndNodeRationale_ReportUnmetered: the MCP totals and a node's
// read report a node on an unpriced model as unmetered, not as $0.
func TestRunTotalsAndNodeRationale_ReportUnmetered(t *testing.T) {
	store, err := db.NewStore(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runID, err := store.Workflows().CreateWorkflowRun("totals", 1, "name: totals", "{}",
		[]db.NodeSeed{{Name: "local", Type: "agent"}, {Name: "cloud", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	// local: 3 calls to an unpriced model; cloud: 1 priced call costing 0.0500.
	const localCalls, cloudCost = 3, 500
	if err := store.Workflows().UpdateNodeMetrics(runID, "local", 10, 5, 0, "openai", "", 0, localCalls); err != nil {
		t.Fatal(err)
	}
	if err := store.Workflows().UpdateNodeMetrics(runID, "cloud", 10, 5, cloudCost, "anthropic", "", 1, 0); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s := &mcpServer{out: json.NewEncoder(&buf), inflight: newInflightRegistry(), store: store}

	totals := callToolJSON(t, s, &buf, "chb_run_totals", map[string]any{"run_id": runID})
	if want := fmt.Sprintf("0.0500 + unmetered (%d calls)", localCalls); totals["cost_usd"] != want || fmt.Sprint(totals["unmetered_calls"]) != fmt.Sprint(localCalls) {
		t.Errorf("run_totals = %v, want cost_usd %q and %d unmetered calls", totals, want, localCalls)
	}
	for node, want := range map[string]string{"local": "unmetered", "cloud": "0.0500"} {
		got := callToolJSON(t, s, &buf, "chb_node_rationale", map[string]any{"run_id": runID, "node_name": node})
		if got["cost_usd"] != want {
			t.Errorf("node_rationale %s cost_usd = %v, want %q", node, got["cost_usd"], want)
		}
	}
}

// TestRunTotals_CountTheConstraintProbes: the MCP totals add the run's
// constraint probes, which no node row carries, to its nodes'.
func TestRunTotals_CountTheConstraintProbes(t *testing.T) {
	store, err := db.NewStore(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runID, err := store.Workflows().CreateWorkflowRun("totals", 1, "name: totals", "{}", []db.NodeSeed{{Name: "cloud", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	node := [5]int64{10, 5, 500, 1, 0} // tokens in, out, cost x10000, metered, unmetered calls
	probe := [5]int64{11, 5, 0, 0, 1}  // one probe to an unpriced model
	if err := store.Workflows().UpdateNodeMetrics(runID, "cloud", node[0], node[1], node[2], "openai", "", node[3], node[4]); err != nil {
		t.Fatal(err)
	}
	if err := store.Workflows().AddRunProbeUsage(runID, probe[0], probe[1], probe[2], probe[3], probe[4]); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s := &mcpServer{out: json.NewEncoder(&buf), inflight: newInflightRegistry(), store: store}
	got := callToolJSON(t, s, &buf, "chb_run_totals", map[string]any{"run_id": runID})
	for i, k := range []string{"tokens_in", "tokens_out", "cost_usd_x10000", "metered_calls", "unmetered_calls"} {
		if want := node[i] + probe[i]; fmt.Sprint(got[k]) != fmt.Sprint(want) {
			t.Errorf("run_totals %s = %v, want %d, the node's and the probe's together", k, got[k], want)
		}
	}
}

// With the argument absent the child gets agent-run's own default, which the
// description names.
func TestAgentRunSpec_MaxIterationsNamesAgentRunsDefault(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "cli", "agent_run.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`IntVar\(&(?:o\.)?maxIterations, "max-iterations", (\d+),`).FindSubmatch(src)
	if m == nil {
		t.Fatal("agent-run's --max-iterations flag not found")
	}
	props := agentRunSpec()["inputSchema"].(map[string]any)["properties"].(map[string]any)
	desc := props["max_iterations"].(map[string]any)["description"].(string)
	if want := "default " + string(m[1]); !strings.Contains(strings.ToLower(desc), want) {
		t.Errorf("max_iterations description %q does not say %q", desc, want)
	}
}

// TestNodeRationaleSpec_ShapeAndRequiredFields locks the contract for
// chb_node_rationale — the MCP read path for a node's persisted rationale,
// so a host reads synthesize / decompose / evaluate outputs without shelling
// out.
func TestNodeRationaleSpec_ShapeAndRequiredFields(t *testing.T) {
	s := nodeRationaleSpec()
	if s["name"] != "chb_node_rationale" {
		t.Errorf("name = %v", s["name"])
	}
	schema, _ := s["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"run_id", "node_name"} {
		if _, ok := props[want]; !ok {
			t.Errorf("node_rationale spec missing property %q", want)
		}
	}
	required, _ := schema["required"].([]string)
	wantRequired := map[string]bool{"run_id": true, "node_name": true}
	if len(required) != 2 {
		t.Errorf("required = %v; want both run_id and node_name", required)
	}
	for _, r := range required {
		if !wantRequired[r] {
			t.Errorf("unexpected required field %q", r)
		}
	}
}

// TestRunStateSpec_ShapeAndRequiredFields locks chb_run_state's
// contract — the MCP path that recovers full untruncated outputs from
// workflow_runs.state_json (the 64 KB rationale cap can still chop a
// big fan-out aggregate; state holds the full text).
func TestRunStateSpec_ShapeAndRequiredFields(t *testing.T) {
	s := runStateSpec()
	if s["name"] != "chb_run_state" {
		t.Errorf("name = %v", s["name"])
	}
	schema, _ := s["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"run_id", "key"} {
		if _, ok := props[want]; !ok {
			t.Errorf("run_state spec missing property %q", want)
		}
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "run_id" {
		t.Errorf("required = %v; want only run_id (key is optional)", required)
	}
}

// chb_set_budget_mode's description names every tool that starts a run by
// the name a host calls, chb_research included. The tools that start a run
// are the open-world ones (mcp.md: every agent-spawning tool is destructive
// and open-world), read here from the specs themselves.
func TestSetBudgetModeDescription_NamesEveryToolThatStartsARun(t *testing.T) {
	desc, _ := setBudgetModeSpec()["description"].(string)
	var starters []string
	for _, raw := range allToolSpecs() {
		spec, _ := raw.(map[string]any)
		ann, _ := spec["annotations"].(map[string]any)
		if open, _ := ann["openWorldHint"].(bool); open {
			starters = append(starters, spec["name"].(string))
		}
	}
	if len(starters) == 0 {
		t.Fatal("no tool declares openWorldHint; the premise of this test is gone")
	}
	for _, name := range starters {
		if !strings.Contains(desc, name) {
			t.Errorf("chb_set_budget_mode description does not name %s, which starts a run:\n%s", name, desc)
		}
	}
}

// A budget_mode argument overrides the session's mode for its one run: the
// handler passes it as --budget-mode, which `chb agent-run` and `chb ask`
// prefer to the HIVE_BUDGET_MODE that spawnEnv and scriptEnv set from
// the session. Left out, the run takes the session's mode, as the argument
// and chb_set_budget_mode say. The tools that take the argument are read
// from the specs.
func TestBudgetModeArgument_NamesTheSessionDefaultAndTheOverride(t *testing.T) {
	setDesc, _ := setBudgetModeSpec()["description"].(string)
	var overrides []string
	for _, sentence := range strings.SplitAfter(setDesc, ". ") {
		if strings.Contains(sentence, "budget_mode") && strings.Contains(sentence, "overrides") {
			overrides = append(overrides, sentence)
		}
	}
	var takers int
	for _, raw := range allToolSpecs() {
		spec, _ := raw.(map[string]any)
		schema, _ := spec["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		prop, ok := props["budget_mode"].(map[string]any)
		if !ok {
			continue
		}
		takers++
		name := spec["name"].(string)
		if desc, _ := prop["description"].(string); !strings.Contains(desc, "chb_set_budget_mode") {
			t.Errorf("%s: budget_mode says %q; want it to name the session's mode (chb_set_budget_mode) as its default", name, desc)
		}
		named := false
		for _, sentence := range overrides {
			named = named || strings.Contains(sentence, name)
		}
		if !named {
			t.Errorf("chb_set_budget_mode does not say that %s's budget_mode overrides it:\n%s", name, setDesc)
		}
	}
	if takers == 0 {
		t.Fatal("no tool takes budget_mode; the premise of this test is gone")
	}
}

// The dial has four positions and every tool spec advertises them, but the
// setter rejected `cheap`.
func TestSetBudgetMode_AcceptsAllFourPositions(t *testing.T) {
	for _, mode := range []string{"premium", "standard", "cheap", "free"} {
		var buf bytes.Buffer
		s := &mcpServer{out: json.NewEncoder(&buf)}
		s.handleSetBudgetMode(rpcRequest{}, map[string]any{"mode": mode})
		if s.budgetMode != mode {
			t.Errorf("mode %q was rejected (budgetMode=%q)", mode, s.budgetMode)
		}
	}
}

// chb_preflight's `pass` needs a real preflight report: a binary named chb
// that resolves on PATH and exits cleanly does not pass.
func TestPreflightVerdict_RequiresARealReport(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		pass, found bool
	}{
		{"passing report", "  ✓ yaml validates\n\npreflight: PASS\n", true, true},
		{"failing report", "  ✗ provider auth\n\npreflight: FAIL (1 check failed)\n", false, true},
		{"some other exit-0 binary", "usage: chb [options]\n", false, false},
		{"empty output", "", false, false},
	}
	for _, c := range cases {
		pass, found := preflightVerdict(c.out)
		if pass != c.pass || found != c.found {
			t.Errorf("%s: pass=%v found=%v, want pass=%v found=%v", c.name, pass, found, c.pass, c.found)
		}
	}
}
