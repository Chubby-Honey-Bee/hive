package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// calibration-export writes counts and no weights; calibration-merge of
// two workspaces' files prints the summed scores and stores nothing, and
// refuses a file of another shape.
func TestCalibrationExportMerge_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := "https://example.com/a"
	seed := func(s *db.Store, n int) {
		t.Helper()
		d1, conf := 2, 75
		for i := 0; i < n; i++ {
			id, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "t", MSSLabel: "assumption", Finding: "a" + strings.Repeat("x", i+1), SourceURLs: &src, D1: &d1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := calibration.Record(s, calibration.Outcome{SubjectKind: calibration.SubjectFinding, FindingID: id, Resolution: calibration.Confirmed, Source: calibration.SourceHuman, StatedConfidence: &conf}); err != nil {
				t.Fatal(err)
			}
		}
	}

	a := useTempStore(t)
	seed(a, 6)
	fileA := filepath.Join(dir, "a.json")
	out := runCalibrateCmd(t, newCalibrationExportCmd(), "--out", fileA)
	if !strings.Contains(out, "6 outcomes") {
		t.Fatalf("export printed %q", out)
	}
	rawA, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"weight"`, `"hit_rate"`, `"calibrated"`} {
		if strings.Contains(string(rawA), forbidden) {
			t.Fatalf("the export carries %s", forbidden)
		}
	}
	stdout := runCalibrateCmd(t, newCalibrationExportCmd())
	var onStdout calibration.Bundle
	if err := json.Unmarshal([]byte(stdout), &onStdout); err != nil || onStdout.Outcomes != 6 || onStdout.Format != calibration.BundleFormat {
		t.Fatalf("export to stdout = %q (%v)", stdout, err)
	}

	b := useTempStore(t)
	seed(b, 5)
	fileB := filepath.Join(dir, "b.json")
	runCalibrateCmd(t, newCalibrationExportCmd(), "--out", fileB)

	var merged map[string]any
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCalibrationMergeCmd(), "--json", fileA, fileB)), &merged); err != nil {
		t.Fatal(err)
	}
	scores := merged["scores"].([]any)
	// label/assumption in '' and in d1=2: 11 outcomes, calibrated over the
	// union where neither workspace is alone.
	if len(scores) != 2 {
		t.Fatalf("merged scores = %v", scores)
	}
	for _, raw := range scores {
		s := raw.(map[string]any)
		if s["predictor_key"] != "assumption" || s["n_resolved"].(float64) != 11 || s["calibrated"] != true {
			t.Fatalf("merged row = %v", s)
		}
	}
	if srcs := merged["sources"].([]any); len(srcs) != 2 || !strings.Contains(merged["note"].(string), "correlational") {
		t.Fatalf("merged = %v", merged)
	}
	text := runCalibrateCmd(t, newCalibrationMergeCmd(), fileB, fileA)
	if !strings.Contains(text, "merged calibration scores over 2 workspaces: 2") || !strings.Contains(text, "n=11") || !strings.Contains(text, "not stored") {
		t.Fatalf("merge printed %q", text)
	}
	rows, err := b.Calibration().ListScores("", nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("the merge wrote %d scores to the workspace (%v)", len(rows), err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"format":"something-else","counts":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := newCalibrationMergeCmdErr(fileA, bad); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("another format merged: %v", err)
	}
}

func newCalibrationMergeCmdErr(args ...string) error {
	cmd := newCalibrationMergeCmd()
	cmd.SetArgs(args)
	cmd.SetOut(os.Stderr)
	return cmd.Execute()
}
