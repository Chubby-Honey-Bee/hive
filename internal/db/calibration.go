package db

import (
	"database/sql"
	"fmt"
)

// ScoreKey identifies one calibration score: a predictor in a scope.
type ScoreKey struct {
	PredictorKind string // lens | label | convergence | synthesizer
	PredictorKey  string
	ScopeKey      string // "" is the global scope; else a coordinate prefix
}

// ScoreRow is one row of calibration_scores, or one snapshot of it in
// calibration_revisions. The counts are the formula's input; the rest is
// derived from them.
type ScoreRow struct {
	ScoreKey
	NResolved     int
	NConfirmed    int
	NRefuted      int
	NPartial      int
	HitRate       float64
	BrierSum      float64
	BrierN        int
	BrierScore    sql.NullFloat64 // NULL when no outcome stated a confidence
	Weight        float64
	Calibrated    bool
	UpdatedTickID sql.NullInt64
}

// Same reports whether two rows hold the same counts and derived values,
// the tick aside: what decides whether a recompute changed a row.
func (s *ScoreRow) Same(o *ScoreRow) bool {
	a, b := *s, *o
	a.UpdatedTickID, b.UpdatedTickID = sql.NullInt64{}, sql.NullInt64{}
	return a == b
}

// CalibrationRepo owns calibration_scores and calibration_revisions.
type CalibrationRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newCalibrationRepo binds the two pools.
func newCalibrationRepo(writeDB, readDB *sql.DB) *CalibrationRepo {
	return &CalibrationRepo{writeDB: writeDB, readDB: readDB}
}

const scoreColumns = `predictor_kind, predictor_key, scope_key, n_resolved, n_confirmed, n_refuted, n_partial,
	hit_rate, brier_sum, brier_n, brier_score, weight, calibrated, updated_tick_id`

// UpsertScore writes the row under its key, replacing every column.
func (r *CalibrationRepo) UpsertScore(s *ScoreRow) error {
	_, err := r.writeDB.Exec(
		`INSERT INTO calibration_scores (`+scoreColumns+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(predictor_kind, predictor_key, scope_key) DO UPDATE SET
			n_resolved = excluded.n_resolved, n_confirmed = excluded.n_confirmed,
			n_refuted = excluded.n_refuted, n_partial = excluded.n_partial,
			hit_rate = excluded.hit_rate, brier_sum = excluded.brier_sum, brier_n = excluded.brier_n,
			brier_score = excluded.brier_score, weight = excluded.weight,
			calibrated = excluded.calibrated, updated_tick_id = excluded.updated_tick_id`,
		s.PredictorKind, s.PredictorKey, s.ScopeKey, s.NResolved, s.NConfirmed, s.NRefuted, s.NPartial,
		s.HitRate, s.BrierSum, s.BrierN, s.BrierScore, s.Weight, boolInt(s.Calibrated), s.UpdatedTickID,
	)
	if err != nil {
		return fmt.Errorf("upsert calibration score: %w", err)
	}
	return nil
}

// DeleteScore removes one row; a missing row is not an error.
func (r *CalibrationRepo) DeleteScore(k ScoreKey) error {
	_, err := r.writeDB.Exec(
		`DELETE FROM calibration_scores WHERE predictor_kind = ? AND predictor_key = ? AND scope_key = ?`,
		k.PredictorKind, k.PredictorKey, k.ScopeKey,
	)
	if err != nil {
		return fmt.Errorf("delete calibration score: %w", err)
	}
	return nil
}

// GetScore returns one row, or nil when there is none.
func (r *CalibrationRepo) GetScore(k ScoreKey) (*ScoreRow, error) {
	rows, err := r.readDB.Query(
		`SELECT `+scoreColumns+` FROM calibration_scores
		 WHERE predictor_kind = ? AND predictor_key = ? AND scope_key = ?`,
		k.PredictorKind, k.PredictorKey, k.ScopeKey,
	)
	if err != nil {
		return nil, fmt.Errorf("get calibration score: %w", err)
	}
	defer rows.Close()
	out, err := scanScoreRows(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out[0], nil
}

// ListScores returns the rows in key order. An empty kind matches every
// kind; a nil scope matches every scope, and a pointer to "" the global one.
func (r *CalibrationRepo) ListScores(kind string, scope *string) ([]*ScoreRow, error) {
	q := `SELECT ` + scoreColumns + ` FROM calibration_scores WHERE 1=1`
	var args []any
	if kind != "" {
		q += ` AND predictor_kind = ?`
		args = append(args, kind)
	}
	if scope != nil {
		q += ` AND scope_key = ?`
		args = append(args, *scope)
	}
	rows, err := r.readDB.Query(q+` ORDER BY predictor_kind, predictor_key, scope_key`, args...)
	if err != nil {
		return nil, fmt.Errorf("list calibration scores: %w", err)
	}
	defer rows.Close()
	return scanScoreRows(rows)
}

// SnapshotRevisions appends one calibration_revisions row per score under
// the tick. The snapshot's UpdatedTickID is the tick.
func (r *CalibrationRepo) SnapshotRevisions(tickID int64, rows []*ScoreRow) error {
	for _, s := range rows {
		if _, err := r.writeDB.Exec(
			`INSERT INTO calibration_revisions (tick_id, predictor_kind, predictor_key, scope_key,
				n_resolved, n_confirmed, n_refuted, n_partial, hit_rate, brier_sum, brier_n,
				brier_score, weight, calibrated)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			tickID, s.PredictorKind, s.PredictorKey, s.ScopeKey,
			s.NResolved, s.NConfirmed, s.NRefuted, s.NPartial, s.HitRate, s.BrierSum, s.BrierN,
			s.BrierScore, s.Weight, boolInt(s.Calibrated),
		); err != nil {
			return fmt.Errorf("snapshot calibration revision: %w", err)
		}
	}
	return nil
}

// LatestRevisions returns, per key, the most recent snapshot: what a
// recompute compares its new values against for drift.
func (r *CalibrationRepo) LatestRevisions() (map[ScoreKey]*ScoreRow, error) {
	rows, err := r.readDB.Query(
		`SELECT predictor_kind, predictor_key, scope_key, n_resolved, n_confirmed, n_refuted, n_partial,
			hit_rate, brier_sum, brier_n, brier_score, weight, calibrated, tick_id
		 FROM calibration_revisions
		 WHERE id IN (SELECT MAX(id) FROM calibration_revisions GROUP BY predictor_kind, predictor_key, scope_key)
		 ORDER BY predictor_kind, predictor_key, scope_key`,
	)
	if err != nil {
		return nil, fmt.Errorf("latest calibration revisions: %w", err)
	}
	defer rows.Close()
	list, err := scanScoreRows(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[ScoreKey]*ScoreRow, len(list))
	for _, s := range list {
		out[s.ScoreKey] = s
	}
	return out, nil
}

// ScoresAtTick returns, per key, the snapshot current at a tick: the
// latest revision under a tick with an id at or below tickID. Ticks open
// in id order, so that is the score a reader at tick N saw.
func (r *CalibrationRepo) ScoresAtTick(tickID int64) ([]*ScoreRow, error) {
	return r.scoresWhere(`tick_id <= ?`, tickID)
}

// ScoresAtTime returns, per key, the snapshot current at a moment: the
// latest revision under a calibrate tick that started at or before it.
// The moment is in SQLite's timestamp form, as CombRevisionsRepo.At takes.
func (r *CalibrationRepo) ScoresAtTime(at string) ([]*ScoreRow, error) {
	return r.scoresWhere(`tick_id IN (SELECT id FROM time_wheel WHERE datetime(started_at) <= datetime(?))`, at)
}

func (r *CalibrationRepo) scoresWhere(cond string, arg any) ([]*ScoreRow, error) {
	rows, err := r.readDB.Query(
		`SELECT predictor_kind, predictor_key, scope_key, n_resolved, n_confirmed, n_refuted, n_partial,
			hit_rate, brier_sum, brier_n, brier_score, weight, calibrated, tick_id
		 FROM calibration_revisions
		 WHERE id IN (SELECT MAX(id) FROM calibration_revisions WHERE `+cond+`
		              GROUP BY predictor_kind, predictor_key, scope_key)
		 ORDER BY predictor_kind, predictor_key, scope_key`, arg,
	)
	if err != nil {
		return nil, fmt.Errorf("calibration scores at: %w", err)
	}
	defer rows.Close()
	return scanScoreRows(rows)
}

// CountRevisions returns the number of snapshot rows.
func (r *CalibrationRepo) CountRevisions() (int, error) {
	var n int
	if err := r.readDB.QueryRow(`SELECT COUNT(*) FROM calibration_revisions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count calibration revisions: %w", err)
	}
	return n, nil
}

func scanScoreRows(rows *sql.Rows) ([]*ScoreRow, error) {
	var out []*ScoreRow
	for rows.Next() {
		s := &ScoreRow{}
		var calibrated int
		if err := rows.Scan(
			&s.PredictorKind, &s.PredictorKey, &s.ScopeKey, &s.NResolved, &s.NConfirmed, &s.NRefuted, &s.NPartial,
			&s.HitRate, &s.BrierSum, &s.BrierN, &s.BrierScore, &s.Weight, &calibrated, &s.UpdatedTickID,
		); err != nil {
			return nil, fmt.Errorf("scan calibration score: %w", err)
		}
		s.Calibrated = calibrated != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
