package runner

// Tests for what a run on the gemini CLI reports as its cost: a CLI without
// --output-format gives no token counts, so a call on it is unmetered even
// on a priced model, never $0. The CLI is a shell script and the Gemini API
// an httptest fake; no model is called.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// TestRun_GeminiCLIIsUnmetered: nodes on a gemini CLI without
// --output-format record their calls as unmetered, a fan one per item, and
// the run log, the node rows and the run totals say "unmetered", not $0.00.
// The log names why once, and no other line says the CLI reports no token
// counts. A node and a fan on the Gemini API with the same model in the same
// run are still priced from their usage, cached tokens at the model's cached
// price.
func TestRun_GeminiCLIIsUnmetered(t *testing.T) {
	const model = "gemini-2.5-flash"
	if !isMetered(model) {
		t.Fatalf("precondition: %q has no models-config entry; the test needs a priced model", model)
	}
	const in, cached, out = 40_000, 30_000, 8_000 // the Gemini API fake's usage per call
	apiCost := cachedCost(t, model, in, cached, out)
	if dollars(apiCost) == dollars(0) {
		t.Fatalf("precondition: %d/%d tokens on %q price to %s, which cannot be told from $0 on the two-decimal surfaces", in, out, model, dollars(0))
	}
	if apiCost == pricedCost(model, in, out) {
		t.Fatalf("precondition: %q prices a cached token at its input price, so the test cannot tell them apart", model)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"{\"answer\":\"ok\"}"}]}}],"usageMetadata":{"promptTokenCount":%d,"cachedContentTokenCount":%d,"candidatesTokenCount":%d}}`, in, cached, out)
	}))
	defer api.Close()

	items := []any{"a", "b", "c"}
	const cliNodes = `  cli:
    type: agent
    model: ` + model + `
    prompt: "answer"
    outputs: [answer]
  fan:
    type: parallel_fan
    model: ` + model + `
    prompt_template: "item {item}"
    fan_source: items
    outputs: [answer]
`
	const apiNode = `  api:
    type: agent
    provider: gemini
    model: ` + model + `
    prompt: "answer"
    outputs: [answer]
  apifan:
    type: parallel_fan
    provider: gemini
    model: ` + model + `
    prompt_template: "item {item}"
    fan_source: items
    outputs: [answer]
`
	cliCalls := int64(1 + len(items))
	apiCalls := int64(1 + len(items))
	cases := []struct {
		name     string
		nodes    string
		wantCost string
	}{
		{"cli only", cliNodes, "unmetered"},
		{"cli and api", cliNodes + apiNode, fmt.Sprintf("%s + unmetered (%d calls)", dollars(apiCalls*apiCost), cliCalls)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GEMINI_API_KEY", "k")
			t.Setenv("GEMINI_BASE_URL", api.URL)
			res, store, log, err := geminiCLIRun(t, oldGeminiCLI(`{"answer":"ok"}`), "name: cli\ninputs: [items]\nnodes:\n"+c.nodes, Config{Inputs: map[string]any{"items": items}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(log, "cost="+c.wantCost+"\n") {
				t.Errorf("log lacks cost=%s:\n%s", c.wantCost, log)
			}
			want := map[string][2]int64{"cli": {0, 1}, "fan": {0, int64(len(items))}}
			if strings.Contains(c.nodes, "api:") {
				want["api"] = [2]int64{1, 0}
				want["apifan"] = [2]int64{int64(len(items)), 0}
			}
			var metered, unmetered int64
			for node, w := range want {
				var m, u int64
				var cost int64
				if err := store.ReadDB.QueryRow(`SELECT metered_calls, unmetered_calls, cost_usd_x10000 FROM workflow_node_states WHERE node_name=?`, node).Scan(&m, &u, &cost); err != nil {
					t.Fatal(err)
				}
				if m != w[0] || u != w[1] {
					t.Errorf("node %s calls metered/unmetered = %d/%d, want %d/%d", node, m, u, w[0], w[1])
				}
				if wantCost := apiCost * w[0]; cost != wantCost {
					t.Errorf("node %s cost_usd_x10000 = %d, want %d", node, cost, wantCost)
				}
				metered += w[0]
				unmetered += w[1]
			}
			if int64(res.MeteredCalls) != metered || int64(res.UnmeteredCalls) != unmetered {
				t.Errorf("run calls metered/unmetered = %d/%d, want %d/%d", res.MeteredCalls, res.UnmeteredCalls, metered, unmetered)
			}
			// One line names why, once for the model, with the provider the
			// rows record and the CLI that has no --output-format.
			provider := readLocalNodeRow(t, store, "cli").provider
			line := fmt.Sprintf("provider %s reports no token counts for model %q (%s --help lists no --output-format flag, which gemini-cli has from 0.6.0): its calls are reported unmetered", provider, model, os.Getenv("GEMINI_CLI_PATH"))
			if n := strings.Count(log, line); n != 1 {
				t.Errorf("%q logged %d times, want once:\n%s", line, n, log)
			}
			if n := strings.Count(log, "reports no token counts"); n != 1 {
				t.Errorf("%d lines say the CLI reports no token counts, want the one above:\n%s", n, log)
			}
			if strings.Contains(log, fmt.Sprintf("model %q has no price", model)) {
				t.Errorf("log says the priced model %q has no price:\n%s", model, log)
			}

			snap := readRunCosts(t, store, res.RunID)
			if snap.Totals.Cost != c.wantCost {
				t.Errorf("total cost label = %q, want %q", snap.Totals.Cost, c.wantCost)
			}
			for _, n := range snap.Nodes {
				want := "unmetered"
				switch n.Name {
				case "api":
					want = dollars(apiCost)
				case "apifan":
					want = dollars(int64(len(items)) * apiCost)
				}
				if n.Cost != want {
					t.Errorf("node %s cost label = %q, want %q", n.Name, n.Cost, want)
				}
			}
		})
	}
}

// TestRun_NoAnsweredCallCountsNoCall: a node or a fan none of whose calls
// was answered records no call. The gemini CLI returns no result when it
// fails; an HTTP backend refused on its first call (the OpenAI-compatible
// one, and the Anthropic SDK, which streams) returns one with no turn.
// Either way the row counts no metered or unmetered call and no cost, and
// names no endpoint, so no surface shows $0.00 for a priced model that never
// answered: the totals count no call.
func TestRun_NoAnsweredCallCountsNoCall(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"refused"}}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"refused"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer refusing.Close()
	const failingCLI = "cat >/dev/null\necho 'refused' >&2\nexit 1\n"
	nodes := map[string]string{
		"agent":        "  only:\n    type: agent\n    model: %s\n    prompt: \"answer\"\n    outputs: [answer]\n",
		"parallel_fan": "  only:\n    type: parallel_fan\n    model: %s\n    prompt_template: \"item {item}\"\n    fan_source: items\n    outputs: [answer]\n",
	}
	for _, backend := range []struct{ name, model string }{
		{"gemini-cli", "gemini-2.5-flash"},
		{"openai", ResolveOpenAIModel("sonnet")},
		{"anthropic", string(ResolveModelAlias("sonnet"))},
	} {
		if !isMetered(backend.model) {
			t.Fatalf("precondition: %q has no models-config entry; the test needs a priced model", backend.model)
		}
		for _, kind := range []string{"agent", "parallel_fan"} {
			t.Run(backend.name+"/"+kind, func(t *testing.T) {
				yaml := "name: none\ninputs: [items]\nnodes:\n" + fmt.Sprintf(nodes[kind], backend.model)
				cfg := Config{Inputs: map[string]any{"items": []any{"a", "b"}}}
				var res *Result
				var store *db.Store
				var log string
				switch backend.name {
				case "gemini-cli":
					res, store, log, _ = geminiCLIRun(t, failingCLI, yaml, cfg)
				case "anthropic":
					t.Setenv("ANTHROPIC_API_KEY", "k")
					t.Setenv("ANTHROPIC_BASE_URL", refusing.URL)
					res, store, log, _ = providerRun(t, "anthropic", yaml, cfg)
				default:
					res, store, log, _ = localRun(t, refusing, yaml, cfg)
				}
				if res == nil {
					t.Fatalf("no result:\n%s", log)
				}
				row := readLocalNodeRow(t, store, "only")
				if row.status != "failed" {
					t.Fatalf("node status %q, want failed:\n%s", row.status, log)
				}
				var m, u, cost int64
				if err := store.ReadDB.QueryRow(`SELECT metered_calls, unmetered_calls, cost_usd_x10000 FROM workflow_node_states WHERE node_name='only'`).Scan(&m, &u, &cost); err != nil {
					t.Fatal(err)
				}
				if m != 0 || u != 0 || cost != 0 || row.baseURL != "" {
					t.Errorf("row metered/unmetered/cost/base_url = %d/%d/%d/%q, want no call recorded", m, u, cost, row.baseURL)
				}
				if res.MeteredCalls != 0 || res.UnmeteredCalls != 0 {
					t.Errorf("run calls metered/unmetered = %d/%d, want 0/0", res.MeteredCalls, res.UnmeteredCalls)
				}
				snap := readRunCosts(t, store, res.RunID)
				if snap.Totals.MeteredCalls != 0 || snap.Totals.UnmeteredCalls != 0 {
					t.Errorf("the totals count %d metered and %d unmetered calls, so they show %q, not —", snap.Totals.MeteredCalls, snap.Totals.UnmeteredCalls, snap.Totals.Cost)
				}
				if strings.Contains(log, "reports no token counts") || strings.Contains(log, "has no price") {
					t.Errorf("log names an unmetered call, though none was made:\n%s", log)
				}
			})
		}
	}
}

// geminiCLIRun runs yaml with the gemini CLI as the run's default provider.
// The CLI is a shell script with body script.
func geminiCLIRun(t *testing.T, script, yaml string, cfg Config) (*Result, *db.Store, string, error) {
	t.Helper()
	t.Setenv("GEMINI_CLI_PATH", fakeGeminiScript(t, script))
	return providerRun(t, "gemini-cli", yaml, cfg)
}

// providerRun runs yaml with provider as the run's default provider, and
// returns the result, the error, the store and the run log.
func providerRun(t *testing.T, provider, yaml string, cfg Config) (*Result, *db.Store, string, error) {
	t.Helper()
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	cfg.WorkflowYAML = wf
	cfg.ProjectName = "cli"
	cfg.ProjectDir = dir
	cfg.MaxIterations = 10
	cfg.Provider = provider
	cfg.Log = &log
	store := newTempStore(t)
	res, err := Run(context.Background(), store, cfg)
	return res, store, log.String(), err
}

// runCostView is a run's cost as its surfaces print it (`run-totals`,
// `chb_run_totals`, `chb_node_rationale`): each node's label, and the
// totals over the node rows and the run row's probe usage.
type runCostView struct {
	Nodes  []nodeCostView
	Totals totalsCostView
}

type nodeCostView struct {
	Name, Cost string
}

type totalsCostView struct {
	Cost                         string
	TokensIn, TokensOut          int64
	MeteredCalls, UnmeteredCalls int64
}

// readRunCosts reads runID's cost view from its node rows and the run row's
// probe usage, labelling each as formatCost does.
func readRunCosts(t *testing.T, store *db.Store, runID int64) runCostView {
	t.Helper()
	probe, err := db.ReadProbeUsage(store.ReadDB, runID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ReadDB.Query(
		`SELECT node_name, COALESCE(tokens_in,0), COALESCE(tokens_out,0), COALESCE(cost_usd_x10000,0),
		        COALESCE(metered_calls,0), COALESCE(unmetered_calls,0)
		 FROM workflow_node_states WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var view runCostView
	in, out, cost := probe.TokensIn, probe.TokensOut, probe.CostUSDx10000
	metered, unmetered := probe.MeteredCalls, probe.UnmeteredCalls
	for rows.Next() {
		var name string
		var nIn, nOut, nCost, nMetered, nUnmetered int64
		if err := rows.Scan(&name, &nIn, &nOut, &nCost, &nMetered, &nUnmetered); err != nil {
			t.Fatal(err)
		}
		view.Nodes = append(view.Nodes, nodeCostView{name, formatCost(nCost, int(nMetered), int(nUnmetered))})
		in, out, cost = in+nIn, out+nOut, cost+nCost
		metered, unmetered = metered+nMetered, unmetered+nUnmetered
	}
	view.Totals = totalsCostView{
		Cost: formatCost(cost, int(metered), int(unmetered)), TokensIn: in, TokensOut: out,
		MeteredCalls: metered, UnmeteredCalls: unmetered,
	}
	return view
}
