package comb

import (
	"database/sql"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Staleness captures whether a stored vantage digest is still the one a
// refresh would write.
type Staleness string

// The Staleness of a region's digest, as Classify reports it.
const (
	StaleFresh   Staleness = "fresh"   // a refresh would write the digest the row holds
	StaleStale   Staleness = "stale"   // a refresh would write another digest
	StaleMissing Staleness = "missing" // no digest row exists for this region
)

// Classify returns fresh|stale|missing for a region by comb.md § Staleness.
// Used by `comb query` to surface staleness in the response without forcing
// a refresh.
func Classify(store *db.Store, region Coords) (Staleness, error) {
	key := RegionKey(region)
	stale, err := vantageStaleness(store.ReadConn(), &key)
	if err != nil {
		return "", err
	}
	s, ok := stale[key]
	if !ok {
		return StaleMissing, nil
	}
	if s {
		return StaleStale, nil
	}
	return StaleFresh, nil
}

// StaleVantages maps the key of every comb_state row to whether that vantage
// is stale by comb.md § Staleness. It runs at most six queries whatever the
// number of rows, in one read transaction, and closes each before the next,
// so a connection that allows one open query serves it. A database with no
// comb_state table holds no vantages.
func StaleVantages(read *sql.DB) (map[string]bool, error) {
	return vantageStaleness(read, nil)
}

// vantageStaleness applies comb.md § Staleness to the row keyed key, or to
// every row when key is nil. A region is stale when a refresh would write it
// a different digest: regionTallies reads what BuildDigest reads, the
// tally's digest is the one BuildDigest would write, and the row is compared
// with it on each field the digest sets. It orders no times, so a change in
// the second of a refresh counts like any other. A forager vantage is never
// stale. It reads in one transaction, so the rows and the tables are one
// database state.
func vantageStaleness(read *sql.DB, key *string) (map[string]bool, error) {
	tx, err := read.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	exists, err := tableExists(tx, "comb_state")
	if err != nil {
		return nil, err
	}
	if !exists {
		return map[string]bool{}, nil
	}
	return stalenessIn(tx, key)
}

// stalenessIn is vantageStaleness within the read transaction tx, on a
// database that holds comb_state.
func stalenessIn(tx *sql.Tx, key *string) (map[string]bool, error) {
	vs, err := readVantages(tx, key)
	if err != nil {
		return nil, err
	}
	out := notStale(vs)
	regions := regionVantages(vs)
	if len(regions) == 0 {
		return out, nil
	}
	if err := decideByDigest(tx, keyRegion(key), regions, out); err != nil {
		return nil, err
	}
	return out, nil
}

// notStale maps the key of each vantage in vs to false, the staleness of a
// vantage no digest decides.
func notStale(vs []db.CombRow) map[string]bool {
	out := make(map[string]bool, len(vs))
	for _, v := range vs {
		out[v.VantageKey] = false
	}
	return out
}

// readVantages reads the comb_state row keyed key, or every row when key is
// nil.
func readVantages(tx *sql.Tx, key *string) ([]db.CombRow, error) {
	q := `SELECT vantage_key, narrative, confidence, contested, dominant_label, evidence_count, open_questions_count FROM comb_state`
	var args []any
	if key != nil {
		q += ` WHERE vantage_key = ?`
		args = append(args, *key)
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var vs []db.CombRow
	err = eachRow(rows, func() error {
		var v db.CombRow
		if err := rows.Scan(&v.VantageKey, &v.Narrative, &v.Confidence, &v.Contested,
			&v.DominantLabel, &v.EvidenceCount, &v.OpenQuestionsCount); err != nil {
			return err
		}
		vs = append(vs, v)
		return nil
	})
	return vs, err
}

// keyRegion is the region key names, which bounds the rows the tallies
// read: the whole comb when key is nil or names no region.
func keyRegion(key *string) Coords {
	if key == nil {
		return Coords{}
	}
	if r, err := ParseRegionKey(*key); err == nil {
		return r
	}
	return Coords{}
}

// regionVantage is a region's row, whose staleness a refresh decides.
type regionVantage struct {
	row    *db.CombRow
	region Coords
}

// regionVantages is each vantage in vs whose key is a region key. A key that
// is no region key, such as a forager's, is never stale.
func regionVantages(vs []db.CombRow) []regionVantage {
	var out []regionVantage
	for i := range vs {
		if r, err := ParseRegionKey(vs[i].VantageKey); err == nil {
			out = append(out, regionVantage{&vs[i], r})
		}
	}
	return out
}

// decideByDigest marks each region in stale by whether the digest a refresh
// would write differs from its row, reading the regions' tallies within
// within in tx.
func decideByDigest(tx *sql.Tx, within Coords, rvs []regionVantage, stale map[string]bool) error {
	regions := make([]Coords, len(rvs))
	for i, rv := range rvs {
		regions[i] = rv.region
	}
	tallies, err := regionTallies(tx, within, regions)
	if err != nil {
		return err
	}
	dims := orderedDims()
	for _, rv := range rvs {
		stale[rv.row.VantageKey] = !sameDigest(rv.row, digestToRow(tallies[pinsOf(rv.region, dims)].digest(rv.region)))
	}
	return nil
}

// sameDigest reports whether stored holds the digest in row, a row a refresh
// would write: the same narrative, confidence, contested flag, dominant
// label, evidence count and open-question count. The coordinates follow from
// the key, and the digest method is the builder's constant.
func sameDigest(stored, row *db.CombRow) bool {
	return digestFieldsOf(stored) == digestFieldsOf(row)
}

// digestFields is the part of a comb_state row that a refresh's digest sets
// and the staleness rule compares.
type digestFields struct {
	narrative          string
	confidence         int
	contested          bool
	dominantLabel      sql.NullString
	evidenceCount      int
	openQuestionsCount int
}

func digestFieldsOf(r *db.CombRow) digestFields {
	return digestFields{r.Narrative, r.Confidence, r.Contested, r.DominantLabel, r.EvidenceCount, r.OpenQuestionsCount}
}
