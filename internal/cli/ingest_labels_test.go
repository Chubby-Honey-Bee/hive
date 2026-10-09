package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// agents/researcher.md documents the guarantee marker with
// `"depends_on_ids": [1, 2, 3, 4]`, a JSON array, which ingestFinding reads
// as the guarantee's dependencies. Round-trip all four labels through the
// marker path exactly as an agent emits them.
func TestIngestFinding_AllFourLabelsRoundTrip(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "ingest.db"))
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

	marker := func(m map[string]any) []byte { b, _ := json.Marshal(m); return b }
	defID, err := ingestFinding(marker(map[string]any{"wave": 1, "agent": "t", "d1": 0, "mss_label": "definition", "finding": "Tier 1 means parts under $50 each"}))
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	asmID, err := ingestFinding(marker(map[string]any{"wave": 1, "agent": "t", "d1": 0, "mss_label": "assumption", "finding": "RPi4 draws ~6W", "source_urls": "https://example.test/power"}))
	if err != nil {
		t.Fatalf("assumption: %v", err)
	}
	if _, err := ingestFinding(marker(map[string]any{"wave": 1, "agent": "t", "d1": 0, "mss_label": "unknown", "finding": "HackRF supply unclear"})); err != nil {
		t.Fatalf("unknown: %v", err)
	}
	// The documented shape: a bare array.
	gID, err := ingestFinding(marker(map[string]any{"wave": 1, "agent": "t", "d1": 0, "mss_label": "guarantee", "finding": "Tier 1 BOM is $98", "depends_on_ids": []any{defID, asmID}}))
	if err != nil {
		t.Fatalf("guarantee with array depends_on_ids: %v", err)
	}
	var deps string
	if err := s.ReadDB.QueryRow("SELECT depends_on_ids FROM findings WHERE id=?", gID).Scan(&deps); err != nil {
		t.Fatal(err)
	}
	var got []int64
	if err := json.Unmarshal([]byte(deps), &got); err != nil || len(got) != 2 || got[0] != defID || got[1] != asmID {
		t.Fatalf("stored depends_on_ids = %q, want [%d,%d]", deps, defID, asmID)
	}
	// A JSON string of the list is accepted too, for callers that pre-serialise.
	if _, err := ingestFinding(marker(map[string]any{"wave": 1, "agent": "t", "d1": 0, "mss_label": "guarantee", "finding": "string-form deps", "depends_on_ids": "[1]"})); err != nil {
		t.Fatalf("guarantee with string depends_on_ids: %v", err)
	}
}
