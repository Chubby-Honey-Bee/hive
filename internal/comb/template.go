package comb

import (
	"fmt"
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Resolve returns the narrative for the given vantage key. Two forms:
//
//	region key       e.g. "" / "d1=0" / "d1=0;d2=3"
//	                 Falls back to the most-specific covering region
//	                 (so "d1=0;d2=3;d3=2" with no exact row resolves
//	                 to the d1=0;d2=3 row, then to d1=0, etc.).
//	forager:<name>    e.g. "forager:optimist"
//	                 Direct lookup; no fallback (a forager either has a
//	                 verdict at this vantage or it doesn't).
//
// The line ends ` [stale]` when the vantage it resolves to is stale by
// comb.md § Staleness, as every Comb surface reports it.
//
// Misses, malformed keys, and DB errors all resolve to the empty string
// — template substitution must never fail dispatch. A malformed key
// emits a one-line warning to stderr so problems are visible without
// breaking the run.
func Resolve(store *db.Store, vantageKey string) string {
	vantageKey = strings.TrimSpace(vantageKey)

	// Forager vantage — direct lookup, no covering walk.
	if IsForagerVantage(vantageKey) {
		row, err := store.Comb().Get(vantageKey)
		return foundLine(store, row, err)
	}

	// Region vantage — parse + covering walk.
	target, err := ParseRegionKey(vantageKey)
	if err != nil {
		warn(err)
		return ""
	}
	row, err := nearestRow(store, target)
	return foundLine(store, row, err)
}

// nearestRow is the row of the most specific region covering target that
// has one, or nil when none has.
func nearestRow(store *db.Store, target Coords) (*db.CombRow, error) {
	for _, region := range CoveringRegions(target) {
		row, err := store.Comb().Get(RegionKey(region))
		if err != nil || row != nil {
			return row, err
		}
	}
	return nil, nil
}

// foundLine is the line of a row a lookup found: empty for a miss, and
// empty with a warning when the lookup failed.
func foundLine(store *db.Store, row *db.CombRow, err error) string {
	if err != nil {
		warn(err)
		return ""
	}
	if row == nil {
		return ""
	}
	return line(store, row)
}

// warn writes a template warning to stderr: substitution never fails
// dispatch, so a problem is visible without breaking the run.
func warn(err error) {
	fmt.Fprintf(os.Stderr, "[comb] template warning: %v\n", err)
}

// line is row as the token renders it, marked ` [stale]` when the vantage is
// stale by comb.md § Staleness. It is empty, with a warning, when the
// staleness cannot be read.
func line(store *db.Store, row *db.CombRow) string {
	stale, err := vantageStaleness(store.ReadConn(), &row.VantageKey)
	if err != nil {
		warn(err)
		return ""
	}
	return row.Line(stale[row.VantageKey]) + calibratedSuffix(store, row)
}

// calibratedSuffix is ` [calibrated confidence N%]` when the row's
// dominant label has a calibrated score in the region's scopes (comb.md §
// Calibrated confidence), else empty. It is empty, with a warning, when
// the scores cannot be read; a forager vantage has no dominant label.
func calibratedSuffix(store *db.Store, row *db.CombRow) string {
	if !row.DominantLabel.Valid {
		return ""
	}
	scores, err := store.Calibration().ListScores(calibration.KindLabel, nil)
	if err != nil {
		warn(err)
		return ""
	}
	label := row.DominantLabel.String
	v, ok := calibration.CalibratedConfidence(row.Confidence, label,
		calibration.ScoresFrom(scores).LabelScore(label, calibration.ScopesFor(row.D1, row.D2)))
	if !ok {
		return ""
	}
	return fmt.Sprintf(" [calibrated confidence %d%%]", v)
}

// stripJSONFence removes a leading ```json / trailing ``` markdown fence
// (foragers often wrap their JSON output in one).
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
