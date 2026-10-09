package mss

// DepNode is one finding as WalkDeps reads it: its id, its label and the ids
// it depends on.
type DepNode struct {
	ID    int64
	Label string
	Deps  []int64
}

// WalkDeps visits each finding the dependency ids start reach, transitively,
// a level at a time: start is the first level, and each next level is the
// dependencies of the level before that the walk has not met. It follows
// every finding's dependencies whatever its label, visits each finding once,
// and neither visits nor follows an id with no finding. readLevel returns the
// findings among one level's ids that exist, in the order to visit them; the
// walk follows their dependencies in that order, which makes it breadth
// first. An error from readLevel or visit ends the walk with that error.
//
// It is the one transitive walk no-laundering rests on: the write-time check
// (ValidateGuaranteeDeps) reads each level with one query, and the audit
// (db.RunAudit) reads it from the findings it loaded.
func WalkDeps(start []int64, readLevel func(ids []int64) ([]DepNode, error), visit func(DepNode) error) error {
	met := make(map[int64]bool)
	level := unmet(start, met)
	for len(level) > 0 {
		nodes, err := readLevel(level)
		if err != nil {
			return err
		}
		next, err := visitLevel(nodes, visit)
		if err != nil {
			return err
		}
		level = unmet(next, met)
	}
	return nil
}

// visitLevel visits each finding of a level in turn and returns their
// dependencies, in that order.
func visitLevel(nodes []DepNode, visit func(DepNode) error) ([]int64, error) {
	var next []int64
	for _, n := range nodes {
		if err := visit(n); err != nil {
			return nil, err
		}
		next = append(next, n.Deps...)
	}
	return next, nil
}

// unmet is the ids the walk has not met, each once and in order, now marked
// met.
func unmet(ids []int64, met map[int64]bool) []int64 {
	level := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !met[id] {
			met[id] = true
			level = append(level, id)
		}
	}
	return level
}
