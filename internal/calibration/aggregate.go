package calibration

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// aggregation is the ledger folded per predictor and scope, with the pools
// each kind's reference rate is drawn from.
type aggregation struct {
	counts     map[db.ScoreKey]*Counts
	lensPool   map[string]*Counts // per scope: every lens outcome
	queenPool  map[string]*Counts // per scope: every synthesis verdict
	noNabla    map[string]*Counts // per scope: synthesis verdicts of runs with no ∇
	scopeTotal map[string]int     // per scope: outcomes of any kind
}

// newAggregation is an aggregation of counts and scope totals whose pools
// are empty.
func newAggregation(counts map[db.ScoreKey]*Counts, totals map[string]int) *aggregation {
	return &aggregation{
		counts:     counts,
		lensPool:   map[string]*Counts{},
		queenPool:  map[string]*Counts{},
		noNabla:    map[string]*Counts{},
		scopeTotal: totals,
	}
}

// foldLedger reads the whole ledger, each finding's agent and the ∇ runs,
// and folds them, crediting lens outcomes to foragers. It returns the
// ledger with its aggregation.
func foldLedger(store *db.Store, foragers []string) ([]*db.OutcomeRow, *aggregation, error) {
	ledger, err := store.Outcomes().ListAll()
	if err != nil {
		return nil, nil, err
	}
	agents, err := findingAgents(store.ReadDB)
	if err != nil {
		return nil, nil, err
	}
	nablaRuns, err := runsWithFiredResonance(store.ReadDB)
	if err != nil {
		return nil, nil, err
	}
	return ledger, aggregate(ledger, agents, nablaRuns, foragers), nil
}

func aggregate(ledger []*db.OutcomeRow, agents map[int64]string, nablaRuns map[int64]bool, foragers []string) *aggregation {
	isForager := make(map[string]bool, len(foragers))
	for _, f := range foragers {
		isForager[f] = true
	}
	f := &ledgerFold{
		a:         newAggregation(map[db.ScoreKey]*Counts{}, map[string]int{}),
		agents:    agents,
		isForager: isForager,
		nablaRuns: nablaRuns,
	}
	for _, o := range ledger {
		f.fold(o)
	}
	return f.a
}

// ledgerFold is what aggregate folds each outcome with: the aggregation so
// far, each finding's agent, the forager names and the runs with a ∇.
type ledgerFold struct {
	a         *aggregation
	agents    map[int64]string
	isForager map[string]bool
	nablaRuns map[int64]bool
}

// fold adds outcome o to every scope it falls in.
func (f *ledgerFold) fold(o *db.OutcomeRow) {
	conf := statedConfidence(o)
	for _, scope := range scopesOf(o) {
		f.a.scopeTotal[scope]++
		f.foldIn(o, scope, conf)
	}
}

// foldIn credits o, which stated confidence conf, to the predictors of its
// subject in scope.
func (f *ledgerFold) foldIn(o *db.OutcomeRow, scope string, conf *int) {
	switch o.SubjectKind {
	case SubjectFinding:
		f.foldFinding(o, scope, conf)
	case SubjectLensVerdict:
		f.foldLens(o.Lens.String, scope, o.Resolution, conf)
	case SubjectSynthesisVerdict:
		f.foldSynthesis(o, scope, conf)
	}
}

// foldFinding credits a finding outcome to its label, and to its agent's
// lens when the agent is a forager.
func (f *ledgerFold) foldFinding(o *db.OutcomeRow, scope string, conf *int) {
	f.a.add(db.ScoreKey{PredictorKind: KindLabel, PredictorKey: o.SubjectLabel.String, ScopeKey: scope}, o.Resolution, conf)
	if agent := f.agents[o.FindingID.Int64]; f.isForager[agent] {
		f.foldLens(agent, scope, o.Resolution, conf)
	}
}

// foldLens credits a resolution to lens and to the scope's lens pool.
func (f *ledgerFold) foldLens(lens, scope, resolution string, conf *int) {
	f.a.add(db.ScoreKey{PredictorKind: KindLens, PredictorKey: lens, ScopeKey: scope}, resolution, conf)
	pool(f.a.lensPool, scope).Add(resolution, conf)
}

// foldSynthesis credits a synthesis verdict to the Queen and the scope's
// synthesis pool, then to ∇ when its run has one, else to the scope's no-∇
// pool.
func (f *ledgerFold) foldSynthesis(o *db.OutcomeRow, scope string, conf *int) {
	f.a.add(db.ScoreKey{PredictorKind: KindSynthesizer, PredictorKey: KeyQueen, ScopeKey: scope}, o.Resolution, conf)
	pool(f.a.queenPool, scope).Add(o.Resolution, conf)
	if f.nablaRuns[o.RunID.Int64] {
		f.a.add(db.ScoreKey{PredictorKind: KindConvergence, PredictorKey: KeyNabla, ScopeKey: scope}, o.Resolution, conf)
	} else {
		pool(f.a.noNabla, scope).Add(o.Resolution, conf)
	}
}

// statedConfidence is o's stated confidence, nil when it stated none.
func statedConfidence(o *db.OutcomeRow) *int {
	if !o.StatedConfidence.Valid {
		return nil
	}
	v := int(o.StatedConfidence.Int64)
	return &v
}

func (a *aggregation) add(k db.ScoreKey, resolution string, conf *int) {
	c, ok := a.counts[k]
	if !ok {
		c = &Counts{}
		a.counts[k] = c
	}
	c.Add(resolution, conf)
}

func pool(m map[string]*Counts, scope string) *Counts {
	c, ok := m[scope]
	if !ok {
		c = &Counts{}
		m[scope] = c
	}
	return c
}

// eligible reports whether a scope is scored: the global scope always, a
// prefix when it holds at least NFloor outcomes.
func (a *aggregation) eligible(scope string) bool {
	return scope == ScopeGlobal || a.scopeTotal[scope] >= NFloor
}

// reference is p̄ for a key: the pooled posterior mean of the kind's pool
// in the scope, or the label's target.
func (a *aggregation) reference(k db.ScoreKey) float64 {
	switch k.PredictorKind {
	case KindLabel:
		return LabelTargets[k.PredictorKey]
	case KindLens:
		return poolRate(a.lensPool, k.ScopeKey)
	case KindSynthesizer:
		return poolRate(a.queenPool, k.ScopeKey)
	default:
		return poolRate(a.noNabla, k.ScopeKey)
	}
}

func poolRate(m map[string]*Counts, scope string) float64 {
	if c, ok := m[scope]; ok {
		return c.PHat()
	}
	return Counts{}.PHat()
}

// rows applies the formula to every eligible key, in key order.
func (a *aggregation) rows() []*db.ScoreRow {
	keys := a.eligibleKeys()
	out := make([]*db.ScoreRow, 0, len(keys))
	for _, k := range keys {
		out = append(out, a.row(k))
	}
	return out
}

// eligibleKeys is every key in an eligible scope, in key order.
func (a *aggregation) eligibleKeys() []db.ScoreKey {
	keys := make([]db.ScoreKey, 0, len(a.counts))
	for k := range a.counts {
		if a.eligible(k.ScopeKey) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return lessKey(keys[i], keys[j]) })
	return keys
}

// row applies the formula to key k's counts.
func (a *aggregation) row(k db.ScoreKey) *db.ScoreRow {
	c := a.counts[k]
	s := Formula(*c, a.reference(k))
	row := &db.ScoreRow{
		ScoreKey:   k,
		NResolved:  c.N(),
		NConfirmed: c.Confirmed,
		NRefuted:   c.Refuted,
		NPartial:   c.Partial,
		HitRate:    s.HitRate,
		BrierSum:   c.BrierSum,
		BrierN:     c.BrierN,
		Weight:     s.W,
		Calibrated: s.Calibrated,
	}
	if s.HasBrier {
		row.BrierScore = sql.NullFloat64{Float64: s.BrierScore, Valid: true}
	}
	return row
}

// nablaReport describes every scored convergence row against its no-∇ pool.
func (a *aggregation) nablaReport(rows []*db.ScoreRow) []NablaScope {
	var out []NablaScope
	for _, r := range rows {
		if r.PredictorKind == KindConvergence {
			out = append(out, a.nablaScope(r))
		}
	}
	return out
}

// nablaScope is the ∇ report of convergence row r against its scope's no-∇
// pool.
func (a *aggregation) nablaScope(r *db.ScoreRow) NablaScope {
	neg := Counts{}
	if c, ok := a.noNabla[r.ScopeKey]; ok {
		neg = *c
	}
	return NablaScope{
		Scope:      r.ScopeKey,
		NPos:       r.NResolved,
		HitPos:     r.HitRate,
		NNeg:       neg.N(),
		HitNeg:     neg.HitRate(),
		Weight:     r.Weight,
		Calibrated: r.Calibrated,
		Predictive: r.Calibrated && r.Weight > 1.0,
	}
}

// scopesOf lists the scopes an outcome falls in: the global scope, then
// its d1 prefix and its d1;d2 prefix when those coordinates are set.
func scopesOf(o *db.OutcomeRow) []string {
	scopes := []string{ScopeGlobal}
	if !o.D1.Valid {
		return scopes
	}
	d1 := fmt.Sprintf("d1=%d", o.D1.Int64)
	scopes = append(scopes, d1)
	if o.D2.Valid {
		scopes = append(scopes, fmt.Sprintf("%s;d2=%d", d1, o.D2.Int64))
	}
	return scopes
}

// findingAgents maps each finding with an outcome to its agent.
func findingAgents(read *sql.DB) (map[int64]string, error) {
	rows, err := read.Query(
		`SELECT f.id, f.agent FROM findings f
		 WHERE f.id IN (SELECT finding_id FROM outcomes WHERE finding_id IS NOT NULL)`)
	if err != nil {
		return nil, fmt.Errorf("finding agents: %w", err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var agent string
		if err := rows.Scan(&id, &agent); err != nil {
			return nil, err
		}
		out[id] = agent
	}
	return out, rows.Err()
}

// runsWithFiredResonance is the set of runs the ∇ sensor fired in: those
// with a fired resonates row in forager_bonds.
func runsWithFiredResonance(read *sql.DB) (map[int64]bool, error) {
	rows, err := read.Query(
		`SELECT DISTINCT run_id FROM forager_bonds
		 WHERE bond_kind = 'resonates' AND fired = 1 AND run_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("∇ runs: %w", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
