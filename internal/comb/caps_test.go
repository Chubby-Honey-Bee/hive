package comb

import (
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

type capsPoint struct {
	capped bool
	at     Coords
}

// capsFixture is fixture F: capped findings in one cell with and without a
// d5, two in a cell with absent axes beside a cell that sets one of them,
// one in a cell whose absent axes read as 0 would merge it with another, and
// uncapped findings in the same cell past d4, at a sibling d4, in a cell an
// absent-axis cap would match on its set axes alone, and elsewhere.
var capsFixture = []capsPoint{
	{true, Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3}},
	{true, Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3}},
	{true, Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3, "d5": 1}},
	{true, Coords{"d1": 0, "d3": 5}},
	{true, Coords{"d1": 0, "d3": 5, "d5": 3}},
	{true, Coords{"d1": 0, "d2": 2, "d3": 5}},
	{true, Coords{"d1": 1, "d2": 4}},
	{true, Coords{"d1": 1, "d2": 4, "d3": 0, "d4": 0}},
	{false, Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3, "d5": 2}},
	{false, Coords{"d1": 0, "d2": 2, "d3": 6}},
	{false, Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 9}},
	{false, Coords{"d1": 2, "d2": 0, "d3": 0, "d4": 0}},
	{false, Coords{"d1": 1, "d2": 4, "d3": 9, "d4": 9}},
}

// twoAxisFixture is fixture F2: no finding sets d3 or d4, as in a workspace
// whose agents write d1 and d2 only. It caps a cell with and without a d5, and
// a cell that sets d1 alone beside an uncapped cell under the same d1.
var twoAxisFixture = []capsPoint{
	{true, Coords{"d1": 0, "d2": 1}},
	{true, Coords{"d1": 0, "d2": 1}},
	{true, Coords{"d1": 0, "d2": 1, "d5": 3}},
	{true, Coords{"d1": 1}},
	{false, Coords{"d1": 0, "d2": 1, "d5": 4}},
	{false, Coords{"d1": 0, "d2": 2}},
	{false, Coords{"d1": 2, "d2": 0}},
	{false, Coords{"d1": 1, "d2": 5}},
}

var capsFixtures = map[string][]capsPoint{"F": capsFixture, "F2": twoAxisFixture}

// seedCapsFixture writes the fixture's findings, only the capped ones when
// cappedOnly, and caps the capped ones when capIt.
func seedCapsFixture(t *testing.T, store *db.Store, fixture []capsPoint, cappedOnly, capIt bool) {
	t.Helper()
	src := "https://example.com/caps"
	for _, f := range fixture {
		if cappedOnly && !f.capped {
			continue
		}
		row := &db.Finding{Wave: 1, Agent: "test", MSSLabel: "assumption", Finding: "fixture", SourceURLs: &src}
		ptrs := []**int{&row.D1, &row.D2, &row.D3, &row.D4, &row.D5, &row.D6, &row.D7, &row.D8}
		for i, d := range orderedDims() {
			if v, ok := f.at[d]; ok {
				*ptrs[i] = intPtr(v)
			}
		}
		id, err := store.Findings().AddFinding(row)
		if err != nil {
			t.Fatalf("AddFinding %v: %v", f.at, err)
		}
		if capIt && f.capped {
			if _, err := store.WriteDB.Exec(`INSERT INTO capped_findings (finding_id) VALUES (?)`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func mustRegion(t *testing.T, key string) Coords {
	t.Helper()
	c, err := ParseRegionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Capped cells are the distinct d1..d4 of the capped findings, an absent
// axis a value of its own: the hive's seal query, where DISTINCT treats NULLs
// as equal.
func TestCappedCells_CountsDistinctCellsWithAbsentAxes(t *testing.T) {
	for name, fixture := range capsFixtures {
		t.Run(name, func(t *testing.T) {
			store := freshStore(t)
			seedCapsFixture(t, store, fixture, false, true)
			capped, err := CappedCoords(store.ReadConn())
			if err != nil {
				t.Fatal(err)
			}
			var want int
			if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM (SELECT DISTINCT f.d1, f.d2, f.d3, f.d4
				FROM capped_findings cf JOIN findings f ON f.id = cf.finding_id)`).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if got := CappedCells(capped); got != want {
				t.Errorf("CappedCells = %d; want %d", got, want)
			}
		})
	}
}

// A database with no capped_findings table holds no caps. A table of another
// shape is a read error, not an empty set.
func TestCappedCoords_NoTableHoldsNoCaps(t *testing.T) {
	store := freshStore(t)
	addFinding(t, store, "assumption", "x", intPtr(0), nil, nil)
	if _, err := store.WriteDB.Exec(`DROP TABLE capped_findings`); err != nil {
		t.Fatal(err)
	}
	got, err := CappedCoords(store.ReadConn())
	if err != nil || len(got) != 0 {
		t.Fatalf("no table: CappedCoords = %v, %v; want no caps and no error", got, err)
	}
	if _, err := store.WriteDB.Exec(`CREATE TABLE capped_findings (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if got, err := CappedCoords(store.ReadConn()); err == nil {
		t.Fatalf("a capped_findings table of another shape: CappedCoords = %v, nil; want an error", got)
	}
}
