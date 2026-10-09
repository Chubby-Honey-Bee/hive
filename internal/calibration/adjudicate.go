package calibration

import (
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Adjudication is what Adjudicate did: the conflict, the finding that
// survives, the one that does not, and the ledger row on it.
type Adjudication struct {
	ConflictID int64
	WinnerID   int64
	LoserID    int64
	OutcomeID  int64
}

// Adjudicate names the finding that survives a conflict
// (ConflictsRepo.SetWinner) and writes one `refuted` outcome on the losing
// finding with source `downstream_run`. It is the one writer of that
// source. It calls no cascade: naming the winner arms the alarm cascade
// (hive.md, evalAlarm), and Record runs none for `downstream_run`
// (CALIB-2). SetWinner's refusals stand: an unknown conflict, a finding
// not party to it, or a conflict already resolved writes nothing.
func Adjudicate(store *db.Store, conflictID, winnerID int64) (Adjudication, error) {
	if err := store.Conflicts().SetWinner(conflictID, winnerID); err != nil {
		return Adjudication{}, err
	}
	adj := Adjudication{ConflictID: conflictID, WinnerID: winnerID}
	loser, err := conflictLoser(store, conflictID, winnerID)
	if err != nil {
		return adj, fmt.Errorf("conflict %d adjudicated; read its findings: %w", conflictID, err)
	}
	adj.LoserID = loser
	rec, err := Record(store, Outcome{
		SubjectKind: SubjectFinding,
		FindingID:   adj.LoserID,
		Resolution:  Refuted,
		Source:      SourceDownstreamRun,
		Rationale:   fmt.Sprintf("adjudicated conflict %d: finding %d survives", conflictID, winnerID),
	})
	if err != nil {
		return adj, fmt.Errorf("conflict %d adjudicated; outcome on finding %d: %w", conflictID, adj.LoserID, err)
	}
	adj.OutcomeID = rec.ID
	return adj, nil
}

// conflictLoser is the finding of conflict conflictID that is not winnerID.
func conflictLoser(store *db.Store, conflictID, winnerID int64) (int64, error) {
	var a, b int64
	if err := store.ReadDB.QueryRow(
		`SELECT finding_a_id, finding_b_id FROM conflicts WHERE id = ?`, conflictID,
	).Scan(&a, &b); err != nil {
		return 0, err
	}
	if winnerID == a {
		return b, nil
	}
	return a, nil
}
