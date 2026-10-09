package calibration

import (
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// BundleFormat names the export's shape. A merge refuses any other.
const BundleFormat = "hive-calibration-counts/1"

// Bundle is one workspace's calibration counts, keyed as the scores are
// and carrying no derived value: no hit rate, weight or calibrated flag.
// It holds every key, the scopes under the floor included, and each
// scope's outcome total, so a merge decides eligibility over the union and
// re-runs the formula. Merging counts, not weights, keeps the merge
// commutative and associative.
type Bundle struct {
	Format           string         `json:"format"`
	Source           string         `json:"source"`
	ExportedAt       string         `json:"exported_at"`
	Outcomes         int            `json:"outcomes"`
	ThroughOutcomeID int64          `json:"through_outcome_id"`
	ScopeTotals      map[string]int `json:"scope_totals"`
	Counts           []CountRow     `json:"counts"`
}

// CountRow is one predictor's counts in one scope: the formula's input.
type CountRow struct {
	PredictorKind string  `json:"predictor_kind"`
	PredictorKey  string  `json:"predictor_key"`
	ScopeKey      string  `json:"scope_key"`
	Confirmed     int     `json:"confirmed"`
	Partial       int     `json:"partial"`
	Refuted       int     `json:"refuted"`
	BrierSum      float64 `json:"brier_sum"`
	BrierN        int     `json:"brier_n"`
}

func (c CountRow) key() db.ScoreKey {
	return db.ScoreKey{PredictorKind: c.PredictorKind, PredictorKey: c.PredictorKey, ScopeKey: c.ScopeKey}
}

func (c CountRow) counts() Counts {
	return Counts{Confirmed: c.Confirmed, Partial: c.Partial, Refuted: c.Refuted, BrierSum: c.BrierSum, BrierN: c.BrierN}
}

// Export folds the whole ledger as Recompute does, with the same lens
// attribution and ∇ runs, and emits the counts. It opens no tick and
// writes nothing. source names the database.
func Export(store *db.Store, source string, foragers []string) (*Bundle, error) {
	ledger, agg, err := foldLedger(store, foragers)
	if err != nil {
		return nil, err
	}
	maxID, err := store.Outcomes().MaxID()
	if err != nil {
		return nil, err
	}
	return &Bundle{
		Format:           BundleFormat,
		Source:           source,
		ExportedAt:       time.Now().UTC().Format(time.RFC3339),
		Outcomes:         len(ledger),
		ThroughOutcomeID: maxID,
		ScopeTotals:      maps.Clone(agg.scopeTotal),
		Counts:           agg.countRows(),
	}, nil
}

// countRows is every count in the aggregation as a CountRow, in key order.
func (a *aggregation) countRows() []CountRow {
	out := make([]CountRow, 0, len(a.counts))
	for k, c := range a.counts {
		out = append(out, CountRow{
			PredictorKind: k.PredictorKind, PredictorKey: k.PredictorKey, ScopeKey: k.ScopeKey,
			Confirmed: c.Confirmed, Partial: c.Partial, Refuted: c.Refuted, BrierSum: c.BrierSum, BrierN: c.BrierN,
		})
	}
	sort.Slice(out, func(i, j int) bool { return lessKey(out[i].key(), out[j].key()) })
	return out
}

// Merge sums the bundles' counts and scope totals, key by key, rebuilds the
// pools each reference rate is drawn from, and applies the formula over the
// union: the scores a recompute over the joined ledgers would write, the
// tick aside. Integer counts sum exactly; a Brier sum is a float summed in
// input order, so it is equal across orders up to rounding and enters no
// weight. It refuses a bundle of another format, a count row whose kind is
// not one of the four, a key a bundle lists twice, and a scope whose ∇
// count exceeds its synthesis count.
func Merge(bundles ...*Bundle) ([]*db.ScoreRow, error) {
	m := &merger{counts: map[db.ScoreKey]*Counts{}, totals: map[string]int{}}
	for i, b := range bundles {
		if err := m.add(i+1, b); err != nil {
			return nil, err
		}
	}
	agg, err := aggregationFromCounts(m.counts, m.totals)
	if err != nil {
		return nil, err
	}
	return agg.rows(), nil
}

// merger sums bundles' counts, key by key, and their scope totals.
type merger struct {
	counts map[db.ScoreKey]*Counts
	totals map[string]int
}

// add sums in bundle b, the n-th of the merge counting from 1.
func (m *merger) add(n int, b *Bundle) error {
	if err := checkBundle(n, b); err != nil {
		return err
	}
	if err := m.addCounts(n, b); err != nil {
		return err
	}
	for scope, total := range b.ScopeTotals {
		m.totals[scope] += total
	}
	return nil
}

// checkBundle refuses an empty bundle and a bundle of another format.
func checkBundle(n int, b *Bundle) error {
	if b == nil {
		return fmt.Errorf("bundle %d is empty", n)
	}
	if b.Format != BundleFormat {
		return fmt.Errorf("bundle %d (%s): format %q is not %q", n, b.Source, b.Format, BundleFormat)
	}
	return nil
}

// addCounts sums in the count rows of bundle b.
func (m *merger) addCounts(n int, b *Bundle) error {
	seen := make(map[db.ScoreKey]bool, len(b.Counts))
	for _, row := range b.Counts {
		k := row.key()
		if err := checkCountRow(n, b.Source, k, seen); err != nil {
			return err
		}
		seen[k] = true
		m.countsAt(k).Merge(row.counts())
	}
	return nil
}

// checkCountRow refuses a count row of bundle n whose kind is not one of
// the four, and a key the bundle lists twice: seen holds the keys it
// listed before.
func checkCountRow(n int, source string, k db.ScoreKey, seen map[db.ScoreKey]bool) error {
	if !validKind(k.PredictorKind) {
		return fmt.Errorf("bundle %d (%s): predictor kind %q is not lens, label, convergence or synthesizer", n, source, k.PredictorKind)
	}
	if seen[k] {
		return fmt.Errorf("bundle %d (%s): %s/%s@%q is listed twice", n, source, k.PredictorKind, k.PredictorKey, k.ScopeKey)
	}
	return nil
}

// countsAt is the summed counts of key k, added empty when it has none yet.
func (m *merger) countsAt(k db.ScoreKey) *Counts {
	c, ok := m.counts[k]
	if !ok {
		c = &Counts{}
		m.counts[k] = c
	}
	return c
}

// aggregationFromCounts rebuilds an aggregation from summed counts: the
// lens pool is the sum of the lens keys in a scope, the synthesis pool is
// the queen's row there, and the no-∇ pool is the queen's row less the
// nabla row. Only hits and n enter a reference rate, so the pools' Brier
// fields are not rebuilt.
func aggregationFromCounts(counts map[db.ScoreKey]*Counts, totals map[string]int) (*aggregation, error) {
	a := newAggregation(counts, totals)
	a.poolsFromCounts()
	if err := a.noNablaFromCounts(); err != nil {
		return nil, err
	}
	if err := a.checkNablaRows(); err != nil {
		return nil, err
	}
	return a, nil
}

// poolsFromCounts sums each scope's lens rows into its lens pool and its
// queen's row into its synthesis pool.
func (a *aggregation) poolsFromCounts() {
	for k, c := range a.counts {
		switch k.PredictorKind {
		case KindLens:
			pool(a.lensPool, k.ScopeKey).Merge(c.resolutions())
		case KindSynthesizer:
			pool(a.queenPool, k.ScopeKey).Merge(c.resolutions())
		}
	}
}

// noNablaFromCounts sets each scope's no-∇ pool to its synthesis pool less
// its nabla row.
func (a *aggregation) noNablaFromCounts() error {
	for scope, queen := range a.queenPool {
		neg, err := a.lessNabla(scope, queen.resolutions())
		if err != nil {
			return err
		}
		a.noNabla[scope] = &neg
	}
	return nil
}

// lessNabla is neg less scope's nabla row, when it has one. It refuses a
// scope with more ∇ synthesis verdicts than synthesis verdicts.
func (a *aggregation) lessNabla(scope string, neg Counts) (Counts, error) {
	nabla, ok := a.counts[db.ScoreKey{PredictorKind: KindConvergence, PredictorKey: KeyNabla, ScopeKey: scope}]
	if !ok {
		return neg, nil
	}
	neg.Confirmed -= nabla.Confirmed
	neg.Partial -= nabla.Partial
	neg.Refuted -= nabla.Refuted
	if neg.anyNegative() {
		return Counts{}, fmt.Errorf("scope %q: more ∇ synthesis verdicts than synthesis verdicts", scope)
	}
	return neg, nil
}

// checkNablaRows refuses a ∇ row in a scope with no synthesis row.
func (a *aggregation) checkNablaRows() error {
	for k := range a.counts {
		if _, ok := a.queenPool[k.ScopeKey]; k.PredictorKind == KindConvergence && !ok {
			return fmt.Errorf("scope %q: a ∇ row with no synthesis row", k.ScopeKey)
		}
	}
	return nil
}

func lessKey(a, b db.ScoreKey) bool {
	if a.PredictorKind != b.PredictorKind {
		return a.PredictorKind < b.PredictorKind
	}
	if a.PredictorKey != b.PredictorKey {
		return a.PredictorKey < b.PredictorKey
	}
	return a.ScopeKey < b.ScopeKey
}
