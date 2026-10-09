package cli

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// export-graph exits non-zero, and writes nothing, when a table it reads
// cannot be read, rather than write a graph without that table's nodes as
// though it were whole.
func TestExportGraph_FailsWhenATableCannotBeRead(t *testing.T) {
	s := useTestStore(t)
	if _, err := s.WriteDB.Exec(`DROP TABLE wave_gates`); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "graph.json")
	err := runCmd(t, newExportGraphCmd(), "--out", out)
	if err == nil || !strings.Contains(err.Error(), "no such table: wave_gates") {
		t.Fatalf("export-graph with no wave_gates table: err = %v; want the read's error", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("export-graph wrote %s although a read failed (stat: %v)", out, statErr)
	}
}

// A wave's finding count that cannot be read fails the export, which writes
// nothing, rather than export it as 0.
func TestExportGraph_FailsWhenAWaveCountCannotBeRead(t *testing.T) {
	s := useTestStore(t)
	if _, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, finding) VALUES (1, 'a', 'f')`); err != nil {
		t.Fatal(err)
	}
	reads := s.ReadDB
	s.ReadDB = failingDB(t, s.Path, "FROM findings WHERE wave=?")
	t.Cleanup(func() { s.ReadDB = reads })
	out := filepath.Join(t.TempDir(), "graph.json")
	err := runCmd(t, newExportGraphCmd(), "--out", out)
	if !errors.Is(err, errInjected) {
		t.Fatalf("export-graph with a wave count that cannot be read: err = %v; want the read's error", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("export-graph wrote %s although a read failed (stat: %v)", out, statErr)
	}
}

// --compact cuts finding text to at most 120 bytes on a rune boundary, so a
// multi-byte rune that straddles byte 120 never comes out as invalid UTF-8.
func TestCompactFinding_CutsOnARuneBoundary(t *testing.T) {
	var inputs []string
	for pad := 115; pad <= 121; pad++ {
		for _, r := range []string{"é", "€", "𝄞"} {
			inputs = append(inputs, strings.Repeat("a", pad)+strings.Repeat(r, 4))
		}
	}
	inputs = append(inputs, strings.Repeat("a", 120), strings.Repeat("€", 40), "short")

	for _, in := range inputs {
		got := compactFinding(in)
		if len(in) <= 120 {
			if got != in {
				t.Errorf("%q (%d bytes) was changed to %q", in, len(in), got)
			}
			continue
		}
		// The longest prefix of whole runes that fits in 120 bytes.
		want := 0
		for want < len(in) {
			_, size := utf8.DecodeRuneInString(in[want:])
			if want+size > 120 {
				break
			}
			want += size
		}
		if got != in[:want]+"..." {
			t.Errorf("compactFinding(%d bytes) = %q, want the first %d bytes plus \"...\"", len(in), got, want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("compactFinding(%d bytes) is not valid UTF-8: %q", len(in), got)
		}
	}
}

// A node id becomes a Mermaid identifier whatever the agent name holds: a
// space or a bracket is replaced, so the diagram renders.
func TestMermaidID(t *testing.T) {
	cases := map[string]string{
		"agent-researcher": "agent_researcher",
		"researcher wifi":  "researcher_wifi",
		"analyst[1]":       "analyst_1_",
		"finding:42":       "finding_42",
		"wave.1":           "wave_1",
		"42":               "n_42",
		"":                 "n_",
		"already_fine":     "already_fine",
		"quote\"and'apost": "quote_and_apost",
	}
	for in, want := range cases {
		if got := mermaidID(in); got != want {
			t.Errorf("mermaidID(%q) = %q; want %q", in, got, want)
		}
	}
	for in := range cases {
		for _, r := range mermaidID(in) {
			ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				t.Errorf("mermaidID(%q) kept %q, which Mermaid cannot parse in an id", in, r)
			}
		}
	}
}

// The Mermaid export colours each node type from one tone table, a finding
// by its MSS label and a gate by whether it passed.
func TestGraphToMermaid_ColoursNodesByTypeAndLabel(t *testing.T) {
	gate := func(forced bool) map[string]any {
		d := map[string]any{"mss_audit_passed": int64(1), "source_check_passed": int64(1), "conflict_check_passed": int64(1), "agents_completed": int64(1)}
		if forced {
			d["source_check_passed"] = int64(0)
		}
		return d
	}
	nodes := []map[string]any{
		{"id": "wave-1", "type": "wave", "label": "Wave 1", "data": map[string]any{}},
		{"id": "agent-a", "type": "agent", "label": "a", "data": map[string]any{}},
		{"id": "finding-1", "type": "finding", "label": "F1", "data": map[string]any{"mss_label": "assumption"}},
		{"id": "finding-2", "type": "finding", "label": "F2", "data": map[string]any{"mss_label": "guarantee"}},
		{"id": "conflict-1", "type": "conflict", "label": "C1", "data": map[string]any{}},
		{"id": "eval-1", "type": "evaluation", "label": "E1", "data": map[string]any{}},
		{"id": "gate-w1", "type": "gate", "label": "G1", "data": gate(false)},
		{"id": "gate-w2", "type": "gate", "label": "G2", "data": gate(true)},
		{"id": "dim-1", "type": "dimension", "label": "d1", "data": map[string]any{}},
	}
	out := graphToMermaid(map[string]any{"nodes": nodes, "edges": []map[string]any{}})

	fills := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*classDef (\S+) fill:(#[0-9A-Fa-f]{6}),`).FindAllStringSubmatch(out, -1) {
		fills[m[1]] = m[2]
	}
	classOf := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*class (\S+) (\S+)$`).FindAllStringSubmatch(out, -1) {
		classOf[m[1]] = m[2]
	}
	for _, n := range nodes {
		typ, data := n["type"].(string), n["data"].(map[string]any)
		wantClass, wantFill := typ, ""
		switch typ {
		case "finding":
			l := data["mss_label"].(string)
			wantClass, wantFill = "finding_"+l, mss.LabelColors[mss.Label(l)].Solid
		case "gate":
			if data["source_check_passed"] == int64(0) {
				wantClass = "gate_blocked"
			}
		}
		if wantFill == "" {
			for _, n := range mermaidNodeTones {
				if n.Name == wantClass {
					wantFill = n.Fill
				}
			}
			if wantFill == "" {
				t.Fatalf("no tone for node type %s", wantClass)
			}
		}
		id := mermaidID(n["id"].(string))
		if classOf[id] != wantClass {
			t.Errorf("%s: class %q; want %q", id, classOf[id], wantClass)
		}
		if fills[wantClass] != wantFill {
			t.Errorf("%s: classDef %s fills %q; want %q", id, wantClass, fills[wantClass], wantFill)
		}
	}
}
