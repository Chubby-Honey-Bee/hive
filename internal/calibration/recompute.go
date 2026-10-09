package calibration

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Options steer one recompute.
type Options struct {
	// Rebuild recomputes even when no outcome was recorded since the last
	// calibrate tick.
	Rebuild bool
	// Foragers are the lens names. A finding outcome whose finding's agent
	// is one of them is credited to that lens.
	Foragers []string
	// Label names the calibrate tick; empty picks a unique one.
	Label string
}

// Drift is one drift report.
type Drift struct {
	db.ScoreKey
	// Reason is guarantee_floor (a calibrated label/guarantee score under
	// GuaranteeConfirmFloor) or hit_rate_drop (a calibrated score whose hit
	// rate fell by DriftDelta or more since its previous revision).
	Reason   string
	HitRate  float64
	Previous float64
}

// NablaScope is the ∇ report for one scope: did runs with a fired
// resonates bond reach synthesis verdicts that held more often than runs
// without one?
type NablaScope struct {
	Scope      string
	NPos       int
	HitPos     float64
	NNeg       int
	HitNeg     float64
	Weight     float64
	Calibrated bool
	// Predictive is Calibrated and Weight > 1.
	Predictive bool
}

// Result is what a recompute did.
type Result struct {
	// Skipped is true when nothing was recorded since the last calibrate
	// tick and Rebuild was off: no tick, no write.
	Skipped     bool
	SinceTickID int64
	NewOutcomes int
	TickID      int64
	// Scores is every row after the recompute, in key order.
	Scores []*db.ScoreRow
	// Changed are the rows this recompute wrote and snapshotted.
	Changed []*db.ScoreRow
	// Removed are the stored keys that have no outcome left.
	Removed              []db.ScoreKey
	Drift                []Drift
	DriftCount           int
	LowestCalibratedLens string
	Nabla                []NablaScope
}

// Recompute scores every predictor in every scope from the whole outcomes
// ledger. It is a pure function of the ledger, the fired resonates bonds
// and opts.Foragers, so two runs over the same rows write the same scores,
// and a row that did not change keeps its updated_tick_id. It opens a
// calibrate tick whose notes carry the ledger's high-water mark, upserts
// the rows that changed, deletes the rows whose key has no outcome left,
// snapshots the changed rows under the tick and reports drift. It makes no
// model call and writes no finding (CALIB-1).
func Recompute(store *db.Store, opts Options) (*Result, error) {
	res, err := sinceLastTick(store)
	if err != nil {
		return nil, err
	}
	if res.NewOutcomes == 0 && !opts.Rebuild {
		return reportStanding(store, res)
	}
	return rescore(store, opts, res)
}

// sinceLastTick starts a recompute's result: the last calibrate tick, and
// how many outcomes were recorded since it.
func sinceLastTick(store *db.Store) (*Result, error) {
	res := &Result{}
	last, err := store.TimeWheel().Recent(db.TickCalibrate, 1)
	if err != nil {
		return nil, err
	}
	if len(last) > 0 {
		res.SinceTickID = last[0].ID
	}
	fresh, err := store.Outcomes().ListSince(res.SinceTickID)
	if err != nil {
		return nil, err
	}
	res.NewOutcomes = len(fresh)
	return res, nil
}

// reportStanding completes the result of a recompute with nothing to do.
// The result still reports what stands: the guarantee-floor drift of the
// current scores and the lowest calibrated lens, which a calibrate node
// reads on every run. A hit-rate drop is reported once, by the recompute
// that saw it.
func reportStanding(store *db.Store, res *Result) (*Result, error) {
	scores, err := store.Calibration().ListScores("", nil)
	if err != nil {
		return nil, err
	}
	res.Skipped = true
	res.Scores = scores
	res.Drift = floorDrift(scores)
	res.DriftCount = len(res.Drift)
	res.LowestCalibratedLens = LowestCalibratedLens(scores, ScopeGlobal)
	return res, nil
}

// rescore opens the calibrate tick, scores the whole ledger under it and
// closes it.
func rescore(store *db.Store, opts Options, res *Result) (*Result, error) {
	// The high-water mark is read before the ledger: an outcome recorded
	// between the two is scored now and read again as new by the next run,
	// which recomputes the same values. The other order could lose one.
	maxID, err := store.Outcomes().MaxID()
	if err != nil {
		return nil, err
	}
	tickID, err := store.TimeWheel().Begin(tickLabel(opts.Label), db.TickCalibrate, 0, 0, db.CalibrateTickNotes(maxID))
	if err != nil {
		return nil, fmt.Errorf("open calibrate tick: %w", err)
	}
	res.TickID = tickID
	defer func() { _ = store.TimeWheel().End(tickID) }()
	if err := scoreUnderTick(store, opts, res); err != nil {
		return nil, err
	}
	return res, nil
}

// tickLabel is label, or a unique calibrate tick name when it is empty.
func tickLabel(label string) string {
	if label != "" {
		return label
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("calibrate-%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(suffix))
}

// scoreUnderTick folds the ledger, writes the rows that changed under
// res.TickID, and reports the scores, the drift against the previous
// revisions, the lowest calibrated lens and ∇.
func scoreUnderTick(store *db.Store, opts Options, res *Result) error {
	_, agg, err := foldLedger(store, opts.Foragers)
	if err != nil {
		return err
	}
	rows := agg.rows()
	current, previous, err := readStanding(store)
	if err != nil {
		return err
	}
	res.Changed = stampChanged(rows, current, res.TickID)
	res.Removed = removedKeys(rows, current)
	if err := writeChanges(store, res.TickID, res.Changed, res.Removed); err != nil {
		return err
	}
	res.Scores = rows
	res.Drift = drift(rows, previous)
	res.DriftCount = len(res.Drift)
	res.LowestCalibratedLens = LowestCalibratedLens(rows, ScopeGlobal)
	res.Nabla = agg.nablaReport(rows)
	return nil
}

// readStanding reads the scores as they stand and each key's latest
// revision.
func readStanding(store *db.Store) ([]*db.ScoreRow, map[db.ScoreKey]*db.ScoreRow, error) {
	current, err := store.Calibration().ListScores("", nil)
	if err != nil {
		return nil, nil, err
	}
	previous, err := store.Calibration().LatestRevisions()
	if err != nil {
		return nil, nil, err
	}
	return current, previous, nil
}

// stampChanged sets each new row's updated_tick_id: an unchanged row keeps
// its current one, and a changed row takes tickID and is returned.
func stampChanged(rows, current []*db.ScoreRow, tickID int64) []*db.ScoreRow {
	currentByKey := ScoresFrom(current)
	var changed []*db.ScoreRow
	for _, r := range rows {
		if c, ok := currentByKey[r.ScoreKey]; ok && c.Same(r) {
			r.UpdatedTickID = c.UpdatedTickID
			continue
		}
		r.UpdatedTickID = sql.NullInt64{Int64: tickID, Valid: true}
		changed = append(changed, r)
	}
	return changed
}

// removedKeys is the key of each current row with no new row.
func removedKeys(rows, current []*db.ScoreRow) []db.ScoreKey {
	newKeys := ScoresFrom(rows)
	var removed []db.ScoreKey
	for _, c := range current {
		if _, ok := newKeys[c.ScoreKey]; !ok {
			removed = append(removed, c.ScoreKey)
		}
	}
	return removed
}

// writeChanges upserts the changed rows, deletes the removed keys and
// snapshots the changed rows under tickID.
func writeChanges(store *db.Store, tickID int64, changed []*db.ScoreRow, removed []db.ScoreKey) error {
	cal := store.Calibration()
	if err := each(changed, cal.UpsertScore); err != nil {
		return err
	}
	if err := each(removed, cal.DeleteScore); err != nil {
		return err
	}
	return cal.SnapshotRevisions(tickID, changed)
}

// each calls f with every item in turn, stopping at its first error.
func each[T any](items []T, f func(T) error) error {
	for _, it := range items {
		if err := f(it); err != nil {
			return err
		}
	}
	return nil
}

// drift is the drift of each calibrated row: under the guarantee floor, and
// a hit rate that fell by DriftDelta or more since its previous revision.
func drift(rows []*db.ScoreRow, previous map[db.ScoreKey]*db.ScoreRow) []Drift {
	var out []Drift
	for _, r := range rows {
		if r.Calibrated {
			out = appendDrift(out, r, previous[r.ScoreKey])
		}
	}
	return out
}

// appendDrift appends calibrated row r's drift against its previous
// revision prev, nil when it has none.
func appendDrift(out []Drift, r, prev *db.ScoreRow) []Drift {
	if isFloorDrift(r) {
		out = append(out, Drift{ScoreKey: r.ScoreKey, Reason: "guarantee_floor", HitRate: r.HitRate})
	}
	if hitRateDropped(r, prev) {
		out = append(out, Drift{ScoreKey: r.ScoreKey, Reason: "hit_rate_drop", HitRate: r.HitRate, Previous: prev.HitRate})
	}
	return out
}

// hitRateDropped reports whether r's hit rate fell by DriftDelta or more
// since its previous revision prev, that revision calibrated too.
func hitRateDropped(r, prev *db.ScoreRow) bool {
	return prev != nil && prev.Calibrated && prev.HitRate-r.HitRate >= DriftDelta
}

// isFloorDrift reports whether a row is a calibrated label/guarantee score
// under GuaranteeConfirmFloor.
func isFloorDrift(r *db.ScoreRow) bool {
	return r.Calibrated && r.PredictorKind == KindLabel && r.PredictorKey == "guarantee" && r.HitRate < GuaranteeConfirmFloor
}

// floorDrift is the standing guarantee-floor drift of a set of scores, in
// row order.
func floorDrift(rows []*db.ScoreRow) []Drift {
	var out []Drift
	for _, r := range rows {
		if isFloorDrift(r) {
			out = append(out, Drift{ScoreKey: r.ScoreKey, Reason: "guarantee_floor", HitRate: r.HitRate})
		}
	}
	return out
}

// LowestCalibratedLens is the calibrated lens with the smallest weight in
// a scope, first by name on a tie; "" when no lens is calibrated there.
func LowestCalibratedLens(rows []*db.ScoreRow, scope string) string {
	lenses := calibratedLenses(rows, scope)
	if len(lenses) == 0 {
		return ""
	}
	best := lenses[0]
	for _, r := range lenses[1:] {
		if r.Weight < best.Weight {
			best = r
		}
	}
	return best.PredictorKey
}

// calibratedLenses is the calibrated lens rows in scope, in row order.
func calibratedLenses(rows []*db.ScoreRow, scope string) []*db.ScoreRow {
	var out []*db.ScoreRow
	for _, r := range rows {
		if isCalibratedLensIn(r, scope) {
			out = append(out, r)
		}
	}
	return out
}

// isCalibratedLensIn reports whether r is a calibrated lens score in scope.
func isCalibratedLensIn(r *db.ScoreRow, scope string) bool {
	return r.PredictorKind == KindLens && r.ScopeKey == scope && r.Calibrated
}
