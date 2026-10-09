package mcp

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func newCalibrationTestServer(t *testing.T, buf *bytes.Buffer) (*mcpServer, *db.Store) {
	t.Helper()
	store, err := db.NewStore(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := newTestServer(buf)
	s.store = store
	return s, store
}

// chb_outcome_record writes with source human through the CLI's shape: a
// finding outcome copies the finding's label, a refuted finding runs the
// cascade over its dependents and keeps its own label, and a key outside
// the shape is refused before anything is written.
func TestOutcomeRecord_RecordsAsHumanAndRefusesUnknownKeys(t *testing.T) {
	var buf bytes.Buffer
	s, store := newCalibrationTestServer(t, &buf)
	src := "https://example.com/a"
	a, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "t", MSSLabel: "assumption", Finding: "a", SourceURLs: &src})
	if err != nil {
		t.Fatal(err)
	}
	deps := `[` + itoa(a) + `]`
	g, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "t", MSSLabel: "guarantee", Finding: "g", SourceURLs: &src, DependsOnIDs: &deps})
	if err != nil {
		t.Fatal(err)
	}

	text, isErr, rpcErr := callTool(t, s, &buf, "chb_outcome_record",
		map[string]any{"subject_kind": "finding", "finding_id": float64(g), "resolution": "confirmed", "stated_confidence": float64(90)})
	if rpcErr != nil || isErr {
		t.Fatalf("confirmed: %v %s", rpcErr, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("result %q: %v", text, err)
	}
	if out["id"].(float64) != 1 || out["source"] != "human" || len(out["reverted"].([]any)) != 0 {
		t.Fatalf("result = %v", out)
	}
	row, _ := store.Outcomes().Get(1)
	if row == nil || row.Source != "human" || row.SubjectLabel.String != "guarantee" || row.StatedConfidence.Int64 != 90 {
		t.Fatalf("row = %+v", row)
	}

	text, isErr, rpcErr = callTool(t, s, &buf, "chb_outcome_record",
		map[string]any{"subject_kind": "finding", "finding_id": float64(a), "resolution": "refuted"})
	if rpcErr != nil || isErr {
		t.Fatalf("refuted: %v %s", rpcErr, text)
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if rev := out["reverted"].([]any); len(rev) != 1 || rev[0].(float64) != float64(g) {
		t.Fatalf("reverted = %v, want [%d]", out["reverted"], g)
	}
	var label string
	store.ReadDB.QueryRow(`SELECT mss_label FROM findings WHERE id = ?`, a).Scan(&label)
	if label != "assumption" {
		t.Fatalf("the refuted finding's label changed to %s", label)
	}

	text, isErr, rpcErr = callTool(t, s, &buf, "chb_outcome_record",
		map[string]any{"subject_kind": "finding", "finding_id": float64(a), "resolution": "confirmed", "source": "external"})
	if rpcErr != nil || !isErr || !strings.Contains(text, `"source"`) {
		t.Fatalf("a key outside the shape was taken: %v %s", rpcErr, text)
	}
	text, isErr, rpcErr = callTool(t, s, &buf, "chb_outcome_record",
		map[string]any{"subject_kind": "finding", "finding_id": float64(999), "resolution": "confirmed"})
	if rpcErr != nil || !isErr || !strings.Contains(text, "no finding with id 999") {
		t.Fatalf("a missing finding was taken: %v %s", rpcErr, text)
	}
	_, _, rpcErr = callTool(t, s, &buf, "chb_outcome_record",
		map[string]any{"subject_kind": "finding", "finding_id": float64(a), "resolution": "maybe"})
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("a resolution outside the enum was not refused by the schema: %+v", rpcErr)
	}
	rows, _ := store.Outcomes().ListAll()
	if len(rows) != 2 {
		t.Fatalf("ledger holds %d rows, want the two accepted", len(rows))
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// chb_outcome_record tells the caller's mistake from the store's failure. A
// key outside the shape and a finding that does not exist are tool errors
// the caller can fix; an outcome the store cannot write is protocol error
// -32603.
func TestOutcomeRecord_TellsARefusalFromAStoreFailure(t *testing.T) {
	var buf bytes.Buffer
	s, store := newCalibrationTestServer(t, &buf)
	refusals := []map[string]any{
		{"subject_kind": "synthesis_verdict", "run_id": float64(1), "resolution": "confirmed", "source": "external"},
		{"subject_kind": "finding", "finding_id": float64(999), "resolution": "confirmed"},
	}
	for _, args := range refusals {
		text, isErr, rpcErr := callTool(t, s, &buf, "chb_outcome_record", args)
		if rpcErr != nil || !isErr {
			t.Fatalf("%v: rpc=%+v isError=%v text=%q; want a tool error", args, rpcErr, isErr, text)
		}
	}

	if _, err := store.WriteDB.Exec(`DROP TABLE outcomes`); err != nil {
		t.Fatal(err)
	}
	text, isErr, rpcErr := callTool(t, s, &buf, "chb_outcome_record",
		map[string]any{"subject_kind": "synthesis_verdict", "run_id": float64(1), "resolution": "confirmed"})
	if rpcErr == nil || rpcErr.Code != -32603 || !strings.Contains(rpcErr.Message, "no such table: outcomes") || isErr {
		t.Fatalf("a store that cannot write: rpc=%+v isError=%v text=%q; want -32603 naming the failure", rpcErr, isErr, text)
	}
}

// chb_calibration_read lists the scores with the correlational note, and
// filters by kind and by a given scope.
func TestCalibrationRead_ListsAndFilters(t *testing.T) {
	var buf bytes.Buffer
	s, store := newCalibrationTestServer(t, &buf)
	text, isErr, rpcErr := callTool(t, s, &buf, "chb_calibration_read", map[string]any{})
	if rpcErr != nil || isErr {
		t.Fatalf("empty: %v %s", rpcErr, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out["scores"].([]any)) != 0 || !strings.Contains(out["note"].(string), "correlational") {
		t.Fatalf("empty database: %v", out)
	}

	src := "https://example.com/a"
	d1 := 2
	for i := 0; i < 3; i++ {
		id, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "t", MSSLabel: "assumption", Finding: "a" + itoa(int64(i)), SourceURLs: &src, D1: &d1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := calibration.Record(store, calibration.Outcome{SubjectKind: calibration.SubjectFinding, FindingID: id, Resolution: calibration.Confirmed, Source: calibration.SourceHuman}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := calibration.Recompute(store, calibration.Options{}); err != nil {
		t.Fatal(err)
	}
	text, _, _ = callTool(t, s, &buf, "chb_calibration_read", map[string]any{"kind": "label"})
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	scores := out["scores"].([]any)
	if len(scores) != 1 || scores[0].(map[string]any)["predictor_key"] != "assumption" || scores[0].(map[string]any)["calibrated"] != false {
		t.Fatalf("kind=label: %v", scores)
	}
	text, _, _ = callTool(t, s, &buf, "chb_calibration_read", map[string]any{"scope": "d1=2"})
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out["scores"].([]any)) != 0 {
		t.Fatalf("three outcomes score no d1=2 prefix, yet: %v", out["scores"])
	}
	if _, _, rpcErr := callTool(t, s, &buf, "chb_calibration_read", map[string]any{"kind": "oracle"}); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("a kind outside the enum: %+v", rpcErr)
	}
}
