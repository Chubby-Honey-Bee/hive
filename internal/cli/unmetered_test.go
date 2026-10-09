package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// seedCallCounts gives a fresh store one run whose nodes carry the given
// cost and call counts, swaps it in as the CLI's store, and returns the
// run id and the database's path.
func seedCallCounts(t *testing.T, nodes map[string][3]int64) (int64, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "totals.db")
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prev := store
	store = s
	t.Cleanup(func() { store = prev })
	var seeds []db.NodeSeed
	for name := range nodes {
		seeds = append(seeds, db.NodeSeed{Name: name, Type: "agent"})
	}
	runID, err := s.Workflows().CreateWorkflowRun("totals", 1, "name: totals", "{}", seeds)
	if err != nil {
		t.Fatal(err)
	}
	for name, n := range nodes {
		if err := s.Workflows().UpdateNodeMetrics(runID, name, 10, 5, n[0], "openai", "", n[1], n[2]); err != nil {
			t.Fatal(err)
		}
	}
	return runID, path
}

// TestRunTotals_UnmeteredIsNotZeroDollars: run-totals reports a run on
// unpriced models as unmetered, not as $0, and a mixed run as both.
func TestRunTotals_UnmeteredIsNotZeroDollars(t *testing.T) {
	// cost_usd is dollars to 4 places for the priced calls: 1234/10000.
	cases := []struct {
		name  string
		nodes map[string][3]int64 // node → cost x10000, metered calls, unmetered calls
		want  string
	}{
		{"unmetered", map[string][3]int64{"local": {0, 0, 4}}, "unmetered"},
		{"metered", map[string][3]int64{"cloud": {1234, 2, 0}}, "0.1234"},
		{"mixed", map[string][3]int64{"local": {0, 0, 4}, "cloud": {1234, 2, 0}}, "0.1234 + unmetered (4 calls)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runID, _ := seedCallCounts(t, c.nodes)
			var metered, unmetered int64
			for _, n := range c.nodes {
				metered += n[1]
				unmetered += n[2]
			}
			want := c.want

			cmd := newRunTotalsCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{fmt.Sprint(runID)})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", out.String(), err)
			}
			if got["cost_usd"] != want || fmt.Sprint(got["unmetered_calls"]) != fmt.Sprint(unmetered) || fmt.Sprint(got["metered_calls"]) != fmt.Sprint(metered) {
				t.Errorf("run-totals = %s, want cost_usd %q with %d metered and %d unmetered calls", out.String(), want, metered, unmetered)
			}

			cmd = newRunTotalsCmd()
			out.Reset()
			cmd.SetOut(&out)
			cmd.SetArgs([]string{fmt.Sprint(runID), "--field", "cost_usd"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(out.String()) != want {
				t.Errorf("--field cost_usd = %q, want %q", strings.TrimSpace(out.String()), want)
			}
		})
	}
}

// TestProofInspect_UnmeteredIsNotZeroDollars: proof-inspect's cost total
// and each node's cost read unmetered for calls to an unpriced model, not
// $0, and a mixed total names both.
func TestProofInspect_UnmeteredIsNotZeroDollars(t *testing.T) {
	// A priced node's cost in 1/10000 USD, and as dollars to 4 places.
	const costX10000, unmeteredCalls = 1234, 4
	priced := fmt.Sprintf("$%d.%04d", costX10000/10000, costX10000%10000)
	cases := []struct {
		name  string
		nodes map[string][3]int64 // node → cost x10000, metered calls, unmetered calls
		total string
	}{
		{"unmetered", map[string][3]int64{"local": {0, 0, unmeteredCalls}}, "unmetered"},
		{"metered", map[string][3]int64{"cloud": {costX10000, 2, 0}}, priced},
		{"mixed", map[string][3]int64{"local": {0, 0, unmeteredCalls}, "cloud": {costX10000, 2, 0}},
			fmt.Sprintf("%s + unmetered (%d calls)", priced, unmeteredCalls)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, path := seedCallCounts(t, c.nodes)
			var out bytes.Buffer
			// The seeded run has no decisions, so the inspection fails its
			// wiring invariant after it has printed the cost sections.
			_ = runProofInspection(&out, io.Discard, path)
			text := out.String()
			_, afterTotal, ok := strings.Cut(text, "── COST TOTAL ──")
			if !ok {
				t.Fatalf("no cost total section:\n%s", text)
			}
			totalSection, perNode, _ := strings.Cut(afterTotal, "── PER-NODE METRICS ──")
			if got := strings.TrimSpace(totalSection); got != c.total {
				t.Errorf("cost total = %q, want %q", got, c.total)
			}
			for name, n := range c.nodes {
				want := priced
				if n[2] > 0 {
					want = "unmetered"
				}
				found := false
				for _, line := range strings.Split(perNode, "\n") {
					f := strings.Fields(line)
					if len(f) >= 5 && f[0] == name {
						found = true
						if got := strings.Join(f[4:], " "); got != want {
							t.Errorf("node %s cost = %q, want %q", name, got, want)
						}
					}
				}
				if !found {
					t.Errorf("no per-node row for %s:\n%s", name, perNode)
				}
			}
		})
	}
}

// TestRunTotals_CountTheConstraintProbes: run-totals and proof-inspect add
// the run's constraint probes, which no node row carries, to its nodes'.
func TestRunTotals_CountTheConstraintProbes(t *testing.T) {
	runID, path := seedCallCounts(t, map[string][3]int64{"cloud": {1234, 2, 0}})
	var in, out, cost, metered, unmetered int64
	if err := store.ReadDB.QueryRow(
		`SELECT SUM(tokens_in), SUM(tokens_out), SUM(cost_usd_x10000), SUM(metered_calls), SUM(unmetered_calls)
		 FROM workflow_node_states WHERE run_id=?`, runID,
	).Scan(&in, &out, &cost, &metered, &unmetered); err != nil {
		t.Fatal(err)
	}
	// One probe to an unpriced model, one to a priced one.
	for _, p := range [][5]int64{{11, 5, 0, 0, 1}, {7, 3, 20, 1, 0}} {
		if err := store.Workflows().AddRunProbeUsage(runID, p[0], p[1], p[2], p[3], p[4]); err != nil {
			t.Fatal(err)
		}
		in, out, cost, metered, unmetered = in+p[0], out+p[1], cost+p[2], metered+p[3], unmetered+p[4]
	}
	dollars := fmt.Sprintf("%d.%04d + unmetered (%d calls)", cost/10000, cost%10000, unmetered)

	cmd := newRunTotalsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{fmt.Sprint(runID)})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", buf.String(), err)
	}
	want := map[string]any{"tokens_in": in, "tokens_out": out, "cost_usd_x10000": cost, "metered_calls": metered, "unmetered_calls": unmetered, "cost_usd": dollars}
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			t.Errorf("run-totals %s = %v, want %v, the nodes' and the probes' together", k, got[k], v)
		}
	}

	buf.Reset()
	_ = runProofInspection(&buf, io.Discard, path)
	_, afterTotal, _ := strings.Cut(buf.String(), "── COST TOTAL ──")
	totalSection, _, _ := strings.Cut(afterTotal, "── PER-NODE METRICS ──")
	if got := strings.TrimSpace(totalSection); got != "$"+dollars {
		t.Errorf("proof-inspect cost total = %q, want %q", got, "$"+dollars)
	}
}

// TestHarnessChildEnv_DropsOnlyTheEndpoint: the self-test harnesses run chb
// without OPENAI_BASE_URL and with everything else.
func TestHarnessChildEnv_DropsOnlyTheEndpoint(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("OPENAI_API_KEY", "local")
	t.Setenv("HIVE_PROVIDER", "openai")
	env := harnessChildEnv()
	have := map[string]bool{}
	for _, kv := range env {
		have[strings.SplitN(kv, "=", 2)[0]] = true
	}
	if have["OPENAI_BASE_URL"] {
		t.Error("OPENAI_BASE_URL reaches the harness's chb")
	}
	for _, k := range []string{"OPENAI_API_KEY", "HIVE_PROVIDER"} {
		if !have[k] {
			t.Errorf("%s dropped", k)
		}
	}
}

// TestPreflight_UnknownNodeProviderFails: chb preflight fails a provider:
// no backend has, as agent-run refuses it.
func TestPreflight_UnknownNodeProviderFails(t *testing.T) {
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	for _, p := range []string{"ollama", "openai"} {
		r := newPreflightReport()
		r.checkProviderAuth("", map[string]any{"nodes": map[string]any{"n": map[string]any{"type": "agent", "provider": p}}})
		failed := false
		for _, res := range r.results {
			if res.name == "per-node provider" && res.level == checkFail && strings.Contains(res.msg, fmt.Sprintf("provider %q is not a known provider", p)) {
				failed = true
			}
		}
		if failed != (p == "ollama") {
			t.Errorf("provider %q: per-node provider failure = %v, results %+v", p, failed, r.results)
		}
	}
}
