package calibration

import (
	"strconv"
	"strings"
	"testing"
)

// Adjudicating a conflict writes one refuted outcome on the loser with
// source downstream_run and nothing else: the loser keeps its label, a
// guarantee that depends on it keeps its label (no cascade from the outcome
// path; the alarm cascade is the hive's), and no gap is opened.
func TestAdjudicate_WritesOneDownstreamRefutationAndNoCascade(t *testing.T) {
	s := newStore(t)
	a := addFinding(t, s, "assumption", ip(1), ip(0), "skeptic")
	b := addFinding(t, s, "assumption", ip(1), ip(0), "optimist")
	g := addFinding(t, s, "guarantee", ip(1), ip(0), "skeptic", b)
	if err := s.Conflicts().AddConflict(1, a, b, "they disagree"); err != nil {
		t.Fatal(err)
	}
	before := labelsDump(t, s)

	adj, err := Adjudicate(s, 1, a)
	if err != nil {
		t.Fatalf("Adjudicate: %v", err)
	}
	if adj.WinnerID != a || adj.LoserID != b || adj.OutcomeID == 0 {
		t.Fatalf("adjudication = %+v", adj)
	}
	rows, err := s.Outcomes().ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("outcomes = %d, want one", len(rows))
	}
	row := rows[0]
	if row.Source != SourceDownstreamRun || row.Resolution != Refuted || row.FindingID.Int64 != b ||
		row.SubjectLabel.String != "assumption" || row.D1.Int64 != 1 || row.D2.Int64 != 0 || !row.D2.Valid {
		t.Fatalf("outcome row = %+v", row)
	}
	if !strings.Contains(row.Rationale.String, "conflict 1") || !strings.Contains(row.Rationale.String, "finding "+itoa(a)+" survives") {
		t.Fatalf("rationale = %q", row.Rationale.String)
	}
	if after := labelsDump(t, s); after != before {
		t.Fatalf("the adjudication outcome cascaded:\n%s\n---\n%s", before, after)
	}
	if got := labelOf(t, s, g); got != "guarantee" {
		t.Fatalf("G = %s, want guarantee (the cascade belongs to the hive's alarm)", got)
	}
	var gaps int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM gaps`).Scan(&gaps); err != nil {
		t.Fatal(err)
	}
	if gaps != 0 {
		t.Fatalf("gaps = %d, want none", gaps)
	}
	var winner int64
	if err := s.ReadDB.QueryRow(`SELECT winner_finding_id FROM conflicts WHERE id = 1`).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	if winner != a {
		t.Fatalf("winner = %d, want %d", winner, a)
	}
}

// SetWinner's refusals stand, and a refused adjudication writes no outcome.
func TestAdjudicate_RefusalsWriteNothing(t *testing.T) {
	s := newStore(t)
	a := addFinding(t, s, "assumption", ip(1), nil, "skeptic")
	b := addFinding(t, s, "assumption", ip(1), nil, "optimist")
	c := addFinding(t, s, "assumption", ip(1), nil, "steward")
	if err := s.Conflicts().AddConflict(1, a, b, "they disagree"); err != nil {
		t.Fatal(err)
	}
	if _, err := Adjudicate(s, 1, c); err == nil || !strings.Contains(err.Error(), "not party") {
		t.Fatalf("a stranger won: %v", err)
	}
	if _, err := Adjudicate(s, 9, a); err == nil || !strings.Contains(err.Error(), "no conflict") {
		t.Fatalf("an unknown conflict was adjudicated: %v", err)
	}
	if err := s.Conflicts().Resolve(1, 1, "settled"); err != nil {
		t.Fatal(err)
	}
	if _, err := Adjudicate(s, 1, a); err == nil || !strings.Contains(err.Error(), "already resolved") {
		t.Fatalf("a resolved conflict was adjudicated: %v", err)
	}
	rows, err := s.Outcomes().ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("refusals wrote %d outcomes", len(rows))
	}
}

// The precedence external > human > downstream_run decides a finding's
// confirmation state; within a source the latest row wins; the ledger keeps
// every row.
func TestConfirmationState_Precedence(t *testing.T) {
	s := newStore(t)
	f := addFinding(t, s, "assumption", ip(1), nil, "skeptic")
	g := addFinding(t, s, "assumption", ip(1), nil, "optimist")
	h := addFinding(t, s, "assumption", ip(1), nil, "steward")

	if st, err := ConfirmationState(s, f); err != nil || st != (State{}) {
		t.Fatalf("no outcome: %+v, %v", st, err)
	}

	// downstream_run refuted, then external confirmed: external wins.
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Refuted, Source: SourceDownstreamRun})
	ext := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Confirmed, Source: SourceExternal})
	st, err := ConfirmationState(s, f)
	if err != nil {
		t.Fatal(err)
	}
	if st.Resolution != Confirmed || st.Source != SourceExternal || st.OutcomeID != ext.ID || st.Outcomes != 2 || !st.Reviewed {
		t.Fatalf("external over downstream_run: %+v", st)
	}

	// human refuted after an external confirmation: external still wins,
	// whatever the order.
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Refuted, Source: SourceHuman})
	if st, _ = ConfirmationState(s, f); st.Resolution != Confirmed || st.Source != SourceExternal || st.Outcomes != 3 {
		t.Fatalf("external over a later human: %+v", st)
	}

	// human beats downstream_run, in either order.
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: Confirmed, Source: SourceHuman})
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: Refuted, Source: SourceDownstreamRun})
	if st, _ = ConfirmationState(s, g); st.Resolution != Confirmed || st.Source != SourceHuman || !st.Reviewed {
		t.Fatalf("human over downstream_run: %+v", st)
	}

	// downstream_run alone is a state, not a review; the latest row of one
	// source wins.
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: h, Resolution: Refuted, Source: SourceDownstreamRun})
	last := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: h, Resolution: Partial, Source: SourceDownstreamRun})
	if st, _ = ConfirmationState(s, h); st.Resolution != Partial || st.Source != SourceDownstreamRun || st.OutcomeID != last.ID || st.Reviewed {
		t.Fatalf("latest downstream_run: %+v", st)
	}

	rows, _ := s.Outcomes().ListAll()
	if len(rows) != 7 {
		t.Fatalf("the ledger kept %d rows, want every one of 7", len(rows))
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
