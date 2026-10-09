package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// TickKind classifies a tick on the Time Wheel. Each kind represents a
// different cadence at which the system marks "this is when something
// happened" — swarms, waves, sessions, days, ad-hoc, and ripen
// rounds. Chronomantic queries are bounded probes per kind, not
// free-form date scans.
type TickKind string

// The tick kinds time_wheel.kind accepts.
const (
	TickSwarm   TickKind = "swarm"
	TickWave    TickKind = "wave"
	TickSession TickKind = "session"
	TickDay     TickKind = "day"
	TickManual  TickKind = "manual"
	TickRipen   TickKind = "ripen"
	// TickCalibrate brackets one `chb calibrate` recompute; its notes carry
	// the ledger high-water mark (CalibrateTickNotes).
	TickCalibrate TickKind = "calibrate"
)

// TickRow is one tick on the Time Wheel.
type TickRow struct {
	ID        int64
	Label     string
	Kind      TickKind
	RunID     sql.NullInt64
	Wave      sql.NullInt64
	StartedAt string
	EndedAt   sql.NullString
	Notes     sql.NullString
}

// TimeWheelRepo owns the time_wheel table — the chronomantic axis.
// Every Comb revision can be anchored to a tick; ticks make temporal
// queries bounded probes (idx_comb_rev_tick supports the join) instead
// of free-form date-range scans.
type TimeWheelRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newTimeWheelRepo binds the two pools.
func newTimeWheelRepo(writeDB, readDB *sql.DB) *TimeWheelRepo {
	return &TimeWheelRepo{writeDB: writeDB, readDB: readDB}
}

// Begin opens a tick and returns its id. The label must be unique
// within the kind (UNIQUE(kind, label) enforced by schema). When a
// tick of the same (kind, label) already exists, returns the existing
// row's id (idempotent).
func (r *TimeWheelRepo) Begin(label string, kind TickKind, runID, wave int64, notes string) (int64, error) {
	if err := checkTick(label, kind); err != nil {
		return 0, err
	}
	// Idempotent: re-open is a no-op that returns the existing id.
	existing, found, err := r.tickID(kind, label)
	if err != nil || found {
		return existing, err
	}
	return r.insertTick(label, kind, runID, wave, notes)
}

// checkTick refuses a tick with no label or of an unknown kind.
func checkTick(label string, kind TickKind) error {
	if label == "" {
		return errors.New("tick label required")
	}
	if !validTickKind(kind) {
		return fmt.Errorf("invalid tick kind %q", string(kind))
	}
	return nil
}

// tickID is the id of the tick of this kind and label, when there is one.
func (r *TimeWheelRepo) tickID(kind TickKind, label string) (int64, bool, error) {
	var existing int64
	err := r.readDB.QueryRow(
		`SELECT id FROM time_wheel WHERE kind = ? AND label = ?`,
		string(kind), label,
	).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("probe tick: %w", err)
	}
	return existing, true, nil
}

// insertTick writes a new open tick and returns its id.
func (r *TimeWheelRepo) insertTick(label string, kind TickKind, runID, wave int64, notes string) (int64, error) {
	res, err := r.writeDB.Exec(
		`INSERT INTO time_wheel (label, kind, run_id, wave, notes)
		 VALUES (?, ?, ?, ?, ?)`,
		label, string(kind),
		nullIfZero(runID), nullIfZero(wave), nullIfEmpty(notes),
	)
	if err != nil {
		return 0, fmt.Errorf("begin tick: %w", err)
	}
	return res.LastInsertId()
}

// End closes a tick by setting ended_at = CURRENT_TIMESTAMP. Idempotent;
// re-closing is a no-op.
func (r *TimeWheelRepo) End(id int64) error {
	_, err := r.writeDB.Exec(
		`UPDATE time_wheel SET ended_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND ended_at IS NULL`,
		id,
	)
	return err
}

// Reopen clears a tick's ended_at so writes anchor to it again. A resumed
// run uses it: Begin hands back the run's tick, which the first segment
// closed, and CurrentOpen skips a closed tick.
func (r *TimeWheelRepo) Reopen(id int64) error {
	_, err := r.writeDB.Exec(`UPDATE time_wheel SET ended_at = NULL WHERE id = ?`, id)
	return err
}

// Get returns one tick by id.
func (r *TimeWheelRepo) Get(id int64) (*TickRow, error) {
	return oneTick(r.readDB.QueryRow(
		`SELECT id, label, kind, run_id, wave, started_at, ended_at, notes
		 FROM time_wheel WHERE id = ?`, id,
	), "get tick")
}

// Recent returns up to `limit` most-recent ticks of the given kind
// (or all kinds when kind == ""), most-recent first.
func (r *TimeWheelRepo) Recent(kind TickKind, limit int) ([]*TickRow, error) {
	q, args := recentTicksQuery(kind, limitOr(limit, 100))
	rows, err := r.readDB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list ticks: %w", err)
	}
	defer rows.Close()
	return scanTicks(rows)
}

// recentTicksQuery is Recent's SELECT and its arguments.
func recentTicksQuery(kind TickKind, limit int) (string, []any) {
	q := `SELECT id, label, kind, run_id, wave, started_at, ended_at, notes
	      FROM time_wheel`
	var args []any
	if kind != "" {
		q += ` WHERE kind = ?`
		args = append(args, string(kind))
	}
	q += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	return q, append(args, limit)
}

// scanTicks reads every tick row.
func scanTicks(rows *sql.Rows) ([]*TickRow, error) {
	var out []*TickRow
	for rows.Next() {
		t, err := scanTick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// scanTick reads one tick row.
func scanTick(s rowScanner) (*TickRow, error) {
	t := &TickRow{}
	var k string
	if err := s.Scan(
		&t.ID, &t.Label, &k, &t.RunID, &t.Wave,
		&t.StartedAt, &t.EndedAt, &t.Notes,
	); err != nil {
		return nil, err
	}
	t.Kind = TickKind(k)
	return t, nil
}

// oneTick reads a single-tick query's row: nil when there is none, and an
// error named by what otherwise.
func oneTick(row *sql.Row, what string) (*TickRow, error) {
	t, err := scanTick(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return t, nil
}

// Current returns the most-recent open tick of the given kind, or nil
// when none is open. "Open" = ended_at IS NULL.
func (r *TimeWheelRepo) Current(kind TickKind) (*TickRow, error) {
	return oneTick(r.readDB.QueryRow(
		`SELECT id, label, kind, run_id, wave, started_at, ended_at, notes
		 FROM time_wheel
		 WHERE kind = ? AND ended_at IS NULL
		 ORDER BY started_at DESC, id DESC LIMIT 1`,
		string(kind),
	), "current tick")
}

// CurrentOpen returns the most recently begun tick that is still open, or
// nil when there is none. This is what anchors a Comb revision. A write made
// by a run (runID != 0) considers only that run's ticks: concurrent runs in
// one process (`chb replicate`, the MCP server) each hold an open tick,
// and the newest of any kind is another run's as often as not. A write
// outside a run takes whatever tick is open (a wave gate, a ripening pass).
func (r *TimeWheelRepo) CurrentOpen(runID int64) (*TickRow, error) {
	q := `SELECT id, label, kind, run_id, wave, started_at, ended_at, notes
		 FROM time_wheel
		 WHERE ended_at IS NULL`
	var args []any
	if runID != 0 {
		q += ` AND run_id = ?`
		args = append(args, runID)
	}
	return oneTick(r.readDB.QueryRow(q+` ORDER BY started_at DESC, id DESC LIMIT 1`, args...), "current open tick")
}

// CountByKind returns map kind → count over the entire wheel.
func (r *TimeWheelRepo) CountByKind() (map[TickKind]int, error) {
	rows, err := r.readDB.Query(
		`SELECT kind, COUNT(*) FROM time_wheel GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[TickKind]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[TickKind(k)] = n
	}
	return out, rows.Err()
}

func validTickKind(k TickKind) bool {
	switch k {
	case TickSwarm, TickWave, TickSession, TickDay, TickManual, TickRipen, TickCalibrate:
		return true
	}
	return false
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
