package hive

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// Gain control bounds — chosen as Definitions, not Guarantees: these are the
// design envelopes for the bee-colony control loop. Rationale:
//   - BatchSizeMin=2 because a single agent can't disagree with itself, so
//     quorum/convergence detection requires at least two parallel dispatches.
//   - BatchSizeMax=20 caps token spend per wave at roughly $1 of Sonnet usage
//     (empirical Q1 2026 pricing); above that, increase ConvergenceThreshold
//     instead of fanning further.
//   - ConvergenceMin=2 mirrors BatchSizeMin (you cannot converge with one
//     observation).
//   - ConvergenceMax=10 is a soft asymptote: in production runs the marginal
//     value of an 11th corroborating finding has been below noise.
//
// These bounds are tunable per project via hive_state columns; they are not
// global guarantees of system behaviour, only defaults.
const (
	BatchSizeMin   = 2
	BatchSizeMax   = 20
	ConvergenceMin = 2
	ConvergenceMax = 10
)

// ModelTiers are the cost/capability tiers the gain controller can promote/demote
// the swarm to. Order matters: index 0 is cheapest, len-1 is most capable.
// They come from the models config (hive_tiers), or from the routing
// profile HIVE_PROFILE names, which agent-run --profile sets for the
// commands it runs (models.Config.HiveLadder).
func ModelTiers() []string {
	return models.Load().HiveLadder(os.Getenv("HIVE_PROFILE"))
}

// defaultModelTier is the tier a hive starts at, and the one an unknown
// model_tier is stored as: the tier rule's start rung (tierPolicyFrom),
// which is sonnet on the shipped ladder.
func defaultModelTier() string {
	p := activeTierPolicy()
	return p.ladder[p.start]
}

// HiveState represents the hive_state row for a project.
type HiveState struct {
	Project               string
	Iteration             int
	Phase                 string
	BatchSize             int
	ConvergenceThreshold  int
	ModelTier             string
	ConflictRateThreshold float64
	TotalFindings         int
	UnresolvedGaps        int
	UnresolvedConflicts   int
	MSSIntegrity          string
	LabelSkew             float64
	TerminalReason        *string
}

// State is the full scanned state of a project — the sensory input for the hive.
type State struct {
	Hive                  HiveState
	TotalFindings         int
	LatestWave            int
	Labels                map[string]int
	LatestWaveLabels      map[string]int // label counts of the latest wave's findings
	MSSIntegrity          string
	LaunderingViolations  int
	UntraceableGuarantees int
	UnresolvedGaps        []map[string]any
	CriticalGaps          []map[string]any
	UnresolvedConflicts   []map[string]any
	UnansweredFollowups   []map[string]any
	PendingSignals        []map[string]any
	RunningAgents         []map[string]any
	WaveGates             []map[string]any
	LatestEval            map[string]any
	Convergence           map[string]int
	LabelSkew             float64
	ConflictRate          float64
	TierClock             TierClock // the model-tier rule's clock (decideTier)
}

// ScanState reads the full project state from DB.
func ScanState(store *db.Store, project string) (*State, error) {
	h, err := readHiveState(store.ReadDB, project)
	if err != nil {
		return nil, err
	}
	s := &State{
		Hive:             h,
		Labels:           make(map[string]int),
		LatestWaveLabels: make(map[string]int),
		Convergence:      make(map[string]int),
	}
	if err := s.readFindings(store); err != nil {
		return nil, err
	}
	s.readOpenWork(store.ReadDB)
	if err := s.readDerived(store.ReadDB, project); err != nil {
		return nil, err
	}
	return s, nil
}

// readHiveState reads the project's hive_state row.
func readHiveState(rdb *sql.DB, project string) (HiveState, error) {
	var h HiveState
	h.Project = project
	err := rdb.QueryRow(
		"SELECT iteration, phase, batch_size, convergence_threshold, model_tier, conflict_rate_threshold, total_findings, unresolved_gaps, unresolved_conflicts, mss_integrity, label_skew, terminal_reason FROM hive_state WHERE project=?",
		project,
	).Scan(&h.Iteration, &h.Phase, &h.BatchSize, &h.ConvergenceThreshold, &h.ModelTier,
		&h.ConflictRateThreshold, &h.TotalFindings, &h.UnresolvedGaps, &h.UnresolvedConflicts,
		&h.MSSIntegrity, &h.LabelSkew, &h.TerminalReason)
	if err != nil {
		return h, stateReadError(project, err)
	}
	return h, nil
}

// readFindings reads the finding counts, the label counts and the MSS
// audit.
func (s *State) readFindings(store *db.Store) error {
	rdb := store.ReadDB
	rdb.QueryRow("SELECT COUNT(*) FROM findings").Scan(&s.TotalFindings)
	rdb.QueryRow("SELECT COALESCE(MAX(wave), 0) FROM findings").Scan(&s.LatestWave)
	if err := s.readLabels(rdb); err != nil {
		return err
	}
	return s.readAudit(store)
}

// readLabels counts the findings per MSS label, overall and in the latest
// wave.
func (s *State) readLabels(rdb *sql.DB) error {
	if err := scanGroupCounts(rdb, "SELECT mss_label, COUNT(*) FROM findings GROUP BY mss_label", s.Labels); err != nil {
		return fmt.Errorf("read hive metrics: %w", err)
	}
	if err := scanGroupCounts(rdb, "SELECT mss_label, COUNT(*) FROM findings WHERE wave = ? GROUP BY mss_label", s.LatestWaveLabels, s.LatestWave); err != nil {
		return fmt.Errorf("read hive metrics: %w", err)
	}
	return nil
}

// readAudit runs the MSS audit — the same audit the gate runs, not a weaker
// one. The audit walks the dependency graph transitively, so
// `guarantee → guarantee → unknown` — the exact chain the transitive scan
// exists for — fails here as it fails at the gate: one integrity verdict per
// database.
func (s *State) readAudit(store *db.Store) error {
	audit, err := store.MSSAudit()
	if err != nil {
		return fmt.Errorf("mss audit: %w", err)
	}
	s.LaunderingViolations = len(audit.LaunderingViolations)
	s.UntraceableGuarantees = len(audit.UntraceableGuarantees)
	s.MSSIntegrity = audit.Integrity
	return nil
}

// readOpenWork reads the open gaps, conflicts, followups and signals, the
// running agents, the wave gates and the latest evaluation.
func (s *State) readOpenWork(rdb *sql.DB) {
	s.UnresolvedGaps = queryToMaps(rdb, "SELECT * FROM gaps WHERE resolved_by_wave IS NULL ORDER BY priority")
	s.CriticalGaps = criticalGaps(s.UnresolvedGaps)
	s.UnresolvedConflicts = queryToMaps(rdb, "SELECT * FROM conflicts WHERE resolution IS NULL")
	s.UnansweredFollowups = queryToMaps(rdb, "SELECT * FROM followups WHERE answered=0 ORDER BY priority")
	s.PendingSignals = queryToMaps(rdb, "SELECT * FROM signals WHERE acted_on=0 ORDER BY created_at")
	s.RunningAgents = queryToMaps(rdb, "SELECT * FROM agent_runs WHERE status='running'")
	s.WaveGates = queryToMaps(rdb, "SELECT * FROM wave_gates ORDER BY wave")
	s.LatestEval = firstRow(queryToMaps(rdb, "SELECT * FROM evaluations ORDER BY id DESC LIMIT 1"))
}

// criticalGaps are the gaps of critical priority.
func criticalGaps(gaps []map[string]any) []map[string]any {
	var critical []map[string]any
	for _, g := range gaps {
		if g["priority"] == "critical" {
			critical = append(critical, g)
		}
	}
	return critical
}

// firstRow is the first row, or nil when there is none.
func firstRow(rows []map[string]any) map[string]any {
	if len(rows) > 0 {
		return rows[0]
	}
	return nil
}

// readDerived reads the convergence counts and the tier clock, and derives
// the label skew and the conflict rate.
func (s *State) readDerived(rdb *sql.DB, project string) error {
	if err := scanGroupCounts(rdb, "SELECT convergence_level, COUNT(*) FROM findings GROUP BY convergence_level", s.Convergence); err != nil {
		return fmt.Errorf("read hive metrics: %w", err)
	}
	s.LabelSkew = labelSkew(s.Labels, s.TotalFindings)
	s.ConflictRate = conflictRate(len(s.UnresolvedConflicts), s.TotalFindings)
	return s.readTierClock(rdb, project)
}

// labelSkew is the largest label's share of the findings, 0 with none.
func labelSkew(labels map[string]int, total int) float64 {
	if total <= 0 {
		return 0
	}
	maxLabel := 0
	for _, cnt := range labels {
		maxLabel = max(maxLabel, cnt)
	}
	return float64(maxLabel) / float64(total)
}

// conflictRate is the open conflicts per finding, 0 with no findings.
func conflictRate(conflicts, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(conflicts) / float64(total)
}

// readTierClock reads the model-tier rule's clock, as the last --apply pass
// left it.
func (s *State) readTierClock(rdb *sql.DB, project string) error {
	err := rdb.QueryRow(
		`SELECT wait_scans, clear_scans FROM hive_tier_log WHERE project=? ORDER BY id DESC LIMIT 1`, project,
	).Scan(&s.TierClock.Wait, &s.TierClock.Clear)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the model-tier clock: %w", err)
	}
	return nil
}

// SignalsThrough is the highest id among the pending signals the scan read.
// An iteration consumes signals only up to it: a signal written after the
// scan was never seen by the plan, so one written while an action ran — a
// qmp halt included — stays pending for the next scan rather than being
// marked acted on unread.
func (s *State) SignalsThrough() int64 {
	var through int64
	for _, sig := range s.PendingSignals {
		if id, ok := sig["id"].(int64); ok && id > through {
			through = id
		}
	}
	return through
}

// consumeSignals marks the pending signals at or below through as acted on,
// and returns how many it marked.
func consumeSignals(ex execer, through int64) (int64, error) {
	res, err := ex.Exec(`UPDATE signals SET acted_on=1 WHERE acted_on=0 AND id <= ?`, through)
	if err != nil {
		return 0, fmt.Errorf("consume signals: %w", err)
	}
	return res.RowsAffected()
}

// queryToMaps delegates to the shared helper in internal/db and drops its
// error, as the helper itself used to.
func queryToMaps(rdb *sql.DB, query string, args ...any) []map[string]any {
	rows, _ := db.QueryToMaps(rdb, query, args...)
	return rows
}

// scanGroupCounts runs a "SELECT key, COUNT(*)" query into a map and returns
// every error, the query failing outright included. The counts feed label
// skew and the gain-control parameters, so a silently empty map is not a
// harmless default: it is a distribution the hive would then reason about as
// though it were measured.
func scanGroupCounts(rdb *sql.DB, query string, dest map[string]int, args ...any) error {
	rows, err := rdb.Query(query, args...)
	if err != nil {
		return fmt.Errorf("group count query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var cnt int
		if err := rows.Scan(&key, &cnt); err != nil {
			return fmt.Errorf("scan group count: %w", err)
		}
		dest[key] = cnt
	}
	return rows.Err()
}

// stateReadError is the error of a failed read of project's hive_state row:
// notInitializedError when the project has none, else the read's own error.
func stateReadError(project string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notInitializedError{project: project, cause: err}
	}
	return fmt.Errorf("read the hive state of project %q: %w", project, err)
}

// notInitializedError is the error for a project with no hive_state row. Its
// message names the command that creates one and ends there; the missing
// row, its cause, is what it wraps.
type notInitializedError struct {
	project string
	cause   error
}

func (e notInitializedError) Error() string {
	return fmt.Sprintf("hive not initialized for project %q (run: chb hive init --project %s)", e.project, e.project)
}

func (e notInitializedError) Unwrap() error { return e.cause }
