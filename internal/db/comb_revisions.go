package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// CombRevisionRow is one immutable revision of an Comb vantage. Append-
// only; the chronomantic substrate that lets Queen (the
// synthesizer) read what the Comb believed at any prior tick.
//
// The chain of revisions for one vantage_key is its history of
// "actual occasions" in Whitehead's process-philosophy sense — each
// revision prehends (actively grasps) the prior one and the current
// world state.
type CombRevisionRow struct {
	ID                 int64
	VantageKey         string
	VantageKind        VantageKind
	TickID             sql.NullInt64
	Narrative          string
	Confidence         int
	Contested          bool
	DominantLabel      sql.NullString
	EvidenceCount      int
	OpenQuestionsCount int
	RawJSON            sql.NullString
	Source             string
	RevisionAt         string
}

// dbReader is the minimal read interface used by CombRevisionsRepo.
// *sql.DB satisfies this; tests may substitute a stand-in to inject
// query failures without closing the shared connection pool.
type dbReader interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

// CombRevisionsRepo owns the comb_revisions table — the Comb's full
// chronomantic history.
type CombRevisionsRepo struct {
	writeDB *sql.DB
	readDB  dbReader
}

// newCombRevisionsRepo binds the write pool and the reader.
func newCombRevisionsRepo(writeDB *sql.DB, readDB dbReader) *CombRevisionsRepo {
	return &CombRevisionsRepo{writeDB: writeDB, readDB: readDB}
}

// Append writes one immutable revision row. Returns the new id.
// `source` is a short tag identifying who caused the revision
// (e.g. "comb.refresh", "forager:optimist", "dreamer.prune", "manual").
// `tickID` may be 0 when the caller can't or doesn't want to anchor
// the revision to a Time Wheel tick; chronomantic queries fall back
// to revision_at in that case.
func (r *CombRevisionsRepo) Append(row *CombRevisionRow, source string) (int64, error) {
	if row.VantageKind == "" {
		row.VantageKind = VantageRegion
	}
	if source == "" {
		source = "unknown"
	}
	res, err := r.writeDB.Exec(
		`INSERT INTO comb_revisions
		 (vantage_key, vantage_kind, tick_id,
		  narrative, confidence, contested, dominant_label,
		  evidence_count, open_questions_count, raw_json, source)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		row.VantageKey, string(row.VantageKind),
		nullableInt64(row.TickID),
		row.Narrative, row.Confidence, boolToInt(row.Contested),
		nullableString(row.DominantLabel),
		row.EvidenceCount, row.OpenQuestionsCount,
		nullableString(row.RawJSON),
		source,
	)
	if err != nil {
		return 0, fmt.Errorf("append comb_revisions: %w", err)
	}
	return res.LastInsertId()
}

// At returns the revision of `vantageKey` that was current at the
// instant `at` (a SQLite-format timestamp, e.g. "2026-04-01 12:00:00").
// "Current at" means: the latest revision whose revision_at ≤ at.
// Returns (nil, nil) when no revision predates `at`.
//
// Bounded probe — uses the (vantage_key, revision_at DESC) index.
func (r *CombRevisionsRepo) At(vantageKey, at string) (*CombRevisionRow, error) {
	row := r.readDB.QueryRow(
		`SELECT id, vantage_key, vantage_kind, tick_id,
		        narrative, confidence, contested, dominant_label,
		        evidence_count, open_questions_count, raw_json,
		        source, revision_at
		 FROM comb_revisions
		 WHERE vantage_key = ? AND datetime(revision_at) <= datetime(?)
		 ORDER BY revision_at DESC, id DESC LIMIT 1`,
		vantageKey, at,
	)
	return scanRevisionRow(row)
}

// AtTick returns the revision of `vantageKey` that was current at the
// given Time Wheel tick. Picks the latest revision whose tick_id == tickID
// (if any), else falls back to the latest revision written before the tick
// opened (beforeTick). The tick's started_at is looked up from time_wheel.
func (r *CombRevisionsRepo) AtTick(vantageKey string, tickID int64) (*CombRevisionRow, error) {
	row := r.readDB.QueryRow(
		`SELECT id, vantage_key, vantage_kind, tick_id,
		        narrative, confidence, contested, dominant_label,
		        evidence_count, open_questions_count, raw_json,
		        source, revision_at
		 FROM comb_revisions
		 WHERE vantage_key = ? AND tick_id = ?
		 ORDER BY revision_at DESC, id DESC LIMIT 1`,
		vantageKey, tickID,
	)
	out, err := scanRevisionRow(row)
	if err != nil || out != nil {
		return out, err
	}
	startedAt, err := r.tickStartedAt(tickID)
	if err != nil {
		return nil, err
	}
	return r.beforeTick(vantageKey, tickID, startedAt)
}

// beforeTick is the latest revision of `vantageKey` written before tick
// tickID opened, which started at startedAt. The tick decides where it can:
// ticks open in id order and a revision is anchored only to an open tick, so
// a revision anchored to a later tick was written after this one opened,
// whatever its timestamp, and never counts. A revision anchored to no tick,
// or to an earlier one (a concurrent run's, or a resumed run's reopened
// tick, can take revisions after this tick opens), is placed by its
// timestamp: it counts when written at or before startedAt. Both are
// CURRENT_TIMESTAMP values, to the second, so such a revision written in the
// tick's own second counts as before it. datetime() compares them whichever
// form the driver hands back (RFC3339 when scanned into a string).
func (r *CombRevisionsRepo) beforeTick(vantageKey string, tickID int64, startedAt string) (*CombRevisionRow, error) {
	return scanRevisionRow(r.readDB.QueryRow(
		`SELECT id, vantage_key, vantage_kind, tick_id,
		        narrative, confidence, contested, dominant_label,
		        evidence_count, open_questions_count, raw_json,
		        source, revision_at
		 FROM comb_revisions
		 WHERE vantage_key = ? AND (tick_id IS NULL OR tick_id < ?)
		   AND datetime(revision_at) <= datetime(?)
		 ORDER BY revision_at DESC, id DESC LIMIT 1`,
		vantageKey, tickID, startedAt,
	))
}

// tickStartedAt reads when a tick started.
func (r *CombRevisionsRepo) tickStartedAt(tickID int64) (string, error) {
	var startedAt string
	err := r.readDB.QueryRow(
		`SELECT started_at FROM time_wheel WHERE id = ?`, tickID,
	).Scan(&startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("tick %d not found", tickID)
	}
	if err != nil {
		return "", fmt.Errorf("lookup tick: %w", err)
	}
	return startedAt, nil
}

// History returns up to `limit` most-recent revisions of `vantageKey`,
// most-recent first. Used by `chb comb history` to render the full
// arc of belief drift.
func (r *CombRevisionsRepo) History(vantageKey string, limit int) ([]*CombRevisionRow, error) {
	rows, err := r.readDB.Query(
		`SELECT id, vantage_key, vantage_kind, tick_id,
		        narrative, confidence, contested, dominant_label,
		        evidence_count, open_questions_count, raw_json,
		        source, revision_at
		 FROM comb_revisions
		 WHERE vantage_key = ?
		 ORDER BY revision_at DESC, id DESC LIMIT ?`,
		vantageKey, limitOr(limit, 100),
	)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	var out []*CombRevisionRow
	for rows.Next() {
		o, err := scanRevisionRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Diff returns the two revisions bracketing the [from, to] window for
// `vantageKey`: the latest revision at-or-before `from` (the baseline)
// and the latest revision at-or-before `to` (the current state). Either
// may be nil if no revision predates the corresponding bound.
//
// The actual diff computation (narrative diff, confidence delta, label
// flips, intervening signals) is built by the comb package on top of
// this primitive. We keep the storage layer narrow.
func (r *CombRevisionsRepo) Diff(vantageKey, from, to string) (*CombRevisionRow, *CombRevisionRow, error) {
	base, err := r.At(vantageKey, from)
	if err != nil {
		return nil, nil, fmt.Errorf("diff base: %w", err)
	}
	cur, err := r.At(vantageKey, to)
	if err != nil {
		return nil, nil, fmt.Errorf("diff current: %w", err)
	}
	return base, cur, nil
}

// CountByVantage returns a map vantage_key → revision count: which
// vantages have the deepest history.
func (r *CombRevisionsRepo) CountByVantage() (map[string]int, error) {
	rows, err := r.readDB.Query(
		`SELECT vantage_key, COUNT(*) FROM comb_revisions GROUP BY vantage_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// rowScanner is the minimal interface satisfied by *sql.Row and *sql.Rows
// so we can share scan code between Get-style and List-style queries.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRevisionRow(s rowScanner) (*CombRevisionRow, error) {
	o := &CombRevisionRow{}
	var contestedInt int
	var kind string
	err := s.Scan(
		&o.ID, &o.VantageKey, &kind, &o.TickID,
		&o.Narrative, &o.Confidence, &contestedInt, &o.DominantLabel,
		&o.EvidenceCount, &o.OpenQuestionsCount, &o.RawJSON,
		&o.Source, &o.RevisionAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan comb_revisions: %w", err)
	}
	o.VantageKind = VantageKind(kind)
	o.Contested = contestedInt != 0
	return o, nil
}

func scanRevisionRows(rows *sql.Rows) (*CombRevisionRow, error) {
	o := &CombRevisionRow{}
	var contestedInt int
	var kind string
	if err := rows.Scan(
		&o.ID, &o.VantageKey, &kind, &o.TickID,
		&o.Narrative, &o.Confidence, &contestedInt, &o.DominantLabel,
		&o.EvidenceCount, &o.OpenQuestionsCount, &o.RawJSON,
		&o.Source, &o.RevisionAt,
	); err != nil {
		return nil, err
	}
	o.VantageKind = VantageKind(kind)
	o.Contested = contestedInt != 0
	return o, nil
}

// nullableInt64 converts a sql.NullInt64 to a value sqlite stores as
// INTEGER or NULL. Distinct from nullableInt only by parameter type.
func nullableInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
