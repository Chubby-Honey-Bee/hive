package calibration

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Outcome is one resolution to record. A finding outcome names FindingID;
// its coordinates and subject label are copied from the finding. A lens
// verdict names Lens and RunID, a synthesis verdict RunID; their
// coordinates, when known, are given here.
type Outcome struct {
	SubjectKind      string
	FindingID        int64
	Lens             string
	RunID            int64
	BeliefTickID     int64
	D1, D2, D3, D4   *int
	Resolution       string
	StatedConfidence *int
	PredictedValue   *float64
	ActualValue      *float64
	Source           string
	Rationale        string
	EvidenceURLs     string
	ResolvedTickID   int64
}

// ErrInvalid marks an outcome Record refuses for what it says: a subject
// it cannot identify, a resolution, source or confidence outside the
// ledger's sets, or a finding that does not exist. A surface answers it as
// the caller's error; any other error is the store's.
var ErrInvalid = errors.New("invalid outcome")

// Recorded is what Record did: the ledger row, and the findings the
// cascade reverted (CALIB-2), if it ran.
type Recorded struct {
	ID       int64
	Reverted []int64
}

// Record appends one outcome to the ledger.
//
// CALIB-1: it writes no finding's mss_label. CALIB-2: a `refuted` outcome
// on a finding from `human` or `external` calls store.CascadeRevert, so
// every finding that depends on it reverts to `unknown` through the
// existing cascade; the refuted finding itself keeps its label. A
// `downstream_run` refutation does not call it: the adjudication that wrote
// it arms the alarm cascade already (hive.md).
func Record(store *db.Store, o Outcome) (Recorded, error) {
	if err := validateOutcome(o); err != nil {
		return Recorded{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	row := outcomeRow(o)
	if err := attachFinding(store, o, row); err != nil {
		return Recorded{}, err
	}
	id, err := store.Outcomes().Add(row)
	if err != nil {
		return Recorded{}, err
	}
	return revertDependents(store, o, Recorded{ID: id})
}

// outcomeRow is o's ledger row, before a finding outcome takes its
// finding's label and coordinates.
func outcomeRow(o Outcome) *db.OutcomeRow {
	return &db.OutcomeRow{
		SubjectKind:      o.SubjectKind,
		Resolution:       o.Resolution,
		Source:           o.Source,
		Lens:             nullString(o.Lens),
		RunID:            nullInt64(o.RunID),
		BeliefTickID:     nullInt64(o.BeliefTickID),
		ResolvedTickID:   nullInt64(o.ResolvedTickID),
		Rationale:        nullString(o.Rationale),
		EvidenceURLs:     nullString(o.EvidenceURLs),
		D1:               nullIntPtr(o.D1),
		D2:               nullIntPtr(o.D2),
		D3:               nullIntPtr(o.D3),
		D4:               nullIntPtr(o.D4),
		StatedConfidence: nullIntPtr(o.StatedConfidence),
		PredictedValue:   nullFloat64Ptr(o.PredictedValue),
		ActualValue:      nullFloat64Ptr(o.ActualValue),
	}
}

// attachFinding copies a finding outcome's subject into row: the finding,
// its label and its coordinates. A finding that does not exist is
// ErrInvalid. Any other outcome is left as it is.
func attachFinding(store *db.Store, o Outcome, row *db.OutcomeRow) error {
	if o.SubjectKind != SubjectFinding {
		return nil
	}
	var label string
	var d1, d2, d3, d4 sql.NullInt64
	err := store.ReadDB.QueryRow(
		`SELECT mss_label, d1, d2, d3, d4 FROM findings WHERE id = ?`, o.FindingID,
	).Scan(&label, &d1, &d2, &d3, &d4)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no finding with id %d", ErrInvalid, o.FindingID)
	}
	if err != nil {
		return fmt.Errorf("read finding %d: %w", o.FindingID, err)
	}
	row.FindingID = sql.NullInt64{Int64: o.FindingID, Valid: true}
	row.SubjectLabel = sql.NullString{String: label, Valid: true}
	row.D1, row.D2, row.D3, row.D4 = d1, d2, d3, d4
	return nil
}

// revertDependents runs the cascade (CALIB-2) when recording o calls for
// it, and adds the findings it reverted to rec.
func revertDependents(store *db.Store, o Outcome, rec Recorded) (Recorded, error) {
	if !runsCascade(o) {
		return rec, nil
	}
	reverted, err := store.CascadeRevert(o.FindingID)
	if err != nil {
		return rec, fmt.Errorf("outcome %d recorded; cascade from finding %d: %w", rec.ID, o.FindingID, err)
	}
	rec.Reverted = reverted
	return rec, nil
}

// runsCascade reports whether recording o runs the cascade: a refutation
// of a finding from a source that cascades.
func runsCascade(o Outcome) bool {
	return o.SubjectKind == SubjectFinding && o.Resolution == Refuted && cascades(o.Source)
}

// cascades reports whether a refutation from source runs the cascade:
// human and external do, downstream_run does not (CALIB-2).
func cascades(source string) bool {
	return source == SourceHuman || source == SourceExternal
}

// validateOutcome refuses an outcome for what it says: the first of its
// subject, resolution, source and stated confidence that is outside the
// ledger's sets.
func validateOutcome(o Outcome) error {
	for _, check := range []func(Outcome) error{checkSubject, checkResolution, checkSource, checkConfidence} {
		if err := check(o); err != nil {
			return err
		}
	}
	return nil
}

// checkSubject refuses a subject kind outside the three, and an outcome
// that does not name what its kind needs.
func checkSubject(o Outcome) error {
	switch o.SubjectKind {
	case SubjectFinding:
		return need(o.FindingID != 0, "a finding outcome needs finding_id")
	case SubjectLensVerdict:
		return need(namesLensAndRun(o), "a lens_verdict outcome needs lens and run_id")
	case SubjectSynthesisVerdict:
		return need(o.RunID != 0, "a synthesis_verdict outcome needs run_id")
	}
	return fmt.Errorf("subject_kind must be finding, lens_verdict or synthesis_verdict, got %q", o.SubjectKind)
}

// namesLensAndRun reports whether o names both a lens and a run.
func namesLensAndRun(o Outcome) bool {
	return o.Lens != "" && o.RunID != 0
}

// need is nil when named holds, else the refusal msg.
func need(named bool, msg string) error {
	if named {
		return nil
	}
	return errors.New(msg)
}

func checkResolution(o Outcome) error {
	switch o.Resolution {
	case Confirmed, Refuted, Partial:
		return nil
	}
	return fmt.Errorf("resolution must be confirmed, refuted or partial, got %q", o.Resolution)
}

func checkSource(o Outcome) error {
	switch o.Source {
	case SourceHuman, SourceDownstreamRun, SourceExternal:
		return nil
	}
	return fmt.Errorf("source must be human, downstream_run or external, got %q", o.Source)
}

func checkConfidence(o Outcome) error {
	if c := o.StatedConfidence; c != nil && (*c < 0 || *c > 100) {
		return fmt.Errorf("stated_confidence must be between 0 and 100, got %d", *c)
	}
	return nil
}

func nullFloat64Ptr(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: v != 0}
}

func nullIntPtr(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}
