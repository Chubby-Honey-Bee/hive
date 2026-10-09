package runner

// Tests for repair on a local endpoint: the repair model is resolved for
// the node's provider, the run's sampling and the node's reasoning reach
// each repair call, a block naming no model repairs on the model the
// dispatch was sent, the model preflight covers repair models, and the
// completion event names the accepted attempt. Every server is an httptest
// fake; no model is called.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bodyModel is the model a captured chat body names.
func bodyModel(body map[string]any) string {
	m, _ := body["model"].(string)
	return m
}

// TestRun_RepairOnTheNodesProviderWithItsSampling: an on_reject block naming
// `sonnet` on an OpenAI-compatible node sends the model the OpenAI alias table
// resolves it to, with the run's top_p and the node's reasoning, and the
// ledger row and the node record the canonical id of what was sent.
func TestRun_RepairOnTheNodesProviderWithItsSampling(t *testing.T) {
	const nodeModel, blockModel, reasoning, topP = "local-model", "sonnet", "low", 0.9
	repairSent := ResolveOpenAIModel(blockModel)
	yaml := fmt.Sprintf(`name: repair
nodes:
  lens:
    type: agent
    model: %s
    reasoning: %s
    prompt: "answer"
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      model: %s
      max_repair_iterations: 1
`, nodeModel, reasoning, blockModel)
	f := &fakeOpenAI{models: []string{nodeModel, repairSent}, reply: func(body map[string]any) string {
		if bodyModel(body) == nodeModel {
			return chatReply(`{"ok": false}`, "stop", 1, 1)
		}
		return chatReply(`{"ok": true}`, "stop", 1, 1)
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	tp := topP
	_, store, _, err := localRun(t, srv, yaml, Config{TopP: &tp})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 2 {
		t.Fatalf("%d calls, want the dispatch and one repair", len(f.bodies))
	}
	repair := f.bodies[1]
	if got := bodyModel(repair); got != repairSent {
		t.Errorf("repair model = %q, want %q (%q resolved for openai)", got, repairSent, blockModel)
	}
	wantField(t, repair, "top_p", true, topP)
	wantField(t, repair, "reasoning_effort", true, reasoning)
	var ledger, trigger string
	if err := store.ReadDB.QueryRow(`SELECT COALESCE(model,''), COALESCE(trigger,'') FROM workflow_repairs`).Scan(&ledger, &trigger); err != nil {
		t.Fatal(err)
	}
	canonical := resolveAliasForPricing(repairSent)
	if ledger != canonical || trigger != "accept" {
		t.Errorf("ledger row = %q/%q, want %q/accept", ledger, trigger, canonical)
	}
	if row := readLocalNodeRow(t, store, "lens"); row.status != "completed" || row.model != canonical {
		t.Errorf("node = %s on %q, want completed on %q", row.status, row.model, canonical)
	}
}

// TestRun_SameModelRepairOfALocalTag: a block naming no model repairs on
// the model the dispatch was sent, as written.
func TestRun_SameModelRepairOfALocalTag(t *testing.T) {
	const tag = "qwen3.5:4b"
	yaml := fmt.Sprintf(`name: repair
nodes:
  lens:
    type: agent
    model: %s
    prompt: "answer"
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: 1
`, tag)
	f := &fakeOpenAI{models: []string{tag}}
	f.reply = func(map[string]any) string {
		if f.chats.Load() == 1 {
			return chatReply(`{"ok": false}`, "stop", 1, 1)
		}
		return chatReply(`{"ok": true}`, "stop", 1, 1)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if _, _, _, err := localRun(t, srv, yaml, Config{}); err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 2 {
		t.Fatalf("%d calls, want the dispatch and one repair", len(f.bodies))
	}
	for i, body := range f.bodies {
		if got := bodyModel(body); got != tag {
			t.Errorf("call %d model = %q, want %q", i, got, tag)
		}
	}
}

// TestRun_SameModelRepairSendsWhatTheDispatchSent: with no model in the
// block, the repair is sent the name the dispatch was sent, not the
// canonical id recorded for pricing, which can differ (`haiku` is priced as
// a dated id).
func TestRun_SameModelRepairSendsWhatTheDispatchSent(t *testing.T) {
	const alias = "haiku"
	if resolveAliasForPricing(alias) == alias {
		t.Fatalf("precondition: %q is recorded as itself; the test needs a name that differs", alias)
	}
	yaml := fmt.Sprintf(`name: repair
nodes:
  lens:
    type: agent
    model: %s
    prompt: "answer"
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: 1
`, alias)
	var sent []string
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		func(req RunRequest) (*RunResult, error) {
			sent = append(sent, req.Model)
			return &RunResult{FinalText: fmt.Sprintf(`{"ok": %v}`, len(sent) > 1), Turns: 1}, nil
		},
	}}
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), newTempStore(t), Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Backend: backend, Log: discardWriter{}}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[1] != sent[0] {
		t.Fatalf("models sent = %v, want the repair sent the dispatch's %q", sent, sent[0])
	}
}

// TestRun_SameModelRepairOfATierNode: a node naming a tier and no model,
// with a block naming no model, is repaired on the model its tier resolved
// to for the node's provider, as the dispatch was, not on the model the
// provider picks when none is named.
func TestRun_SameModelRepairOfATierNode(t *testing.T) {
	t.Setenv("HIVE_BUDGET_MODE", string(BudgetStandard))
	tierModel := nodeModel("", "worker", BudgetStandard, BackendOpenAI)
	yaml := `name: repair
nodes:
  lens:
    type: agent
    tier: worker
    prompt: "answer"
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: 1
`
	f := &fakeOpenAI{models: []string{tierModel}}
	f.reply = func(map[string]any) string {
		if f.chats.Load() == 1 {
			return chatReply(`{"ok": false}`, "stop", 1, 1)
		}
		return chatReply(`{"ok": true}`, "stop", 1, 1)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, store, _, err := localRun(t, srv, yaml, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 2 {
		t.Fatalf("%d calls, want the dispatch and one repair", len(f.bodies))
	}
	dispatched := bodyModel(f.bodies[0])
	if dispatched == ResolveOpenAIModel("") {
		t.Fatalf("precondition: the tier's model %q is the provider's default; the test needs one that differs", dispatched)
	}
	if got := bodyModel(f.bodies[1]); got != dispatched {
		t.Errorf("repair model = %q, want the dispatch's %q", got, dispatched)
	}
	var ledger string
	if err := store.ReadDB.QueryRow(`SELECT COALESCE(model,'') FROM workflow_repairs`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if want := resolveAliasForPricing(dispatched); ledger != want {
		t.Errorf("ledger model = %q, want %q", ledger, want)
	}
}

// TestRun_ModelPreflightCoversRepairModels: a repair model the endpoint does
// not serve is refused before any call, named as the node's on_reject; a
// block naming no model adds nothing, and one making no attempt is skipped.
func TestRun_ModelPreflightCoversRepairModels(t *testing.T) {
	const served = "local-model"
	tierModel := nodeModel("", "worker", BudgetStandard, BackendOpenAI)
	// A missing model a tier resolved to is refused with where to map the
	// tier.
	cases := []struct {
		name, block string
		missing     string // "" when the run goes ahead
		tier        string
	}{
		{"a tier the endpoint lacks", "tier: worker", tierModel, "worker"},
		{"a model the endpoint lacks", "model: other-model", "other-model", ""},
		{"no model: the node's own", "prompt_template: \"fix {outputs}\"", "", ""},
		{"no attempts", "model: other-model\n      max_repair_iterations: 0", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HIVE_BUDGET_MODE", string(BudgetStandard))
			yaml := fmt.Sprintf(`name: repair
nodes:
  lens:
    type: agent
    model: %s
    prompt: "answer"
    outputs: [ok]
    on_reject:
      %s
`, served, c.block)
			f := &fakeOpenAI{models: []string{served}, reply: func(map[string]any) string { return chatReply(`{"ok": true}`, "stop", 1, 1) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			_, _, _, err := localRun(t, srv, yaml, Config{})
			if c.missing == "" {
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				return
			}
			want := fmt.Sprintf("%q (node lens on_reject)", c.missing)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want a refusal naming %s", err, want)
			}
			if hint := "Tier " + c.tier + " resolves through the models config's tiers"; strings.Contains(err.Error(), "Tier ") != (c.tier != "") ||
				(c.tier != "" && !strings.Contains(err.Error(), hint)) {
				t.Fatalf("err = %v, want the tier hint exactly when a tier's model is missing", err)
			}
			if n := f.chats.Load(); n != 0 {
				t.Errorf("%d chat calls, want none before the refusal", n)
			}
		})
	}
}

// TestPreflightEndpointModels_UnusedBaseURL: OPENAI_BASE_URL set while no
// node runs on the OpenAI-compatible backend is reported, naming the run
// default, and nothing is asked of the endpoint.
func TestPreflightEndpointModels_UnusedBaseURL(t *testing.T) {
	f := &fakeOpenAI{models: []string{"m"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	defn := map[string]any{"nodes": map[string]any{"n": map[string]any{"type": "agent", "model": "m"}}}
	checks, err := PreflightEndpointModels(context.Background(), Config{Provider: "anthropic"}, defn)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Unused != BackendAnthropic || !strings.Contains(checks[0].Summary(), "the run default is anthropic") {
		t.Fatalf("checks = %+v, want OPENAI_BASE_URL reported unused under anthropic", checks)
	}
	if n := f.lists.Load(); n != 0 {
		t.Errorf("%d model listings, want none", n)
	}
}
