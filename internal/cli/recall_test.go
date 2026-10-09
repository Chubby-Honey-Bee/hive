package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Each match recall returns carries the queen output of the run that asked
// the matched question.
func TestRecall_EachMatchCarriesItsOwnRunsVerdict(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "recall.db"))
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

	verdicts := map[string]string{
		"should we ship the plushie?":   "ship it",
		"is the pricing table current?": "refresh it",
	}
	for q, v := range verdicts {
		inputs, _ := json.Marshal(map[string]string{"question": q})
		res, err := s.WriteDB.Exec(`INSERT INTO workflow_runs (workflow_name, definition_yaml, inputs_json) VALUES ('ask', 'name: ask', ?)`, string(inputs))
		if err != nil {
			t.Fatal(err)
		}
		runID, _ := res.LastInsertId()
		if _, err := s.WriteDB.Exec(`INSERT INTO workflow_node_states (run_id, node_name, node_type, status, rationale) VALUES (?, 'queen', 'agent', 'completed', ?)`, runID, v); err != nil {
			t.Fatal(err)
		}
	}

	embedCmd := newCombEmbedCmd()
	embedCmd.SetArgs([]string{"--questions", "--provider", "stub"})
	embedCmd.SetOut(&bytes.Buffer{})
	if err := embedCmd.Execute(); err != nil {
		t.Fatalf("comb embed --questions: %v", err)
	}

	var out bytes.Buffer
	recall := newSwarmRecallCmd()
	recall.SetArgs([]string{"should we ship the plushie?", "--provider", "stub", "--json", "--top", "5"})
	recall.SetOut(&out)
	if err := recall.Execute(); err != nil {
		t.Fatalf("recall: %v", err)
	}
	var matches []struct {
		Question string `json:"question"`
		Verdict  string `json:"verdict"`
		Report   string `json:"report"`
	}
	if err := json.Unmarshal(out.Bytes(), &matches); err != nil {
		t.Fatalf("decode recall output: %v\n%s", err, out.String())
	}
	if len(matches) != len(verdicts) {
		t.Fatalf("got %d matches, want %d: %s", len(matches), len(verdicts), out.String())
	}
	// These queens wrote prose, not an object, so each text is its report.
	for _, m := range matches {
		if want := verdicts[m.Question]; m.Report != want || m.Verdict != "" {
			t.Errorf("question %q carried verdict %q and report %q, want no verdict and report %q", m.Question, m.Verdict, m.Report, want)
		}
	}

	// The text output carries the same queen output under each match.
	out.Reset()
	recall = newSwarmRecallCmd()
	recall.SetArgs([]string{"should we ship the plushie?", "--provider", "stub", "--top", "5"})
	recall.SetOut(&out)
	if err := recall.Execute(); err != nil {
		t.Fatalf("recall: %v", err)
	}
	lines := strings.Split(out.String(), "\n")
	for q, v := range verdicts {
		found := false
		for i, line := range lines {
			if strings.HasSuffix(line, q) && i+1 < len(lines) && strings.Contains(lines[i+1], "queen: "+v) {
				found = true
			}
		}
		if !found {
			t.Errorf("text output has no %q line under question %q:\n%s", "queen: "+v, q, out.String())
		}
	}
}

// Queen returns one JSON object whose opening is her Markdown report, so
// the first 200 characters of her text show neither her verdict nor her
// call. recall reads her object: from her stored outputs, else from her text,
// fenced or after a preamble. The text line shows the verdict and the
// recommendation, and --json carries the verdict, recommendation and report
// as fields, not her raw text.
func TestRecall_RendersTheQueensObject(t *testing.T) {
	const q = "should we ship the plushie?"
	object := map[string]any{
		"report":         "## Swarm Verdict: the plushie\n\n### The Question\n" + strings.Repeat("Long synthesis. ", 40),
		"convergence":    "medium",
		"verdict":        "conditional",
		"recommendation": "Ship once the  seam test\npasses.",
	}
	raw, _ := json.Marshal(object)
	cases := []struct {
		name, rationale, outputs string
	}{
		{"outputs stored", string(raw), string(raw)},
		{"outputs not stored, fenced text", "Here is my synthesis.\n\n```json\n" + string(raw) + "\n```", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := db.NewStore(filepath.Join(t.TempDir(), "recall.db"))
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

			inputs, _ := json.Marshal(map[string]string{"question": q})
			res, err := s.WriteDB.Exec(`INSERT INTO workflow_runs (workflow_name, definition_yaml, inputs_json) VALUES ('ask', 'name: ask', ?)`, string(inputs))
			if err != nil {
				t.Fatal(err)
			}
			runID, _ := res.LastInsertId()
			var outputs any
			if c.outputs != "" {
				outputs = c.outputs
			}
			if _, err := s.WriteDB.Exec(`INSERT INTO workflow_node_states (run_id, node_name, node_type, status, rationale, outputs_json) VALUES (?, 'queen', 'agent', 'completed', ?, ?)`,
				runID, c.rationale, outputs); err != nil {
				t.Fatal(err)
			}
			embedCmd := newCombEmbedCmd()
			embedCmd.SetArgs([]string{"--questions", "--provider", "stub"})
			embedCmd.SetOut(&bytes.Buffer{})
			if err := embedCmd.Execute(); err != nil {
				t.Fatalf("comb embed --questions: %v", err)
			}

			var out bytes.Buffer
			recall := newSwarmRecallCmd()
			recall.SetArgs([]string{q, "--provider", "stub", "--top", "1"})
			recall.SetOut(&out)
			if err := recall.Execute(); err != nil {
				t.Fatalf("recall: %v", err)
			}
			want := "queen: " + object["verdict"].(string) + " — " + strings.Join(strings.Fields(object["recommendation"].(string)), " ")
			if !strings.Contains(out.String(), want) {
				t.Errorf("text output lacks %q:\n%s", want, out.String())
			}

			out.Reset()
			recall = newSwarmRecallCmd()
			recall.SetArgs([]string{q, "--provider", "stub", "--top", "1", "--json"})
			recall.SetOut(&out)
			if err := recall.Execute(); err != nil {
				t.Fatalf("recall --json: %v", err)
			}
			var matches []struct {
				Verdict        string `json:"verdict"`
				Recommendation string `json:"recommendation"`
				Report         string `json:"report"`
			}
			if err := json.Unmarshal(out.Bytes(), &matches); err != nil || len(matches) != 1 {
				t.Fatalf("--json matches %+v (%v), want one", matches, err)
			}
			m := matches[0]
			if m.Verdict != object["verdict"] || m.Recommendation != object["recommendation"] || m.Report != object["report"] {
				t.Errorf("--json match %+v, want the object's verdict, recommendation and report", m)
			}
		})
	}
}

// recall's queen line goes through truncFor, and queen output is full of
// multi-byte runes, so the cut keeps the longest whole-rune prefix under n
// bytes and never prints invalid UTF-8.
func TestTruncFor_CutsOnARuneBoundary(t *testing.T) {
	s := "ship it — but only after the recall — “with care” ✓ done"
	for n := 2; n <= len(s)+1; n++ {
		got := truncFor(s, n)
		if n >= len(s) {
			if got != s {
				t.Errorf("n=%d: %q, want the whole string", n, got)
			}
			continue
		}
		keep := 0
		for i := range s {
			if i <= n-1 {
				keep = i
			}
		}
		if want := s[:keep] + "…"; got != want {
			t.Errorf("n=%d: %q, want %q", n, got, want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("n=%d: %q is not valid UTF-8", n, got)
		}
	}
}

// The default model is the most recently written one, a same-second tie on
// created_at going to the later write.
func TestResolveActiveModel_MostRecentWriteWins(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "model.db"))
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

	write := func(key, model string) {
		t.Helper()
		if err := s.CombEmbeddings().Upsert(&db.CombEmbeddingRow{VantageKey: key, Model: model, Embedding: []float32{1, 0}}); err != nil {
			t.Fatal(err)
		}
	}
	write("d1=0", "model-a")
	write("d1=1", "model-b")
	if got, _ := resolveActiveModel(""); got != "model-b" {
		t.Errorf("after writing b: active = %q, want model-b", got)
	}
	write("d1=0", "model-a") // rewrite an existing (key, model) row
	if got, _ := resolveActiveModel(""); got != "model-a" {
		t.Errorf("after rewriting a: active = %q, want model-a", got)
	}
}
