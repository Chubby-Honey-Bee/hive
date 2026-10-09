package calibration

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// CALIB-1: a confirmed outcome on a guarantee leaves every label and every
// dependency list byte-identical, and the row copies the finding's label
// and coordinates.
func TestRecord_ConfirmedChangesNoLabel(t *testing.T) {
	s := newStore(t)
	d := addFinding(t, s, "definition", ip(1), ip(2), "test")
	g := addFinding(t, s, "guarantee", ip(1), ip(2), "test", d)
	addFinding(t, s, "assumption", ip(1), nil, "test")
	before := labelsDump(t, s)

	rec := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: Confirmed, Source: SourceHuman, StatedConfidence: ip(90)})
	if rec.ID == 0 || len(rec.Reverted) != 0 {
		t.Fatalf("recorded = %+v", rec)
	}
	if after := labelsDump(t, s); after != before {
		t.Fatalf("a confirmed outcome changed a finding:\n%s\n---\n%s", before, after)
	}
	row, err := s.Outcomes().Get(rec.ID)
	if err != nil || row == nil {
		t.Fatalf("Get(%d) = %v, %v", rec.ID, row, err)
	}
	if row.SubjectLabel.String != "guarantee" || row.D1.Int64 != 1 || row.D2.Int64 != 2 || !row.D2.Valid || row.D3.Valid {
		t.Fatalf("the row did not copy the finding's label and coordinates: %+v", row)
	}
	if row.StatedConfidence.Int64 != 90 || row.Source != SourceHuman || row.Resolution != Confirmed {
		t.Fatalf("row = %+v", row)
	}
}

// CALIB-1 and CALIB-2 together: a human refutation of F runs the cascade,
// so the guarantee on F and the guarantee on that reverts to unknown with a
// critical gap each, while F itself keeps its label.
func TestRecord_HumanRefutedCascades(t *testing.T) {
	s := newStore(t)
	f := addFinding(t, s, "assumption", ip(1), nil, "test")
	g := addFinding(t, s, "guarantee", ip(1), nil, "test", f)
	h := addFinding(t, s, "guarantee", ip(1), nil, "test", g)
	bystander := addFinding(t, s, "guarantee", ip(1), nil, "test", addFinding(t, s, "definition", ip(1), nil, "test"))

	rec := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Refuted, Source: SourceHuman})
	if len(rec.Reverted) != 2 || rec.Reverted[0] != g || rec.Reverted[1] != h {
		t.Fatalf("reverted = %v, want [%d %d]", rec.Reverted, g, h)
	}
	if got := labelOf(t, s, g); got != "unknown" {
		t.Fatalf("G = %s, want unknown", got)
	}
	if got := labelOf(t, s, h); got != "unknown" {
		t.Fatalf("H = %s, want unknown", got)
	}
	if got := labelOf(t, s, f); got != "assumption" {
		t.Fatalf("the refuted finding's own label changed to %s; the cascade does not revert its trigger", got)
	}
	if got := labelOf(t, s, bystander); got != "guarantee" {
		t.Fatalf("a guarantee that does not depend on F reverted to %s", got)
	}
	var gaps int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM gaps WHERE agent = 'hive-alarm' AND priority = 'critical'`).Scan(&gaps); err != nil {
		t.Fatal(err)
	}
	if gaps != 2 {
		t.Fatalf("cascade gaps = %d, want 2", gaps)
	}
	var alarms int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM signals WHERE signal_type = 'alarm'`).Scan(&alarms); err != nil {
		t.Fatal(err)
	}
	if alarms != 2 {
		t.Fatalf("alarm signals = %d, want 2", alarms)
	}
}

// CALIB-2: an external refutation cascades as a human one does.
func TestRecord_ExternalRefutedCascades(t *testing.T) {
	s := newStore(t)
	f := addFinding(t, s, "assumption", nil, nil, "test")
	g := addFinding(t, s, "guarantee", nil, nil, "test", f)
	rec := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Refuted, Source: SourceExternal})
	if len(rec.Reverted) != 1 || rec.Reverted[0] != g {
		t.Fatalf("reverted = %v, want [%d]", rec.Reverted, g)
	}
	if got := labelOf(t, s, g); got != "unknown" {
		t.Fatalf("G = %s, want unknown", got)
	}
}

// CALIB-2: a downstream_run refutation writes the row and runs no cascade;
// the adjudication that produces it arms the alarm cascade itself.
func TestRecord_DownstreamRunRefutedDoesNotCascade(t *testing.T) {
	s := newStore(t)
	f := addFinding(t, s, "assumption", nil, nil, "test")
	addFinding(t, s, "guarantee", nil, nil, "test", f)
	before := labelsDump(t, s)

	rec := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Refuted, Source: SourceDownstreamRun})
	if len(rec.Reverted) != 0 {
		t.Fatalf("a downstream_run refutation cascaded: %v", rec.Reverted)
	}
	if after := labelsDump(t, s); after != before {
		t.Fatalf("a downstream_run refutation changed a finding:\n%s\n---\n%s", before, after)
	}
	rows, err := s.Outcomes().ListBySubject(SubjectFinding, f, "", 0)
	if err != nil || len(rows) != 1 || rows[0].Source != SourceDownstreamRun {
		t.Fatalf("ledger rows = %+v, %v", rows, err)
	}
	var gaps int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM gaps`).Scan(&gaps); err != nil {
		t.Fatal(err)
	}
	if gaps != 0 {
		t.Fatalf("gaps = %d, want none", gaps)
	}
}

// A refuted lens or synthesis verdict names no finding, so nothing cascades,
// and a refutation that is only partial cascades nothing either.
func TestRecord_VerdictsAndPartialDoNotCascade(t *testing.T) {
	s := newStore(t)
	f := addFinding(t, s, "assumption", nil, nil, "test")
	addFinding(t, s, "guarantee", nil, nil, "test", f)
	run := addRun(t, s)
	before := labelsDump(t, s)

	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Refuted, Source: SourceHuman, D1: ip(3)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: run, Resolution: Refuted, Source: SourceExternal})
	rec := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Partial, Source: SourceHuman})
	if len(rec.Reverted) != 0 {
		t.Fatalf("a partial outcome cascaded: %v", rec.Reverted)
	}
	if after := labelsDump(t, s); after != before {
		t.Fatalf("labels changed:\n%s\n---\n%s", before, after)
	}
	lens, err := s.Outcomes().ListBySubject(SubjectLensVerdict, 0, "skeptic", run)
	if err != nil || len(lens) != 1 || lens[0].D1.Int64 != 3 {
		t.Fatalf("lens verdict row = %+v, %v", lens, err)
	}
}

// hive.md § Calibration scores capped findings: a human refutation of a
// capped assumption P reverts the guarantee on it, leaves P a capped
// assumption, and P's outcome scores under label/assumption.
func TestRecord_CappedFindingRefutedStaysCapped(t *testing.T) {
	s := newStore(t)
	p := addFinding(t, s, "assumption", ip(4), nil, "test")
	q := addFinding(t, s, "guarantee", ip(4), nil, "test", p)
	// The row hive.ApplyCaps writes for a cap_finding action; written here
	// because internal/hive reaches this package through the gate.
	if _, err := s.WriteDB.Exec(`INSERT OR IGNORE INTO capped_findings (finding_id) VALUES (?)`, p); err != nil {
		t.Fatal(err)
	}
	rec := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: p, Resolution: Refuted, Source: SourceHuman})
	if len(rec.Reverted) != 1 || rec.Reverted[0] != q || labelOf(t, s, q) != "unknown" {
		t.Fatalf("Q did not revert: %+v, %s", rec, labelOf(t, s, q))
	}
	if got := labelOf(t, s, p); got != "assumption" {
		t.Fatalf("P = %s, want assumption", got)
	}
	var capped int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM capped_findings WHERE finding_id = ?`, p).Scan(&capped); err != nil {
		t.Fatal(err)
	}
	if capped != 1 {
		t.Fatalf("P's cap is gone")
	}
	res := mustRecompute(t, s, lenses)
	if r := findScore(res.Scores, KindLabel, "assumption", ""); r == nil || r.NRefuted != 1 {
		t.Fatalf("label/assumption = %+v", r)
	}
}

// Record refuses a malformed outcome before writing anything.
func TestRecord_Validates(t *testing.T) {
	s := newStore(t)
	f := addFinding(t, s, "assumption", nil, nil, "test")
	bad := []Outcome{
		{SubjectKind: "belief", FindingID: f, Resolution: Confirmed, Source: SourceHuman},
		{SubjectKind: SubjectFinding, Resolution: Confirmed, Source: SourceHuman},
		{SubjectKind: SubjectFinding, FindingID: 999, Resolution: Confirmed, Source: SourceHuman},
		{SubjectKind: SubjectLensVerdict, Lens: "skeptic", Resolution: Confirmed, Source: SourceHuman},
		{SubjectKind: SubjectSynthesisVerdict, Resolution: Confirmed, Source: SourceHuman},
		{SubjectKind: SubjectFinding, FindingID: f, Resolution: "maybe", Source: SourceHuman},
		{SubjectKind: SubjectFinding, FindingID: f, Resolution: Confirmed, Source: "model"},
		{SubjectKind: SubjectFinding, FindingID: f, Resolution: Confirmed, Source: SourceHuman, StatedConfidence: ip(101)},
	}
	for _, o := range bad {
		if _, err := Record(s, o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
	all, err := s.Outcomes().ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("a refused outcome left %d rows", len(all))
	}
}

// CALIB-1, statically: nothing in this package writes the findings table.
// Labels change only through the MSS write path, the cascade and the
// dreamer's settle pass, and the cascade is reached through
// store.CascadeRevert.
func TestCalibration_PackageWritesNoFinding(t *testing.T) {
	write := regexp.MustCompile(`(?i)(update\s+findings|insert\s+into\s+findings|delete\s+from\s+findings|mss_label\s*=)`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		if m := write.Find(src); m != nil {
			t.Errorf("%s writes the findings table: %q", name, m)
		}
	}
	if checked == 0 {
		t.Fatal("no source file checked")
	}
}
