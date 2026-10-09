package hive

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// progressQuery reads the research state a pass can change: the findings,
// their labels and their last edit; the gaps and how many are open; the
// conflicts, how many are open and how many have a winner; the sources, the
// evaluation verdicts and the open wave gates. Signals, caps and hive_state
// are left out: every scan writes those, so they would make every pass look
// like progress. An evaluation counts only when its verdict differs from
// the wave's previous one: a pass that gates a blocked wave again records
// the same verdict, and that is no progress.
const progressQuery = `SELECT
	(SELECT COUNT(*) FROM findings),
	(SELECT COALESCE(MAX(id), 0) FROM findings),
	(SELECT COUNT(*) FROM findings WHERE mss_label = 'definition'),
	(SELECT COUNT(*) FROM findings WHERE mss_label = 'guarantee'),
	(SELECT COUNT(*) FROM findings WHERE mss_label = 'assumption'),
	(SELECT COUNT(*) FROM findings WHERE mss_label = 'unknown'),
	(SELECT COALESCE(MAX(updated_at), '') FROM findings),
	(SELECT COUNT(*) FROM gaps),
	(SELECT COUNT(*) FROM gaps WHERE resolved_by_wave IS NULL),
	(SELECT COUNT(*) FROM conflicts),
	(SELECT COUNT(*) FROM conflicts WHERE resolved_by_wave IS NULL),
	(SELECT COUNT(*) FROM conflicts WHERE winner_finding_id IS NOT NULL),
	(SELECT COUNT(*) FROM sources),
	(SELECT COUNT(*) FROM (
		SELECT verdict, LAG(verdict) OVER (PARTITION BY wave ORDER BY id) AS previous FROM evaluations
	) WHERE previous IS NULL OR previous IS NOT verdict),
	(SELECT COUNT(*) FROM wave_gates)`

// Progress is the fingerprint of the research state progressQuery reads.
// Two readings are equal exactly when nothing it covers changed.
type Progress struct {
	Findings        int64  `json:"findings"`
	MaxFindingID    int64  `json:"max_finding_id"`
	Definitions     int64  `json:"definitions"`
	Guarantees      int64  `json:"guarantees"`
	Assumptions     int64  `json:"assumptions"`
	Unknowns        int64  `json:"unknowns"`
	LastEdit        string `json:"last_edit"`
	Gaps            int64  `json:"gaps"`
	OpenGaps        int64  `json:"open_gaps"`
	Conflicts       int64  `json:"conflicts"`
	OpenConflicts   int64  `json:"open_conflicts"`
	ConflictWinners int64  `json:"conflict_winners"`
	Sources         int64  `json:"sources"`
	VerdictChanges  int64  `json:"verdict_changes"`
	WaveGates       int64  `json:"wave_gates"`
}

// canonical is the fingerprint as the JSON hive_iterations stores. The
// fields have one fixed order, so equal states give equal strings.
func (p Progress) canonical() string {
	b, _ := json.Marshal(p)
	return string(b)
}

// rowReader is a pool or a db.Tx.
type rowReader interface {
	QueryRow(query string, args ...any) *sql.Row
}

// readProgress reads the research state's fingerprint.
func readProgress(q rowReader) (Progress, error) {
	var p Progress
	if err := q.QueryRow(progressQuery).Scan(
		&p.Findings, &p.MaxFindingID, &p.Definitions, &p.Guarantees, &p.Assumptions, &p.Unknowns,
		&p.LastEdit, &p.Gaps, &p.OpenGaps, &p.Conflicts, &p.OpenConflicts, &p.ConflictWinners,
		&p.Sources, &p.VerdictChanges, &p.WaveGates,
	); err != nil {
		return p, fmt.Errorf("read hive progress: %w", err)
	}
	return p, nil
}

// stallPasses is how many passes in a row must leave the research state as
// they found it before the hive stops.
const stallPasses = 2

// Stalled reports whether the last stallPasses passes up to and including
// iteration each ended with the research state as its scan found it. A pass
// with no recorded end, as when its run failed before `hive complete`, is not
// a stalled one.
func Stalled(store *db.Store, project string, iteration int) (bool, error) {
	if iteration < stallPasses {
		return false, nil
	}
	var unchanged int
	if err := store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM hive_iterations
		 WHERE project=? AND iteration > ? AND iteration <= ?
		   AND end_progress IS NOT NULL AND end_progress = start_progress`,
		project, iteration-stallPasses, iteration,
	).Scan(&unchanged); err != nil {
		return false, fmt.Errorf("read hive history: %w", err)
	}
	return unchanged == stallPasses, nil
}

// LoopDecision is the loop rule, computed rather than judged: another pass
// while the hive has run fewer than max passes and has not stalled. The
// reason names what stopped it, and is empty when the loop goes on. A
// terminal state is left to the next scan, the one place the terminal phase
// is recorded.
func LoopDecision(store *db.Store, project string, iteration, max int) (bool, string, error) {
	if iteration >= max {
		return false, fmt.Sprintf("iteration %d reached max_iterations %d", iteration, max), nil
	}
	stalled, err := Stalled(store, project, iteration)
	if err != nil {
		return false, "", err
	}
	if stalled {
		return false, fmt.Sprintf("stalled: the last %d passes changed no finding, gap, conflict, source, evaluation verdict or gate", stallPasses), nil
	}
	return true, "", nil
}
