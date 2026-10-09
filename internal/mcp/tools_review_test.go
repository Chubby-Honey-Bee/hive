package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/review"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// writeFindings writes a findings.json holding n critical findings.
func writeFindings(t *testing.T, dir string, n int) string {
	t.Helper()
	agg := review.Aggregate{ByLens: map[string]review.LensReport{}, Totals: map[string]int{}}
	for i := 0; i < n; i++ {
		agg.AllFindings = append(agg.AllFindings, review.Finding{
			Lens: "l", Severity: "critical", File: fmt.Sprintf("f%d.go", i), Line: i + 1, Issue: "i", Fix: "f",
		})
	}
	b, err := json.Marshal(agg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "findings.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// max_fixes: 0 means no cap; absent, the default of 5 applies.
func TestGenImplementWorkflow_MaxFixesAbsentZeroAndSet(t *testing.T) {
	const n = 8
	dir := t.TempDir()
	findings := writeFindings(t, dir, n)
	var buf bytes.Buffer
	s := newTestServer(&buf)

	for _, tc := range []struct {
		name string
		arg  any // nil = absent
		want int
	}{
		{"absent", nil, min(5, n)},
		{"zero", 0, n},
		{"seven", 7, min(7, n)},
		{"three", 3, min(3, n)},
	} {
		args := map[string]any{"findings_json": findings, "out": filepath.Join(dir, tc.name+".yaml")}
		if tc.arg != nil {
			args["max_fixes"] = tc.arg
		}
		text, isErr, rpcErr := callTool(t, s, &buf, "chb_gen_implement_workflow", args)
		if rpcErr != nil || isErr {
			t.Fatalf("%s: error %v %s", tc.name, rpcErr, text)
		}
		var res struct {
			FixCount int `json:"fix_count"`
		}
		if err := json.Unmarshal([]byte(text), &res); err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, text)
		}
		if res.FixCount != tc.want {
			t.Errorf("max_fixes %s: fix_count = %d, want %d", tc.name, res.FixCount, tc.want)
		}
	}
}

// chb_self_implement passes max_fixes: 0 on, so `chb implement` runs
// uncapped, as the tool's schema says 0 means.
func TestSelfImplement_PassesMaxFixesThrough(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"workflows", "agents", "workspace"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub := filepath.Join(root, "chb-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"ARGS: $*\"\necho \"[agent-run] workflow run ID: 3\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = &db.Store{Path: filepath.Join(root, "workspace", "hive.db")}
	s.binOnce.Do(func() { s.bin = stub })

	for _, tc := range []struct {
		name string
		arg  any
		want string // "" = no --max-fixes flag at all
	}{
		{"absent", nil, ""},
		{"zero", 0, "--max-fixes 0"},
		{"two", 2, "--max-fixes 2"},
	} {
		args := map[string]any{"dry_run": true}
		if tc.arg != nil {
			args["max_fixes"] = tc.arg
		}
		text, isErr, rpcErr := callTool(t, s, &buf, "chb_self_implement", args)
		if rpcErr != nil || isErr {
			t.Fatalf("%s: error %v %s", tc.name, rpcErr, text)
		}
		var res struct {
			LogPath string `json:"log_path"`
		}
		if err := json.Unmarshal([]byte(text), &res); err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, text)
		}
		log, err := os.ReadFile(res.LogPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(log)
		if tc.want == "" {
			if strings.Contains(got, "--max-fixes") {
				t.Errorf("absent max_fixes still passed a flag: %s", got)
			}
		} else if !strings.Contains(got, tc.want+" ") && !strings.Contains(got, tc.want+"\n") {
			t.Errorf("max_fixes %s: want %q in the child's args, got %s", tc.name, tc.want, got)
		}
	}
}

// chb_render_review declares run_id an integer, as its handler reads it, so
// the id reaches the report header.
func TestRenderReview_RunIDReachesTheHeader(t *testing.T) {
	dir := t.TempDir()
	findings := writeFindings(t, dir, 1)
	out := filepath.Join(dir, "REVIEW.md")
	var buf bytes.Buffer
	s := newTestServer(&buf)

	const runID = 42
	text, isErr, rpcErr := callTool(t, s, &buf, "chb_render_review",
		map[string]any{"findings_json": findings, "out": out, "workflow": "wf", "run_id": runID})
	if rpcErr != nil || isErr {
		t.Fatalf("error %v %s", rpcErr, text)
	}
	md, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("(run id %d)", runID); !strings.Contains(string(md), want) {
		t.Errorf("REVIEW.md header lacks %q:\n%s", want, md)
	}

	_, _, rpcErr = callTool(t, s, &buf, "chb_render_review",
		map[string]any{"findings_json": findings, "out": out, "run_id": "42"})
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Errorf("a string run_id must be refused as -32602, got %+v", rpcErr)
	}
}

// The fix nodes name tier worker and their repair tier synthesist, unless
// model / repair_model name a model, which the nodes then use.
func TestGenImplementWorkflow_ModelArgsDecideTheNodesModels(t *testing.T) {
	const n = 2
	dir := t.TempDir()
	findings := writeFindings(t, dir, n)
	var buf bytes.Buffer
	s := newTestServer(&buf)

	for _, tc := range []struct {
		name, model, repair string // "" = argument absent, tier default
	}{
		{name: "absent"},
		{name: "both", model: "haiku", repair: "claude-opus-4-8"},
		{name: "repair only", repair: "opus"},
	} {
		out := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".yaml")
		args := map[string]any{"findings_json": findings, "out": out}
		if tc.model != "" {
			args["model"] = tc.model
		}
		if tc.repair != "" {
			args["repair_model"] = tc.repair
		}
		text, isErr, rpcErr := callTool(t, s, &buf, "chb_gen_implement_workflow", args)
		if rpcErr != nil || isErr {
			t.Fatalf("%s: error %v %s", tc.name, rpcErr, text)
		}
		defn, err := workflow.LoadYAML(out)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		nodes, _ := defn["nodes"].(map[string]any)
		for i := 1; i <= n; i++ {
			node, _ := nodes[fmt.Sprintf("fix-%d", i)].(map[string]any)
			if node == nil {
				t.Fatalf("%s: fix-%d missing", tc.name, i)
			}
			rej, _ := node["on_reject"].(map[string]any)
			checkModelOrTier(t, tc.name, "fix", node, tc.model, "worker")
			checkModelOrTier(t, tc.name, "on_reject", rej, tc.repair, "synthesist")
		}
	}
}

func checkModelOrTier(t *testing.T, name, what string, m map[string]any, model, tier string) {
	t.Helper()
	if model == "" {
		if m["tier"] != tier || m["model"] != nil {
			t.Errorf("%s: %s tier=%v model=%v; want tier %s and no model", name, what, m["tier"], m["model"], tier)
		}
		return
	}
	if m["model"] != model || m["tier"] != nil {
		t.Errorf("%s: %s tier=%v model=%v; want model %s and no tier", name, what, m["tier"], m["model"], model)
	}
}

// chb_self_implement forwards model and repair_model to `chb implement` as
// --model and --repair-model, and passes neither when they are absent.
func TestSelfImplement_ForwardsModelArgs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"workflows", "agents", "workspace"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub := filepath.Join(root, "chb-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"ARGS: $*\"\necho \"[agent-run] workflow run ID: 3\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = &db.Store{Path: filepath.Join(root, "workspace", "hive.db")}
	s.spawnLimit = &spawnLimiter{max: 0}
	s.binOnce.Do(func() { s.bin = stub })

	for _, tc := range []struct {
		name, model, repair string
	}{
		{name: "absent"},
		{name: "both", model: "haiku", repair: "opus"},
	} {
		args := map[string]any{"dry_run": true}
		if tc.model != "" {
			args["model"] = tc.model
		}
		if tc.repair != "" {
			args["repair_model"] = tc.repair
		}
		text, isErr, rpcErr := callTool(t, s, &buf, "chb_self_implement", args)
		if rpcErr != nil || isErr {
			t.Fatalf("%s: error %v %s", tc.name, rpcErr, text)
		}
		var res struct {
			LogPath string `json:"log_path"`
		}
		if err := json.Unmarshal([]byte(text), &res); err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, text)
		}
		log, err := os.ReadFile(res.LogPath)
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(log))
		for _, flag := range []struct{ name, want string }{{"--model", tc.model}, {"--repair-model", tc.repair}} {
			got, present := flagValue(fields, flag.name)
			if flag.want == "" && present {
				t.Errorf("%s: absent argument still passed %s %s", tc.name, flag.name, got)
			}
			if flag.want != "" && got != flag.want {
				t.Errorf("%s: %s = %q; want %q (args: %s)", tc.name, flag.name, got, flag.want, log)
			}
		}
	}
}

func flagValue(fields []string, name string) (string, bool) {
	for i, f := range fields {
		if f == name && i+1 < len(fields) {
			return fields[i+1], true
		}
	}
	return "", false
}

// TestSelfReviewSpec_RequiredFields asserts the spec is JSON-shaped
// the way an MCP host expects (name + description + properties map).
func TestSelfReviewSpec_RequiredFields(t *testing.T) {
	s := selfReviewSpec()
	if s["name"] != "chb_self_review" {
		t.Errorf("name = %v", s["name"])
	}
	schema, _ := s["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["provider"]; !ok {
		t.Error("missing provider property")
	}
}

// TestSelfImplementSpec_RequiredFields asserts the implement spec
// surfaces all the optional flags the CLI honours.
func TestSelfImplementSpec_RequiredFields(t *testing.T) {
	s := selfImplementSpec()
	schema, _ := s["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"findings", "severity", "max_fixes", "provider"} {
		if _, ok := props[want]; !ok {
			t.Errorf("self_implement spec missing property %q", want)
		}
	}
}
