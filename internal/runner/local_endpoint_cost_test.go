package runner

// Tests for what a local run reports and records: a model with no price is
// reported unmetered, per model call, in the run log and the node rows, off
// this machine, and costs nothing on it; the
// artifact records the sampling the run used; an unset
// temperature on a local endpoint is named; a --temperature the Anthropic
// API cannot take, and a provider: no backend has, are refused before the
// run starts; a fan item with no text is named. Every server is an httptest
// fake; no model is called.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// dollars renders a cost in 1/10000 USD as runner.md § Per-node persistence
// and cost does: $, whole dollars, two digits of cents, truncated.
func dollars(x10000 int64) string {
	return fmt.Sprintf("$%d.%02d", x10000/10000, x10000%10000/100)
}

// pricedCost is a call's cost from the models config's prices.
func pricedCost(model string, in, out int64) int64 {
	cfg := models.Load()
	return (in*cfg.PriceInPer1MTokensX10000(model) + out*cfg.PriceOutPer1MTokensX10000(model)) / 1_000_000
}

func TestFormatCost(t *testing.T) {
	for _, c := range []struct {
		cost               int64
		metered, unmetered int
		want               string
	}{
		{14201, 3, 0, dollars(14201)},
		{0, 1, 0, dollars(0)},
		{0, 0, 0, dollars(0)},
		{0, 0, 2, "unmetered"},
		{14201, 3, 2, dollars(14201) + " + unmetered (2 calls)"},
	} {
		if got := formatCost(c.cost, c.metered, c.unmetered); got != c.want {
			t.Errorf("formatCost(%d, %d, %d) = %q, want %q", c.cost, c.metered, c.unmetered, got, c.want)
		}
	}
}

// TestRun_UnpricedModelIsUnmetered: a run on a model with no price, served by
// provider openai, which is off this machine whatever its endpoint, logs
// cost=unmetered; a priced one logs dollars; a run mixing them logs both,
// counting every call of the unpriced model's tool loop. The node rows, the
// run totals say the same.
func TestRun_UnpricedModelIsUnmetered(t *testing.T) {
	priced := ResolveOpenAIModel("sonnet")
	if !isMetered(priced) {
		t.Fatalf("precondition: %q has no models-config entry", priced)
	}
	const unpriced = "local-unpriced-model"
	if isMetered(unpriced) {
		t.Fatalf("precondition: %q is priced", unpriced)
	}
	const in, out = 1000, 500
	const toolTurns = 3 // the unpriced node's calls before its answer
	node := func(name, model string) string {
		return fmt.Sprintf("  %s:\n    type: agent\n    model: %s\n    prompt: \"answer %s\"\n    outputs: [answer]\n", name, model, name)
	}
	cases := []struct {
		name            string
		nodes           map[string]string // node → model
		wantCost        string
		wantUnmetered   int64
		wantMeteredRuns int64
	}{
		{"unpriced", map[string]string{"local": unpriced}, "unmetered", toolTurns + 1, 0},
		{"priced", map[string]string{"cloud": priced}, dollars(pricedCost(priced, in, out)), 0, 1},
		{"mixed", map[string]string{"local": unpriced, "cloud": priced},
			fmt.Sprintf("%s + unmetered (%d calls)", dollars(pricedCost(priced, in, out)), toolTurns+1), toolTurns + 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var localCalls atomic.Int64
			f := &fakeOpenAI{models: []string{priced, unpriced}, reply: func(body map[string]any) string {
				if bodyModel(body) == unpriced && localCalls.Add(1) <= toolTurns {
					return toolCallReply("", in, out)
				}
				return chatReply(`{"answer":"ok"}`, "stop", in, out)
			}}
			srv := httptest.NewServer(f)
			defer srv.Close()
			yaml := "name: local\nnodes:\n"
			for name, model := range c.nodes {
				yaml += node(name, model)
			}
			res, store, log, err := localRun(t, srv, yaml, Config{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(log, "done. ") || !strings.Contains(log, "cost="+c.wantCost+"\n") {
				t.Errorf("log lacks cost=%s on its done line:\n%s", c.wantCost, log)
			}
			if int64(res.UnmeteredCalls) != c.wantUnmetered || int64(res.MeteredCalls) != c.wantMeteredRuns {
				t.Errorf("run calls metered/unmetered = %d/%d, want %d/%d", res.MeteredCalls, res.UnmeteredCalls, c.wantMeteredRuns, c.wantUnmetered)
			}
			for name, model := range c.nodes {
				var metered, unmetered int64
				if err := store.ReadDB.QueryRow(`SELECT metered_calls, unmetered_calls FROM workflow_node_states WHERE node_name=?`, name).Scan(&metered, &unmetered); err != nil {
					t.Fatal(err)
				}
				wantM, wantU := int64(1), int64(0)
				if model == unpriced {
					wantM, wantU = 0, toolTurns+1
				}
				if metered != wantM || unmetered != wantU {
					t.Errorf("node %s calls metered/unmetered = %d/%d, want %d/%d", name, metered, unmetered, wantM, wantU)
				}
			}
			if logged := strings.Contains(log, fmt.Sprintf("model %q has no price", unpriced)); logged != (c.wantUnmetered > 0) {
				t.Errorf("no-price line logged = %v, want %v", logged, c.wantUnmetered > 0)
			}

			// The cost labels the run's surfaces print, from the node rows.
			snap := readRunCosts(t, store, res.RunID)
			if snap.Totals.Cost != c.wantCost {
				t.Errorf("total cost label = %q, want %q", snap.Totals.Cost, c.wantCost)
			}
			for _, n := range snap.Nodes {
				want := dollars(pricedCost(priced, in, out))
				if c.nodes[n.Name] == unpriced {
					want = "unmetered"
				}
				if n.Cost != want {
					t.Errorf("node %s cost label = %q, want %q", n.Name, n.Cost, want)
				}
			}
		})
	}
}

// TestRun_LocalModelCostsNothing: a call to a model on this machine, on
// provider local at a loopback endpoint, costs nothing. A run on a model the
// models config does not price, its tool turns and its constraint probe
// included, logs and totals $0.00, counts every call metered, and says
// nothing about a price. A cloud tag on the same server leaves this machine,
// so its calls stay unmetered and the log says why.
func TestRun_LocalModelCostsNothing(t *testing.T) {
	const local, cloud = "local-unpriced-model", "local-unpriced-model:cloud"
	for _, m := range []string{local, cloud} {
		if isMetered(m) {
			t.Fatalf("precondition: %q is priced", m)
		}
	}
	const in, out, toolTurns = 1000, 500, 2
	run := func(t *testing.T, yaml string) (*Result, runCostView, string) {
		t.Helper()
		var turns atomic.Int64
		f := &fakeOpenAI{models: []string{local, cloud}, reply: func(body map[string]any) string {
			if body["tools"] != nil && turns.Add(1) <= toolTurns {
				return toolCallReply("", in, out)
			}
			return chatReply(`{"answer":"ok"}`, "stop", in, out)
		}}
		srv := httptest.NewServer(f)
		defer srv.Close()
		for k, v := range map[string]string{"HIVE_LOCAL_BASE_URL": srv.URL + "/v1", "OPENAI_BASE_URL": "", "HIVE_PROVIDER": "",
			"HIVE_PROVIDER_ALLOWLIST": "", "HIVE_DISABLE_RATE_LIMIT": "1"} {
			t.Setenv(k, v)
		}
		dir := t.TempDir()
		wf := filepath.Join(dir, "wf.yaml")
		if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		var log bytes.Buffer
		store := newTempStore(t)
		res, err := Run(context.Background(), store, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 10, Provider: "local", Log: &log})
		if err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
		return res, readRunCosts(t, store, res.RunID), log.String()
	}

	t.Run("on this machine", func(t *testing.T) {
		res, snap, log := run(t, "name: local\nnodes:\n"+
			"  loop:\n    type: agent\n    model: "+local+"\n    prompt: \"answer loop\"\n    outputs: [answer]\n"+
			"  schema:\n    type: agent\n    model: "+local+"\n    tools: []\n    prompt: \"answer schema\"\n    outputs: [answer]\n"+
			"    output_schema: {type: object, required: [answer], properties: {answer: {type: string}}}\n"+
			"edges:\n  - {from: loop, to: schema}\n")
		if !strings.Contains(log, "cost=$0.00\n") || strings.Contains(log, "has no price") || strings.Contains(log, "unmetered") {
			t.Errorf("want cost=$0.00 and no word of a price or of unmetered calls:\n%s", log)
		}
		calls := int64(toolTurns + 1 + 1 + 1) // the loop's turns and answer, the schema node, its probe
		if snap.Totals.Cost != dollars(0) || snap.Totals.MeteredCalls != calls || snap.Totals.UnmeteredCalls != 0 || res.UnmeteredCalls != 0 {
			t.Errorf("totals %+v (run %d unmetered), want %s and %d metered calls, none unmetered", snap.Totals, res.UnmeteredCalls, dollars(0), calls)
		}
		for _, n := range snap.Nodes {
			if n.Cost != dollars(0) {
				t.Errorf("node %s cost label = %q, want %s", n.Name, n.Cost, dollars(0))
			}
		}
	})

	t.Run("an Ollama cloud model", func(t *testing.T) {
		_, snap, log := run(t, "name: cloud\nnodes:\n  ask:\n    type: agent\n    model: "+cloud+"\n    tools: []\n    prompt: \"answer ask\"\n    outputs: [answer]\nedges: []\n")
		if !strings.Contains(log, fmt.Sprintf("model %q has no price in the models config", cloud)) || snap.Totals.Cost != "unmetered" {
			t.Errorf("total %q; want unmetered, with the no-price line:\n%s", snap.Totals.Cost, log)
		}
	})
}

// TestRun_ArtifactRecordsTheSamplingSent: --artifact records the
// temperature and top-p the run sent, as the flags set them.
func TestRun_ArtifactRecordsTheSamplingSent(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		temp, topP *float64
	}{{nil, nil}, {f64(0.4), nil}, {nil, f64(0.9)}, {f64(0.7), f64(0.8)}} {
		t.Run(fmt.Sprintf("temp=%v/top_p=%v", deref(c.temp), deref(c.topP)), func(t *testing.T) {
			f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "artifact.json")
			if _, _, _, err := localRun(t, srv, fmt.Sprintf(oneNodeYAML, "local-model"), Config{Temperature: c.temp, TopP: c.topP, ArtifactPath: path}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var art struct {
				Determinism map[string]any `json:"determinism"`
			}
			if err := json.Unmarshal(raw, &art); err != nil {
				t.Fatal(err)
			}
			// temperature is always written, null when unset; top_p only
			// when set.
			wantField(t, art.Determinism, "temperature", true, deref(c.temp))
			wantField(t, art.Determinism, "top_p", c.topP != nil, deref(c.topP))
			// And the calls sent exactly those.
			wantField(t, f.bodies[0], "temperature", c.temp != nil, deref(c.temp))
			wantField(t, f.bodies[0], "top_p", c.topP != nil, deref(c.topP))
		})
	}
}

// TestRun_UnsetTemperatureOnALocalEndpointIsNamed: with OPENAI_BASE_URL set
// and no sampling flag, the run log says what Ollama then samples at.
func TestRun_UnsetTemperatureOnALocalEndpointIsNamed(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	for _, cfg := range []Config{{}, {Temperature: f64(0.2)}, {TopP: f64(0.9)}, {Deterministic: true}} {
		t.Run(fmt.Sprintf("temp=%v/top_p=%v/det=%v", deref(cfg.Temperature), deref(cfg.TopP), cfg.Deterministic), func(t *testing.T) {
			f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			_, _, log, err := localRun(t, srv, fmt.Sprintf(oneNodeYAML, "local-model"), cfg)
			if err != nil {
				t.Fatal(err)
			}
			unset := cfg.Temperature == nil && cfg.TopP == nil && !cfg.Deterministic
			named := strings.Contains(log, "Ollama's OpenAI endpoint then uses 1.0 for both")
			if named != unset {
				t.Errorf("unset-sampling line logged = %v, want %v:\n%s", named, unset, log)
			}
		})
	}
}

// TestPreflightSampling: a --temperature above 1 is refused when a node runs
// on the Anthropic SDK backend, whose API takes 0 to 1, and left alone
// otherwise.
func TestPreflightSampling(t *testing.T) {
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	defn := func(provider string) map[string]any {
		n := map[string]any{"type": "agent", "model": "m"}
		if provider != "" {
			n["provider"] = provider
		}
		return map[string]any{"nodes": map[string]any{"a": n}}
	}
	f64 := func(v float64) *float64 { return &v }
	cases := []struct {
		runProvider, nodeProvider string
		temp                      *float64
	}{
		{"openai", "", f64(1.5)},
		{"anthropic", "", f64(1.5)},
		{"anthropic", "", f64(1)},
		{"openai", "anthropic", f64(1.5)},
		{"anthropic", "openai", f64(1.5)},
		{"gemini", "", f64(2)},
		{"anthropic", "", nil},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%s/%v", c.runProvider, c.nodeProvider, deref(c.temp)), func(t *testing.T) {
			err := PreflightSampling(Config{Provider: c.runProvider, Temperature: c.temp}, defn(c.nodeProvider))
			kind := c.runProvider
			if c.nodeProvider != "" {
				kind = c.nodeProvider
			}
			refuse := kind == "anthropic" && c.temp != nil && *c.temp > 1
			if (err != nil) != refuse {
				t.Fatalf("err = %v, want refused = %v", err, refuse)
			}
			if refuse && !strings.Contains(err.Error(), fmt.Sprintf("--temperature %g", *c.temp)) {
				t.Errorf("err = %v, want it to name the flag's value", err)
			}
		})
	}
	// Through a run: refused before the run row.
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte("name: t\nnodes:\n  a:\n    type: agent\n    provider: anthropic\n    model: m\n    prompt: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	backend := &recordingBackend{}
	_, err := Run(context.Background(), store, Config{WorkflowYAML: wf, ProjectDir: dir, Backend: backend, Temperature: f64(1.5), Log: discardWriter{}})
	if err == nil || !strings.Contains(err.Error(), "Anthropic Messages API takes 0 to 1") {
		t.Fatalf("run err = %v, want the temperature refused", err)
	}
	var runs int
	_ = store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_runs`).Scan(&runs)
	if runs != 0 {
		t.Errorf("%d workflow_runs rows, want none", runs)
	}
}

// TestPreflightWorkflowProviders_UnknownProvider: a provider: that names no
// backend is refused rather than served on the run default; an alias passes.
func TestPreflightWorkflowProviders_UnknownProvider(t *testing.T) {
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	for _, p := range []string{"ollama", "lmstudio", "openai", "copilot", "claude-code", "google"} {
		t.Run(p, func(t *testing.T) {
			err := PreflightWorkflowProviders(map[string]any{"nodes": map[string]any{"n": map[string]any{"provider": p}}})
			if known := canonicalKind(p) != ""; (err == nil) != known {
				t.Fatalf("err = %v, want refused = %v", err, !known)
			}
			if err != nil && !strings.Contains(err.Error(), fmt.Sprintf("provider %q is not a known provider", p)) {
				t.Errorf("err = %v, want it to name %q", err, p)
			}
		})
	}
}

// TestRunFanOut_ItemWithNoTextIsNamed: an item that returns no text and no
// error is a failed item, and the partial-success line names it.
func TestRunFanOut_ItemWithNoTextIsNamed(t *testing.T) {
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		func(req RunRequest) (*RunResult, error) {
			if strings.HasSuffix(req.Prompt, "empty") {
				return &RunResult{}, nil
			}
			return &RunResult{FinalText: "answer"}, nil
		},
	}}
	t.Setenv("HIVE_MAX_PARALLEL_FAN", "1")
	var log bytes.Buffer
	rc := &runtimeContext{ctx: context.Background(), store: newParallelTestStore(t), backend: backend, logf: makeLogf(&log)}
	items := []string{"ok", "empty"}
	node := workflow.DispatchNode{Node: "fan", Type: "parallel_fan", ResolvedPrompt: "item {item}", FanItems: items, FanItemPlaceholder: "{item}"}
	if _, err := rc.runFanOut(context.Background(), node, backend, RunRequest{}); err != nil {
		t.Fatalf("1 of 2 items answered, which meets half: %v", err)
	}
	if want := fmt.Sprintf("partial success — 1/%d items (failed: item 2: no text)", len(items)); !strings.Contains(log.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, log.String())
	}
}
