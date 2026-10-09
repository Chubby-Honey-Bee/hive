package hive

import (
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// CapResult records what ApplyCaps did to one candidate.
type CapResult struct {
	FindingID int64  `json:"finding_id"`
	Capped    bool   `json:"capped"`
	Reason    string `json:"reason"`
}

// ApplyCaps executes the `cap_finding` actions in a plan. Quorum
// convergence caps the cell: the finding is recorded in capped_findings and
// keeps its label, an assumption. Agreement among agents is not a
// derivation, so it never makes a guarantee; a guarantee follows only from
// distinct premises named in its depends_on_ids.
//
// The record is durable. The quorum rule leaves a capped finding out, so the
// level-triggered signal fires for it once and the plan acts on it once, and
// no waggle dance recruits to its coordinate. A finding capped already is
// reported, not capped again. With nothing to cap the list is empty, not
// nil, so `caps` prints [].
func ApplyCaps(store *db.Store, actions []Action) ([]CapResult, error) {
	return applyCaps(store.WriteDB, actions)
}

// applyCaps is ApplyCaps on an executor: the write pool, or the transaction
// a scan records in.
func applyCaps(ex execer, actions []Action) ([]CapResult, error) {
	out := []CapResult{}
	for _, a := range actions {
		if !capsFinding(a) {
			continue
		}
		res, err := capFinding(ex, a.FindingID)
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}

// capsFinding reports whether an action is a cap_finding that names a
// finding.
func capsFinding(a Action) bool {
	return a.Type == "cap_finding" && a.FindingID > 0
}

// capFinding records one finding's cap, once.
func capFinding(ex execer, findingID int64) (CapResult, error) {
	res, err := ex.Exec(`INSERT OR IGNORE INTO capped_findings (finding_id) VALUES (?)`, findingID)
	if err != nil {
		return CapResult{}, fmt.Errorf("cap finding %d: %w", findingID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return CapResult{}, fmt.Errorf("cap finding %d: %w", findingID, err)
	}
	if n == 0 {
		return CapResult{FindingID: findingID, Reason: "already capped"}, nil
	}
	return CapResult{
		FindingID: findingID,
		Capped:    true,
		Reason:    "quorum: the cell is capped, and the finding stays an assumption",
	}, nil
}
