package hive

import (
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

type sealPoint struct {
	capped bool
	at     comb.Coords
}

// sealFixture is the Comb's capped-cell fixture F (internal/comb/caps_test.go).
var sealFixture = []sealPoint{
	{true, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3}},
	{true, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3}},
	{true, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3, "d5": 1}},
	{true, comb.Coords{"d1": 0, "d3": 5}},
	{true, comb.Coords{"d1": 0, "d3": 5, "d5": 3}},
	{true, comb.Coords{"d1": 0, "d2": 2, "d3": 5}},
	{true, comb.Coords{"d1": 1, "d2": 4}},
	{true, comb.Coords{"d1": 1, "d2": 4, "d3": 0, "d4": 0}},
	{false, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3, "d5": 2}},
	{false, comb.Coords{"d1": 0, "d2": 2, "d3": 6}},
	{false, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 9}},
	{false, comb.Coords{"d1": 2, "d2": 0, "d3": 0, "d4": 0}},
	{false, comb.Coords{"d1": 1, "d2": 4, "d3": 9, "d4": 9}},
}

// sealTwoAxisFixture is the Comb's two-axis fixture F2
// (internal/comb/caps_test.go): no finding sets d3 or d4.
var sealTwoAxisFixture = []sealPoint{
	{true, comb.Coords{"d1": 0, "d2": 1}},
	{true, comb.Coords{"d1": 0, "d2": 1}},
	{true, comb.Coords{"d1": 0, "d2": 1, "d5": 3}},
	{true, comb.Coords{"d1": 1}},
	{false, comb.Coords{"d1": 0, "d2": 1, "d5": 4}},
	{false, comb.Coords{"d1": 0, "d2": 2}},
	{false, comb.Coords{"d1": 2, "d2": 0}},
	{false, comb.Coords{"d1": 1, "d2": 5}},
}

func insertFindingAt(t *testing.T, s *db.Store, at comb.Coords, level string) int64 {
	t.Helper()
	d := at.AsNullCoords()
	res, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, d3, d4, d5, d6, d7, d8, mss_label, finding, convergence_level)
		VALUES (1, 'test', ?, ?, ?, ?, ?, ?, ?, ?, 'assumption', 'fixture', ?)`,
		d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7], level)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// The Comb's capped cell is the waggle seal. For a gap at each coordinate, a
// dance from a high-convergence patch that shares its d1 recruits to it
// exactly when the Comb does not count its cell capped: adding the gap's
// coordinate to the capped coordinates adds a capped cell exactly when its
// cell is not already one.
func TestSealParity_CombCappedCellIsTheWaggleSeal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture []sealPoint
		gaps    []comb.Coords
	}{
		{"F", sealFixture, []comb.Coords{
			{"d1": 0, "d2": 1, "d3": 2, "d4": 3},
			{"d1": 0, "d2": 1, "d3": 2, "d4": 9},
			{"d1": 0, "d3": 5},
			{"d1": 0, "d3": 5, "d4": 6},
			{"d1": 0, "d2": 2, "d3": 5},
			{"d1": 0, "d2": 2},
			{"d1": 1, "d2": 4},
			{"d1": 1, "d2": 4, "d3": 0},
			{"d1": 1, "d2": 4, "d3": 9, "d4": 9},
		}},
		{"F2", sealTwoAxisFixture, []comb.Coords{
			{"d1": 0, "d2": 1},
			{"d1": 0, "d2": 2},
			{"d1": 0, "d2": 5},
			{"d1": 0, "d2": 1, "d3": 7},
			{"d1": 1},
			{"d1": 1, "d2": 5},
			{"d1": 2, "d2": 0},
		}},
	} {
		sealedSeen, openSeen := 0, 0
		for _, g := range tc.gaps {
			t.Run(tc.name+"/"+comb.RegionKey(g), func(t *testing.T) {
				s := newTestStore(t)
				initProject(t, s, "p")
				var caps []Action
				for _, f := range tc.fixture {
					id := insertFindingAt(t, s, f.at, "low")
					if f.capped {
						caps = append(caps, Action{Type: "cap_finding", FindingID: id})
					}
				}
				if _, err := ApplyCaps(s, caps); err != nil {
					t.Fatal(err)
				}
				insertFindingAt(t, s, comb.Coords{"d1": g["d1"], "d2": 7}, "high")
				gd := g.AsNullCoords()
				if _, err := s.WriteDB.Exec(`INSERT INTO gaps (wave, agent, description, priority, d1, d2, d3, d4)
					VALUES (1, 'seed', 'gap', 'minor', ?, ?, ?, ?)`, gd[0], gd[1], gd[2], gd[3]); err != nil {
					t.Fatal(err)
				}

				state, err := ScanState(s, "p")
				if err != nil {
					t.Fatal(err)
				}
				sealed := len(evalWaggleDance(s, state)) == 0
				capped, err := comb.CappedCoords(s.ReadDB)
				if err != nil {
					t.Fatal(err)
				}
				// The gap's cell is capped exactly when adding its coordinate
				// to the capped coordinates adds no capped cell.
				combSays := comb.CappedCells(append(capped, g)) == comb.CappedCells(capped)
				if sealed != combSays {
					t.Errorf("gap at %s: waggle seal %v; the Comb counts its cell capped %v", fmt.Sprint(g), sealed, combSays)
				}
				if sealed {
					sealedSeen++
				} else {
					openSeen++
				}
			})
		}
		if sealedSeen == 0 || openSeen == 0 {
			t.Fatalf("%s: %d sealed and %d open gaps; the fixture must hold each", tc.name, sealedSeen, openSeen)
		}
	}
}
