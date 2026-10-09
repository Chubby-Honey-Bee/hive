package cli

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

func useTempStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.NewStore(filepath.Join(t.TempDir(), "calibrate.db"))
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
	return s
}

func runCalibrateCmd(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	cmd.SetArgs(args)
	out, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) })
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", cmd.Name(), args, err, out)
	}
	return out
}

// outcome-record, calibrate, db-read calibration and comb wheel over one
// database: the ledger row, the recompute's JSON, the scores listing and the
// calibrate tick.
func TestCalibrateCLI_RoundTrip(t *testing.T) {
	s := useTempStore(t)
	src := "https://example.com/a"
	a, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "test", MSSLabel: "assumption", Finding: "a", SourceURLs: &src})
	if err != nil {
		t.Fatal(err)
	}
	deps := `[` + itoa(a) + `]`
	g, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "test", MSSLabel: "guarantee", Finding: "g", SourceURLs: &src, DependsOnIDs: &deps})
	if err != nil {
		t.Fatal(err)
	}

	out := runCalibrateCmd(t, newOutcomeRecordCmd(), `{"subject_kind":"finding","finding_id":`+itoa(g)+`,"resolution":"confirmed","stated_confidence":85}`)
	if !strings.Contains(out, "outcome #1: finding "+itoa(g)+" confirmed (human)") {
		t.Fatalf("outcome-record printed %q", out)
	}
	out = runCalibrateCmd(t, newOutcomeRecordCmd(), `{"subject_kind":"finding","finding_id":`+itoa(a)+`,"resolution":"refuted","rationale":"measured otherwise"}`)
	if !strings.Contains(out, "refuted (human); cascade reverted findings "+itoa(g)+" to unknown") {
		t.Fatalf("outcome-record printed %q", out)
	}
	var label string
	if err := s.ReadDB.QueryRow(`SELECT mss_label FROM findings WHERE id = ?`, a).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "assumption" {
		t.Fatalf("the refuted finding's label changed to %s", label)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCalibrateCmd(), "--json")), &res); err != nil {
		t.Fatal(err)
	}
	if res["skipped"] != false || res["tick_id"].(float64) == 0 || res["new_outcomes"].(float64) != 2 {
		t.Fatalf("calibrate --json = %v", res)
	}
	scores := res["scores"].([]any)
	if len(scores) != 2 {
		t.Fatalf("scores = %v, want label/assumption and label/guarantee", scores)
	}
	first := scores[0].(map[string]any)
	if first["predictor_kind"] != "label" || first["predictor_key"] != "assumption" || first["n_refuted"].(float64) != 1 ||
		first["calibrated"] != false || first["brier_score"] != nil || first["scope_key"] != "" {
		t.Fatalf("first score = %v", first)
	}
	second := scores[1].(map[string]any)
	if second["predictor_key"] != "guarantee" || math.Abs(second["brier_score"].(float64)-0.15*0.15) > 1e-12 {
		t.Fatalf("second score = %v", second)
	}
	if len(res["changed"].([]any)) != 2 || res["drift_count"].(float64) != 0 || res["lowest_calibrated_lens"] != "" {
		t.Fatalf("calibrate --json = %v", res)
	}
	if !strings.Contains(res["note"].(string), "correlational") {
		t.Fatalf("note = %v", res["note"])
	}

	text := runCalibrateCmd(t, newCalibrateCmd())
	if !strings.Contains(text, "nothing recomputed") {
		t.Fatalf("a second calibrate with nothing new printed %q", text)
	}
	text = runCalibrateCmd(t, newCalibrateCmd(), "--rebuild")
	if !strings.Contains(text, "scores changed: 0, removed: 0") || !strings.Contains(text, "∇ report: no ∇-positive run") || !strings.Contains(text, "correlational") {
		t.Fatalf("calibrate --rebuild printed %q", text)
	}

	var filtered map[string]any
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCalibrateCmd(), "--rebuild", "--json", "--scope", "d1=9")), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered["scores"].([]any)) != 0 {
		t.Fatalf("--scope d1=9 printed %v", filtered["scores"])
	}

	listing := runCalibrateCmd(t, newReadCalibrationCmd())
	if !strings.Contains(listing, "calibration scores: 2") || !strings.Contains(listing, "uncalibrated (n<10)") || !strings.Contains(listing, "correlational") {
		t.Fatalf("db-read calibration printed %q", listing)
	}
	var read map[string]any
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newReadCalibrationCmd(), "--json", "--kind", "label", "--scope", "")), &read); err != nil {
		t.Fatal(err)
	}
	if len(read["scores"].([]any)) != 2 {
		t.Fatalf("db-read calibration --json = %v", read)
	}
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newReadCalibrationCmd(), "--json", "--scope", "d1=1")), &read); err != nil {
		t.Fatal(err)
	}
	if len(read["scores"].([]any)) != 0 {
		t.Fatalf("db-read calibration --scope d1=1 = %v", read)
	}

	var ticks []map[string]any
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCombWheelCmd(), "--kind", "calibrate", "--json")), &ticks); err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 3 {
		t.Fatalf("comb wheel --kind calibrate lists %d ticks, want 3 (two rebuilds and the first run)", len(ticks))
	}
}

// outcome-import reads an array from a file with source external; an
// unknown key is refused.
func TestCalibrateCLI_ImportAndRefusals(t *testing.T) {
	s := useTempStore(t)
	run, err := s.Workflows().CreateWorkflowRun("t", 1, "name: t", "{}", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "outcomes.json")
	body := `[{"subject_kind":"lens_verdict","lens":"skeptic","run_id":` + itoa(run) + `,"resolution":"confirmed","d1":2},
	          {"subject_kind":"synthesis_verdict","run_id":` + itoa(run) + `,"resolution":"partial"}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runCalibrateCmd(t, newOutcomeImportCmd(), path)
	if strings.Count(out, "(external)") != 2 || !strings.Contains(out, "lens skeptic in run "+itoa(run)+" confirmed") || !strings.Contains(out, "synthesis of run "+itoa(run)+" partial") {
		t.Fatalf("outcome-import printed %q", out)
	}
	all, err := s.Outcomes().ListAll()
	if err != nil || len(all) != 2 || all[0].Source != "external" || all[0].D1.Int64 != 2 {
		t.Fatalf("ledger = %+v, %v", all, err)
	}

	cmd := newOutcomeRecordCmd()
	cmd.SetArgs([]string{`{"subject_kind":"synthesis_verdict","run_id":` + itoa(run) + `,"resolution":"confirmed","mss_label":"guarantee"}`})
	if err := cmd.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "mss_label") {
		t.Fatalf("an unknown key was accepted: %v", err)
	}
	cmd = newOutcomeRecordCmd()
	cmd.SetArgs([]string{`{"subject_kind":"finding","finding_id":999,"resolution":"confirmed"}`})
	if err := cmd.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "no finding with id 999") {
		t.Fatalf("a missing finding was accepted: %v", err)
	}
	if all, _ := s.Outcomes().ListAll(); len(all) != 2 {
		t.Fatalf("a refused outcome left a row: %d", len(all))
	}
}

// A caller's mistake in chb outcome-record exits with status 1, the code
// main.go gives every error, a usage mistake included; it has no other. The
// refusal names the key on stderr, with no usage text after it.
func TestOutcomeRecordCLI_UnknownKeyExitsWithTheUsageCode(t *testing.T) {
	dir := t.TempDir()
	out, err := runChb(t, dir, os.Getenv("HIVE_MODELS_PATH"), nil, "--db", filepath.Join(dir, "hive.db"),
		"outcome-record", `{"subject_kind":"synthesis_verdict","run_id":1,"resolution":"confirmed","source":"external"}`)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, `unknown field "source"`) || strings.Contains(out, "Usage:") {
		t.Fatalf("outcome-record with an unknown key: %v\n%s", err, out)
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
