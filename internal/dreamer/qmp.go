package dreamer

import (
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// LaunderingDetected returns true if the live MSS audit reports any
// laundering violations or partition violations. Used as the QMP
// (Queen Mandibular Pheromone) halt — the ripen loop refuses to keep
// running when MSS integrity has been broken, mirroring the live wave
// gate's policy.
func LaunderingDetected(store *db.Store) (bool, string, error) {
	res, err := store.MSSAudit()
	if err != nil {
		return false, "", err
	}
	reason := haltingViolation(res)
	return reason != "", reason, nil
}

// haltingViolation names the first violation in res that halts the loop —
// laundering, then partition, then a dependency cycle — or is empty when res
// holds none of them.
func haltingViolation(res *db.MSSAuditResult) string {
	switch {
	case len(res.LaunderingViolations) > 0:
		return "laundering"
	case len(res.PartitionViolations) > 0:
		return "partition"
	case len(res.DependencyCycles) > 0:
		return "cycle"
	}
	return ""
}

// EmitQMP writes a `qmp` signal so observers (HIVE plan, /comb page)
// know the swarm's Dreamer has halted its background metabolism.
func EmitQMP(store *db.Store, reason string) error {
	src := "system"
	_, err := store.Signals().EmitSignal(
		"qmp",
		&src,
		nil,
		nil, nil, nil, nil,
		map[string]any{
			"reason": reason,
			"source": "dreamer.qmp",
		},
		nil,
	)
	return err
}
