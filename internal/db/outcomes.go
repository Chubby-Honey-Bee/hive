package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// OutcomeRow is one row of the outcomes ledger: the resolution of one
// subject, recorded once and never updated.
type OutcomeRow struct {
	ID               int64
	SubjectKind      string // finding | lens_verdict | synthesis_verdict
	FindingID        sql.NullInt64
	SubjectLabel     sql.NullString // the finding's mss_label when recorded
	Lens             sql.NullString
	RunID            sql.NullInt64
	BeliefTickID     sql.NullInt64
	D1, D2, D3, D4   sql.NullInt64
	Resolution       string // confirmed | refuted | partial
	StatedConfidence sql.NullInt64
	PredictedValue   sql.NullFloat64
	ActualValue      sql.NullFloat64
	Source           string // human | downstream_run | external
	Rationale        sql.NullString
	EvidenceURLs     sql.NullString
	ResolvedTickID   sql.NullInt64
	ResolvedAt       string
}

// OutcomesRepo owns the outcomes table. It inserts and reads; nothing
// updates or deletes a row, and it writes no other table.
type OutcomesRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newOutcomesRepo binds the two pools.
func newOutcomesRepo(writeDB, readDB *sql.DB) *OutcomesRepo {
	return &OutcomesRepo{writeDB: writeDB, readDB: readDB}
}

const outcomeColumns = `id, subject_kind, finding_id, subject_label, lens, run_id, belief_tick_id,
	d1, d2, d3, d4, resolution, stated_confidence, predicted_value, actual_value,
	source, rationale, evidence_urls, resolved_tick_id, resolved_at`

// Add appends one outcome. The table's CHECK constraints enforce the
// subject identity: a finding names finding_id and subject_label, a lens
// verdict names lens and run_id, a synthesis verdict names run_id.
func (r *OutcomesRepo) Add(o *OutcomeRow) (int64, error) {
	res, err := r.writeDB.Exec(
		`INSERT INTO outcomes (subject_kind, finding_id, subject_label, lens, run_id, belief_tick_id,
			d1, d2, d3, d4, resolution, stated_confidence, predicted_value, actual_value,
			source, rationale, evidence_urls, resolved_tick_id)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.SubjectKind, o.FindingID, o.SubjectLabel, o.Lens, o.RunID, o.BeliefTickID,
		o.D1, o.D2, o.D3, o.D4, o.Resolution, o.StatedConfidence, o.PredictedValue, o.ActualValue,
		o.Source, o.Rationale, o.EvidenceURLs, o.ResolvedTickID,
	)
	if err != nil {
		return 0, fmt.Errorf("add outcome: %w", err)
	}
	return res.LastInsertId()
}

// Get returns one outcome by id, or nil when there is none.
func (r *OutcomesRepo) Get(id int64) (*OutcomeRow, error) {
	rows, err := r.readDB.Query(`SELECT `+outcomeColumns+` FROM outcomes WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get outcome: %w", err)
	}
	defer rows.Close()
	out, err := scanOutcomeRows(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out[0], nil
}

// ListAll returns the whole ledger in id order: the recompute's input.
func (r *OutcomesRepo) ListAll() ([]*OutcomeRow, error) {
	rows, err := r.readDB.Query(`SELECT ` + outcomeColumns + ` FROM outcomes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list outcomes: %w", err)
	}
	defer rows.Close()
	return scanOutcomeRows(rows)
}

// ListBySubject returns the outcomes recorded on one subject, oldest first:
// a finding by findingID, a lens verdict by (lens, runID), a synthesis
// verdict by runID.
func (r *OutcomesRepo) ListBySubject(kind string, findingID int64, lens string, runID int64) ([]*OutcomeRow, error) {
	cond, args, err := subjectFilter(kind, findingID, lens, runID)
	if err != nil {
		return nil, err
	}
	rows, err := r.readDB.Query(`SELECT `+outcomeColumns+` FROM outcomes WHERE subject_kind = ?`+cond+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list outcomes by subject: %w", err)
	}
	defer rows.Close()
	return scanOutcomeRows(rows)
}

// subjectFilter is the condition and arguments that pick out one subject
// of the kind: a finding by findingID, a lens verdict by (lens, runID), a
// synthesis verdict by runID.
func subjectFilter(kind string, findingID int64, lens string, runID int64) (string, []any, error) {
	switch kind {
	case "finding":
		return ` AND finding_id = ?`, []any{kind, findingID}, nil
	case "lens_verdict":
		return ` AND lens = ? AND run_id = ?`, []any{kind, lens, runID}, nil
	case "synthesis_verdict":
		return ` AND run_id = ?`, []any{kind, runID}, nil
	}
	return "", nil, fmt.Errorf("unknown subject kind %q", kind)
}

// ListSince returns the outcomes recorded after the calibrate tick tickID
// read the ledger: those with an id above the high-water mark the tick's
// notes carry (CalibrateTickNotes). A tick id of 0, a missing tick or notes
// without the mark return the whole ledger.
func (r *OutcomesRepo) ListSince(tickID int64) ([]*OutcomeRow, error) {
	through, err := r.highWaterMark(tickID)
	if err != nil {
		return nil, err
	}
	rows, err := r.readDB.Query(`SELECT `+outcomeColumns+` FROM outcomes WHERE id > ? ORDER BY id`, through)
	if err != nil {
		return nil, fmt.Errorf("list outcomes since: %w", err)
	}
	defer rows.Close()
	return scanOutcomeRows(rows)
}

// highWaterMark is the outcome id a calibrate tick's notes carry: 0 for
// tick 0, a missing tick or notes without the mark.
func (r *OutcomesRepo) highWaterMark(tickID int64) (int64, error) {
	if tickID == 0 {
		return 0, nil
	}
	notes, err := r.tickNotes(tickID)
	if err != nil {
		return 0, err
	}
	through, _ := ParseCalibrateTickNotes(notes.String)
	return through, nil
}

// tickNotes reads a tick's notes; a missing tick has none.
func (r *OutcomesRepo) tickNotes(tickID int64) (sql.NullString, error) {
	var notes sql.NullString
	err := r.readDB.QueryRow(`SELECT notes FROM time_wheel WHERE id = ?`, tickID).Scan(&notes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return notes, fmt.Errorf("read tick %d: %w", tickID, err)
	}
	return notes, nil
}

// MaxID returns the highest outcome id, 0 for an empty ledger.
func (r *OutcomesRepo) MaxID() (int64, error) {
	var id sql.NullInt64
	if err := r.readDB.QueryRow(`SELECT MAX(id) FROM outcomes`).Scan(&id); err != nil {
		return 0, fmt.Errorf("max outcome id: %w", err)
	}
	return id.Int64, nil
}

const calibrateTickNotesPrefix = "through_outcome_id="

// CalibrateTickNotes is the notes a calibrate tick carries: the highest
// outcome id its recompute read, so the next run can tell what is new.
func CalibrateTickNotes(throughOutcomeID int64) string {
	return calibrateTickNotesPrefix + strconv.FormatInt(throughOutcomeID, 10)
}

// ParseCalibrateTickNotes reads the mark CalibrateTickNotes wrote. The
// second result is false for notes that carry none.
func ParseCalibrateTickNotes(notes string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(notes), calibrateTickNotesPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func scanOutcomeRows(rows *sql.Rows) ([]*OutcomeRow, error) {
	var out []*OutcomeRow
	for rows.Next() {
		o := &OutcomeRow{}
		if err := rows.Scan(
			&o.ID, &o.SubjectKind, &o.FindingID, &o.SubjectLabel, &o.Lens, &o.RunID, &o.BeliefTickID,
			&o.D1, &o.D2, &o.D3, &o.D4, &o.Resolution, &o.StatedConfidence, &o.PredictedValue, &o.ActualValue,
			&o.Source, &o.Rationale, &o.EvidenceURLs, &o.ResolvedTickID, &o.ResolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan outcome: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
