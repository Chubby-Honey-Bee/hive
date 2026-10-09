package comb

import "database/sql"

// CappedCoords returns d1..d8 of every capped finding, an absent axis left
// out. A database with no capped_findings table holds no caps: every build
// that writes one creates the table when it opens the database.
func CappedCoords(read *sql.DB) ([]Coords, error) {
	exists, err := tableExists(read, "capped_findings")
	if err != nil || !exists {
		return nil, err
	}
	return collectCoords(read, `SELECT f.d1, f.d2, f.d3, f.d4, f.d5, f.d6, f.d7, f.d8
		FROM capped_findings cf JOIN findings f ON f.id = cf.finding_id
		ORDER BY cf.finding_id`)
}

// CappedCells counts the distinct cells that hold a capped finding. A cell is
// d1..d4, and an absent axis is a value of its own, as the hive's seal
// compares them.
func CappedCells(capped []Coords) int {
	type axis struct {
		v   int
		set bool
	}
	cell := orderedDims()[:4]
	seen := map[[4]axis]bool{}
	for _, c := range capped {
		var k [4]axis
		for i, d := range cell {
			v, ok := c[d]
			k[i] = axis{v, ok}
		}
		seen[k] = true
	}
	return len(seen)
}
