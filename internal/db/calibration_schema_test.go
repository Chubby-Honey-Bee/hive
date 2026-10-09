package db

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// schemaDump is sqlite_master as one string: what "no change on the second
// open" is checked against.
func schemaDump(t *testing.T, conn *sql.DB) string {
	t.Helper()
	rows, err := conn.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, sqlText string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s %s\n", typ, name, sqlText)
	}
	return b.String()
}

var calibrationObjects = []struct{ typ, name string }{
	{"table", "outcomes"},
	{"table", "calibration_scores"},
	{"table", "calibration_revisions"},
	{"view", "finding_confirmation"},
	{"index", "idx_outcomes_finding"},
	{"index", "idx_outcomes_lens"},
	{"index", "idx_outcomes_run"},
	{"index", "idx_outcomes_coords"},
	{"index", "idx_calibration_scores_kind_scope"},
	{"index", "idx_calibration_revisions_tick_key"},
}

func objectExists(conn *sql.DB, typ, name string) (bool, error) {
	var got string
	err := conn.QueryRow(`SELECT name FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// A fresh database holds the ledger, the scores, the revisions and the
// view, and a second open changes nothing.
func TestInitSchema_CalibrationObjects_FreshTwice(t *testing.T) {
	conn := openSchemaInitMemDB(t)
	if err := initSchema(conn); err != nil {
		t.Fatal(err)
	}
	for _, o := range calibrationObjects {
		ok, err := objectExists(conn, o.typ, o.name)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s %s missing after init", o.typ, o.name)
		}
	}
	before := schemaDump(t, conn)
	if err := initSchema(conn); err != nil {
		t.Fatalf("second init: %v", err)
	}
	if after := schemaDump(t, conn); after != before {
		t.Fatalf("second init changed the schema:\n%s\n---\n%s", before, after)
	}
}

// A database without the ledger gains its tables, the view and their indexes
// when opened; nothing else about it changes.
func TestInitSchema_OldDatabaseGainsCalibrationTables(t *testing.T) {
	conn := openSchemaInitMemDB(t)
	if err := initSchema(conn); err != nil {
		t.Fatal(err)
	}
	// Drop the ledger's objects, so the file holds a database without them.
	// The view goes first; it reads outcomes.
	for _, stmt := range []string{
		`DROP VIEW finding_confirmation`,
		`DROP TABLE calibration_revisions`,
		`DROP TABLE calibration_scores`,
		`DROP TABLE outcomes`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(`INSERT INTO findings (wave, agent, finding) VALUES (1, 'old', 'kept')`); err != nil {
		t.Fatal(err)
	}
	old := schemaDump(t, conn)
	for _, o := range calibrationObjects {
		if ok, _ := objectExists(conn, o.typ, o.name); ok {
			t.Fatalf("%s %s still present after the rollback; the fixture is wrong", o.typ, o.name)
		}
	}

	if err := initSchema(conn); err != nil {
		t.Fatalf("open old database: %v", err)
	}
	for _, o := range calibrationObjects {
		ok, err := objectExists(conn, o.typ, o.name)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s %s not created on an old database", o.typ, o.name)
		}
	}
	// The other objects are untouched: the dump after the open is the dump
	// before it plus the ledger's objects, nothing altered.
	after := schemaDump(t, conn)
	for _, line := range strings.Split(strings.TrimSpace(old), "\n") {
		if !strings.Contains(after, line+"\n") {
			t.Errorf("an existing object changed on open: %s", line)
		}
	}
	var kept int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM findings WHERE finding = 'kept'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("the old row is gone: %d", kept)
	}
}

// The outcomes CHECKs enforce the subject identity.
func TestOutcomes_SubjectIdentityChecks(t *testing.T) {
	s := newTestStore(t)
	fid := mustAddAssumption(t, s, "f")
	runID := mustAddRun(t, s)

	cases := []struct {
		name string
		row  *OutcomeRow
		ok   bool
	}{
		{"finding without label", &OutcomeRow{SubjectKind: "finding", FindingID: nn(fid), Resolution: "confirmed", Source: "human"}, false},
		{"finding without id", &OutcomeRow{SubjectKind: "finding", SubjectLabel: ns("assumption"), Resolution: "confirmed", Source: "human"}, false},
		{"finding", &OutcomeRow{SubjectKind: "finding", FindingID: nn(fid), SubjectLabel: ns("assumption"), Resolution: "confirmed", Source: "human"}, true},
		{"lens verdict without run", &OutcomeRow{SubjectKind: "lens_verdict", Lens: ns("skeptic"), Resolution: "refuted", Source: "human"}, false},
		{"lens verdict", &OutcomeRow{SubjectKind: "lens_verdict", Lens: ns("skeptic"), RunID: nn(runID), Resolution: "refuted", Source: "human"}, true},
		{"synthesis without run", &OutcomeRow{SubjectKind: "synthesis_verdict", Resolution: "partial", Source: "external"}, false},
		{"synthesis", &OutcomeRow{SubjectKind: "synthesis_verdict", RunID: nn(runID), Resolution: "partial", Source: "external"}, true},
		{"bad resolution", &OutcomeRow{SubjectKind: "synthesis_verdict", RunID: nn(runID), Resolution: "maybe", Source: "human"}, false},
		{"bad source", &OutcomeRow{SubjectKind: "synthesis_verdict", RunID: nn(runID), Resolution: "confirmed", Source: "model"}, false},
		{"confidence out of range", &OutcomeRow{SubjectKind: "synthesis_verdict", RunID: nn(runID), Resolution: "confirmed", Source: "human", StatedConfidence: nn(101)}, false},
	}
	for _, c := range cases {
		_, err := s.Outcomes().Add(c.row)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	var n int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM outcomes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("rows written = %d, want 3", n)
	}
	var confirmed int
	if err := s.ReadDB.QueryRow(`SELECT n_confirmed FROM finding_confirmation WHERE finding_id = ?`, fid).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed != 1 {
		t.Fatalf("finding_confirmation n_confirmed = %d, want 1", confirmed)
	}
}

// The ledger reads: by subject, since a calibrate tick, and the high-water
// mark the tick's notes carry.
func TestOutcomesRepo_ListBySubjectAndSince(t *testing.T) {
	s := newTestStore(t)
	fid := mustAddAssumption(t, s, "f")
	runID := mustAddRun(t, s)
	add := func(row *OutcomeRow) int64 {
		id, err := s.Outcomes().Add(row)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id1 := add(&OutcomeRow{SubjectKind: "finding", FindingID: nn(fid), SubjectLabel: ns("assumption"), Resolution: "confirmed", Source: "human"})
	id2 := add(&OutcomeRow{SubjectKind: "lens_verdict", Lens: ns("skeptic"), RunID: nn(runID), Resolution: "refuted", Source: "human"})

	byFinding, err := s.Outcomes().ListBySubject("finding", fid, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byFinding) != 1 || byFinding[0].ID != id1 {
		t.Fatalf("ListBySubject(finding) = %+v", byFinding)
	}
	byLens, err := s.Outcomes().ListBySubject("lens_verdict", 0, "skeptic", runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byLens) != 1 || byLens[0].ID != id2 {
		t.Fatalf("ListBySubject(lens_verdict) = %+v", byLens)
	}
	if _, err := s.Outcomes().ListBySubject("other", 0, "", 0); err == nil {
		t.Fatal("an unknown subject kind was accepted")
	}

	all, err := s.Outcomes().ListSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListSince(0) = %d rows, want 2", len(all))
	}
	maxID, err := s.Outcomes().MaxID()
	if err != nil {
		t.Fatal(err)
	}
	if maxID != id2 {
		t.Fatalf("MaxID = %d, want %d", maxID, id2)
	}
	tick, err := s.TimeWheel().Begin("calibrate-test", TickCalibrate, 0, 0, CalibrateTickNotes(id1))
	if err != nil {
		t.Fatalf("a calibrate tick is refused: %v", err)
	}
	since, err := s.Outcomes().ListSince(tick)
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 1 || since[0].ID != id2 {
		t.Fatalf("ListSince(tick through %d) = %+v, want only %d", id1, since, id2)
	}
	if got, ok := ParseCalibrateTickNotes(CalibrateTickNotes(42)); !ok || got != 42 {
		t.Fatalf("notes round trip = %d, %v", got, ok)
	}
	if _, ok := ParseCalibrateTickNotes("dreamer loop"); ok {
		t.Fatal("notes without the mark parsed as one")
	}
	other, err := s.TimeWheel().Begin("ripen-x", TickRipen, 0, 0, "dreamer loop")
	if err != nil {
		t.Fatal(err)
	}
	sinceOther, err := s.Outcomes().ListSince(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(sinceOther) != 2 {
		t.Fatalf("ListSince(a tick without the mark) = %d rows, want the whole ledger", len(sinceOther))
	}
}

// Scores: upsert replaces every column, delete removes, snapshots append and
// the latest snapshot per key is what LatestRevisions returns.
func TestCalibrationRepo_ScoresAndRevisions(t *testing.T) {
	s := newTestStore(t)
	tick1, err := s.TimeWheel().Begin("calibrate-1", TickCalibrate, 0, 0, CalibrateTickNotes(0))
	if err != nil {
		t.Fatal(err)
	}
	tick2, err := s.TimeWheel().Begin("calibrate-2", TickCalibrate, 0, 0, CalibrateTickNotes(0))
	if err != nil {
		t.Fatal(err)
	}
	key := ScoreKey{PredictorKind: "lens", PredictorKey: "skeptic", ScopeKey: ""}
	row := &ScoreRow{ScoreKey: key, NResolved: 3, NConfirmed: 2, NRefuted: 1, HitRate: 2.0 / 3.0, Weight: 1.1,
		BrierSum: 0.5, BrierN: 2, BrierScore: sql.NullFloat64{Float64: 0.25, Valid: true},
		UpdatedTickID: nn(tick1)}
	if err := s.Calibration().UpsertScore(row); err != nil {
		t.Fatal(err)
	}
	got, err := s.Calibration().GetScore(key)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Same(row) || got.UpdatedTickID != row.UpdatedTickID {
		t.Fatalf("GetScore = %+v, want %+v", got, row)
	}
	if missing, err := s.Calibration().GetScore(ScoreKey{PredictorKind: "lens", PredictorKey: "nobody"}); err != nil || missing != nil {
		t.Fatalf("GetScore(missing) = %+v, %v", missing, err)
	}

	row2 := *row
	row2.NResolved, row2.NConfirmed, row2.HitRate, row2.Calibrated = 12, 11, 11.0/12.0, true
	row2.BrierScore = sql.NullFloat64{}
	row2.UpdatedTickID = nn(tick2)
	if err := s.Calibration().UpsertScore(&row2); err != nil {
		t.Fatal(err)
	}
	got, err = s.Calibration().GetScore(key)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Same(&row2) || !got.Calibrated || got.BrierScore.Valid || got.UpdatedTickID.Int64 != tick2 {
		t.Fatalf("upsert did not replace every column: %+v", got)
	}

	global := ""
	if err := s.Calibration().UpsertScore(&ScoreRow{ScoreKey: ScoreKey{PredictorKind: "label", PredictorKey: "guarantee", ScopeKey: "d1=2"}, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Calibration().ListScores("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].PredictorKind != "label" {
		t.Fatalf("ListScores(all) = %d rows, first %+v; want 2 in key order", len(all), all[0])
	}
	onlyGlobal, err := s.Calibration().ListScores("", &global)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyGlobal) != 1 || onlyGlobal[0].ScopeKey != "" {
		t.Fatalf("ListScores(global) = %+v", onlyGlobal)
	}
	onlyLabel, err := s.Calibration().ListScores("label", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyLabel) != 1 || onlyLabel[0].PredictorKey != "guarantee" {
		t.Fatalf("ListScores(label) = %+v", onlyLabel)
	}

	if err := s.Calibration().SnapshotRevisions(tick1, []*ScoreRow{row}); err != nil {
		t.Fatal(err)
	}
	if err := s.Calibration().SnapshotRevisions(tick2, []*ScoreRow{&row2}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Calibration().CountRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("revisions = %d, want 2", n)
	}
	latest, err := s.Calibration().LatestRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if l := latest[key]; l == nil || l.NResolved != 12 || l.UpdatedTickID.Int64 != tick2 {
		t.Fatalf("LatestRevisions[%v] = %+v, want the tick-2 snapshot", key, l)
	}

	if err := s.Calibration().DeleteScore(key); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Calibration().GetScore(key); got != nil {
		t.Fatalf("row survives DeleteScore: %+v", got)
	}
}

func mustAddAssumption(t *testing.T, s *Store, text string) int64 {
	t.Helper()
	id, err := s.Findings().AddFinding(&Finding{Wave: 1, Agent: "test", MSSLabel: "assumption", Finding: text, SourceURLs: ptr("https://example.com/s")})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustAddRun(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.Workflows().CreateWorkflowRun("calibration-test", 1, "name: t", "{}", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func nn(v int64) sql.NullInt64   { return sql.NullInt64{Int64: v, Valid: true} }
func ns(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
func ptr[T any](v T) *T          { return &v }
