package runner

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// The stored rationale was truncate(text, 64000): 64,000 bytes plus a 3-byte
// ellipsis, cut at a fixed byte that could split a rune. chb_node_rationale
// promises at most 64,000 bytes.
func TestRecordCompletion_CapsRationaleAt64000BytesOnARuneBoundary(t *testing.T) {
	const capN = 64000
	const nodeName = "long"
	yamlStr := "name: cap\nnodes:\n  long:\n    type: agent\n    model: haiku\n    prompt: p\n"

	for _, tc := range []struct {
		name, text string
	}{
		{"ascii", strings.Repeat("a", capN+10)},
		{"exact", strings.Repeat("a", capN)},
		{"euro straddles the cut", strings.Repeat("a", capN-4) + strings.Repeat("€", 10)},
		{"four-byte runes", strings.Repeat("𝄞", capN/4+5)},
	} {
		store := newTempStore(t)
		runID := createCompleteRun(t, store, nodeName, yamlStr)
		defn, _ := workflow.LoadYAMLString(yamlStr)
		rc := buildCompleteRC(t, store, runID, defn, &stubBackend{})
		rc.recordCompletion(workflow.DispatchNode{Node: nodeName, Model: "haiku"}, tc.text)

		var got string
		if err := store.ReadDB.QueryRow(
			`SELECT COALESCE(rationale,'') FROM workflow_node_states WHERE run_id=? AND node_name=?`,
			runID, nodeName).Scan(&got); err != nil {
			t.Fatal(err)
		}

		want := tc.text
		if len(tc.text) > capN {
			// The longest prefix of whole runes that leaves room for "…".
			room := capN - len("…")
			keep := 0
			for keep < len(tc.text) {
				_, size := utf8.DecodeRuneInString(tc.text[keep:])
				if keep+size > room {
					break
				}
				keep += size
			}
			want = tc.text[:keep] + "…"
		}
		if got != want {
			t.Errorf("%s: stored %d bytes, want %d", tc.name, len(got), len(want))
		}
		if len(got) > capN || !utf8.ValidString(got) {
			t.Errorf("%s: stored %d bytes (valid UTF-8 %v), want at most %d valid bytes",
				tc.name, len(got), utf8.ValidString(got), capN)
		}
	}
}
