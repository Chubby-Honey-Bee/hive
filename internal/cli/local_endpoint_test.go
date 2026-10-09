package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/spf13/cobra"
)

// TestSamplingFlags: --temperature and --top-p are taken within their ranges
// and refused beside --deterministic or --seed, which fix temperature 0.
func TestSamplingFlags(t *testing.T) {
	cases := [][]string{
		{},
		{"--temperature", "0.7"},
		{"--temperature", "0"},
		{"--temperature", "2"},
		{"--temperature", "2.5"},
		{"--temperature", "-0.1"},
		{"--top-p", "0.9"},
		{"--top-p", "1"},
		{"--top-p", "0"},
		{"--top-p", "1.2"},
		{"--temperature", "0.7", "--deterministic"},
		{"--top-p", "0.9", "--seed", "7"},
		{"--deterministic"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newAgentRunCmd()
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			det, _ := cmd.Flags().GetBool("deterministic")
			temp, _ := cmd.Flags().GetFloat64("temperature")
			topP, _ := cmd.Flags().GetFloat64("top-p")
			setT, setP := cmd.Flags().Changed("temperature"), cmd.Flags().Changed("top-p")
			wantErr := (setT || setP) && (det || cmd.Flags().Changed("seed")) ||
				setT && (temp < 0 || temp > 2) ||
				setP && (topP <= 0 || topP > 1)

			gotT, gotP, err := samplingFlags(cmd, det, temp, topP)
			if (err != nil) != wantErr {
				t.Fatalf("err = %v, want error = %v", err, wantErr)
			}
			if err != nil {
				return
			}
			if (gotT != nil) != setT || gotT != nil && *gotT != temp {
				t.Errorf("temperature = %v, want set=%v value %v", gotT, setT, temp)
			}
			if (gotP != nil) != setP || gotP != nil && *gotP != topP {
				t.Errorf("top_p = %v, want set=%v value %v", gotP, setP, topP)
			}
		})
	}
}

// TestAskPassesSamplingToAgentRun: the agent-run command ask dispatches
// carries --temperature and --top-p exactly as given.
func TestAskPassesSamplingToAgentRun(t *testing.T) {
	foragersAbs, err := filepath.Abs("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_FORAGERS_DIR", foragersAbs)
	t.Setenv("HIVE_MAX_OUTPUT_TOKENS", "")
	t.Chdir(t.TempDir())
	const temp, topP = 0.35, 0.95

	root := &cobra.Command{Use: "chb", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newSwarmAskCmd())
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "q", "--no-dispatch", "--no-eval", "--out", "s.yaml",
		"--temperature", strconv.FormatFloat(temp, 'g', -1, 64), "--top-p", strconv.FormatFloat(topP, 'g', -1, 64)})
	if err := root.Execute(); err != nil {
		t.Fatalf("ask: %v\n%s", err, stderr.String())
	}
	for _, want := range []string{
		shellJoin([]string{"--temperature", strconv.FormatFloat(temp, 'g', -1, 64)}),
		shellJoin([]string{"--top-p", strconv.FormatFloat(topP, 'g', -1, 64)}),
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("printed agent-run command lacks %s:\n%s", want, stderr.String())
		}
	}
}

// TestCheckEndpointModels: preflight asks OPENAI_BASE_URL which models it
// serves, fails a node sent one it lacks, and warns when it lists none.
func TestCheckEndpointModels(t *testing.T) {
	cases := []struct {
		name   string
		served []string // nil: /models answers 404
		model  string
		want   checkLevel
	}{
		{"served", []string{"qwen3.5:4b"}, "qwen3.5:4b", checkPass},
		{"missing", []string{"qwen3.5:4b"}, "ministral-3:8b", checkFail},
		{"unlisted", nil, "ministral-3:8b", checkWarn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.served == nil {
					http.NotFound(w, r)
					return
				}
				data := []map[string]string{}
				for _, m := range c.served {
					data = append(data, map[string]string{"id": m})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer srv.Close()
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			t.Setenv("OPENAI_API_KEY", "k")
			t.Setenv("HIVE_BUDGET_MODE", "")
			t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
			defn := map[string]any{"nodes": map[string]any{"n": map[string]any{"type": "agent", "model": c.model}}}

			r := newPreflightReport()
			r.checkEndpointModels(context.Background(), "openai", "", defn)
			if len(r.results) != 1 || r.results[0].level != c.want {
				t.Fatalf("results = %+v, want one at level %d", r.results, c.want)
			}
			if !strings.Contains(r.results[0].msg, c.model) {
				t.Errorf("message %q does not name the model %q", r.results[0].msg, c.model)
			}
		})
	}
	// --budget-mode resolves a tier node as agent-run --budget-mode would,
	// over HIVE_BUDGET_MODE.
	t.Run("budget mode", func(t *testing.T) {
		cheap := openAITierModel(runner.BudgetCheap)
		premium := openAITierModel(runner.BudgetPremium)
		if cheap == premium {
			t.Fatalf("planner resolves to %q in both modes; the test needs two models", cheap)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": cheap}}})
		}))
		defer srv.Close()
		t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
		t.Setenv("OPENAI_API_KEY", "k")
		t.Setenv("HIVE_BUDGET_MODE", string(runner.BudgetPremium))
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
		defn := map[string]any{"nodes": map[string]any{"n": map[string]any{"type": "agent", "tier": "planner"}}}
		for _, c := range []struct {
			flag string
			want checkLevel
		}{{string(runner.BudgetCheap), checkPass}, {"", checkFail}} {
			r := newPreflightReport()
			r.checkEndpointModels(context.Background(), "openai", c.flag, defn)
			if len(r.results) != 1 || r.results[0].level != c.want {
				t.Fatalf("--budget-mode %q: results = %+v, want one at level %d", c.flag, r.results, c.want)
			}
		}
		// The command passes its --budget-mode flag to that check. Other
		// checks (PATH, target dir) may fail on a test machine, so only the
		// endpoint line is read.
		wf := filepath.Join(t.TempDir(), "wf.yaml")
		if err := os.WriteFile(wf, []byte("name: t\nnodes:\n  n:\n    type: agent\n    tier: planner\n    prompt: \"answer\"\n    outputs: [ok]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			args []string
			sym  string
		}{{[]string{"--budget-mode", string(runner.BudgetCheap)}, "✓"}, {nil, "✗"}} {
			cmd := newPreflightCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append([]string{wf, "--provider", "openai"}, c.args...))
			_ = cmd.Execute()
			line := ""
			for _, l := range strings.Split(out.String(), "\n") {
				if strings.Contains(l, " endpoint models — ") {
					line = l
				}
			}
			if !strings.HasPrefix(strings.TrimSpace(line), c.sym+" ") {
				t.Errorf("chb preflight %v: endpoint line = %q, want %s\n%s", c.args, line, c.sym, out.String())
			}
		}
	})
	t.Run("no OPENAI_BASE_URL, no check", func(t *testing.T) {
		t.Setenv("OPENAI_BASE_URL", "")
		r := newPreflightReport()
		r.checkEndpointModels(context.Background(), "openai", "", map[string]any{"nodes": map[string]any{"n": map[string]any{"model": "x"}}})
		if len(r.results) != 0 {
			t.Fatalf("results = %+v, want none", r.results)
		}
	})
}

// TestCheckConstraintProbes: preflight probes each model a schema'd node
// sends and reports ✓ when the server returned the probe's object in its
// declared key order, ⚠ when the keys came sorted or the reply was not the
// object; a node without a schema is not probed. The ✓ line says it measures
// the server: a node offered tools carries its schema only on a finalize call.
func TestCheckConstraintProbes(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  checkLevel
		says  string
	}{
		{`{"probe":"hive-constraint-ok","order":"second"}`, checkPass, "finalize call"},
		{`{"order":"second","probe":"hive-constraint-ok"}`, checkWarn, "alphabetical order"},
		{"It is sunny.", checkWarn, "checked only after the call"},
	} {
		var probed []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path == "/api/show" {
				// Ollama's capability answer, asked before a reasoning
				// level is sent: it is not a probe.
				_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "thinking"}})
				return
			}
			probed = append(probed, body["model"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": c.reply}}},
			})
		}))
		t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
		t.Setenv("OPENAI_API_KEY", "k")
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
		defn := map[string]any{"nodes": map[string]any{
			"lens":  map[string]any{"type": "agent", "model": "qwen3.5:4b", "reasoning": "none", "output_schema": map[string]any{"type": "object"}},
			"plain": map[string]any{"type": "agent", "model": "ministral-3:8b"},
		}}
		r := newPreflightReport()
		r.checkConstraintProbes(context.Background(), "openai", "", defn)
		srv.Close()
		if len(probed) != 1 || probed[0] != "qwen3.5:4b" {
			t.Errorf("probed %v, want only the schema'd node's model", probed)
		}
		if len(r.results) != 1 || r.results[0].level != c.want || !strings.Contains(r.results[0].msg, "qwen3.5:4b (reasoning none)") || !strings.Contains(r.results[0].msg, "[lens]") || !strings.Contains(r.results[0].msg, c.says) {
			t.Errorf("reply %q: results %+v, want one at level %d naming the model, level and node, and saying %q", c.reply, r.results, c.want, c.says)
		}
	}
}

// openAITierModel is the model the OpenAI backend is sent for the planner
// tier at mode, recomputed from the models config: the tier's slot, or, for
// a slot the config lists as a Claude model, the alias that names it, as the
// OpenAI backend resolves that alias (runner.md § Model tiers and budget
// mode).
func openAITierModel(mode runner.BudgetMode) string {
	cfg := models.Load()
	slot := cfg.ResolveTier("planner", string(mode))
	if cfg.Models[slot].Family != "anthropic" {
		return runner.ResolveOpenAIModel(slot)
	}
	var names []string
	for a, id := range cfg.Aliases {
		if id == slot {
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		return runner.ResolveOpenAIModel(slot)
	}
	sort.Strings(names)
	return runner.ResolveOpenAIModel(names[0])
}

// chb preflight warns, and names the node, when a reasoning level will not be
// sent: a level other than none to a model Ollama reports cannot think. The
// same level to a thinking model passes.
func TestCheckEndpointModels_ReasoningNotSent(t *testing.T) {
	const plain, thinker = "ministral-3:8b", "qwen3.5:4b"
	caps := map[string][]string{plain: {"completion", "tools"}, thinker: {"completion", "tools", "thinking"}}
	for _, model := range []string{plain, thinker} {
		t.Run(model, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": plain}, {"id": thinker}}})
				case "/api/show":
					var req struct{ Model string }
					_ = json.NewDecoder(r.Body).Decode(&req)
					_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps[req.Model]})
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
			t.Setenv("OPENAI_API_KEY", "k")
			t.Setenv("HIVE_BUDGET_MODE", "")
			t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
			defn := map[string]any{"nodes": map[string]any{"lens": map[string]any{"type": "agent", "model": model, "reasoning": "low"}}}

			r := newPreflightReport()
			r.checkEndpointModels(context.Background(), "openai", "", defn)
			thinks := false
			for _, c := range caps[model] {
				thinks = thinks || c == "thinking"
			}
			want := checkPass
			if !thinks {
				want = checkWarn
			}
			if len(r.results) != 1 || r.results[0].level != want {
				t.Fatalf("results = %+v, want one at level %d", r.results, want)
			}
			label := fmt.Sprintf("%q (node lens, reasoning low)", model)
			if named := strings.Contains(r.results[0].msg, label); named == thinks {
				t.Errorf("message %q names %s = %v, want %v", r.results[0].msg, label, named, !thinks)
			}
		})
	}
}
