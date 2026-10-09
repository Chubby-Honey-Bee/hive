package db

import (
	"database/sql"
	"fmt"
)

// EvaluationsRepo owns the evaluations table.
type EvaluationsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newEvaluationsRepo binds the two pools.
func newEvaluationsRepo(writeDB, readDB *sql.DB) *EvaluationsRepo {
	return &EvaluationsRepo{writeDB: writeDB, readDB: readDB}
}

// AddEvaluation records a wave evaluation.
func (r *EvaluationsRepo) AddEvaluation(wave, coverage, depth, sources, actionability, mssIntegrity int,
	verdict string, laundering, untraceable, redundant int, notes *string,
) error {
	_, err := r.writeDB.Exec(
		`INSERT INTO evaluations
		 (wave, coverage_score, depth_score, source_score, actionability_score,
		  mss_integrity_score, verdict, laundering_violations, untraceable_guarantees,
		  redundant_assumptions, notes)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		wave, coverage, depth, sources, actionability, mssIntegrity,
		verdict, laundering, untraceable, redundant, notes,
	)
	if err != nil {
		return fmt.Errorf("add evaluation: %w", err)
	}
	return nil
}
