package dreamer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// passSettle repairs laundering: a guarantee whose dependency chain reaches
// a finding with mss_label = 'unknown'. It walks the same transitive closure
// as the MSS audit's laundering check, through every label, so it acts on
// every chain the QMP gate halts on.
//
// The walk stops at other guarantees. The guarantee it acts on is the last
// one on each chain before the unknown: demoting it clears its dependencies,
// which cuts the chain for every guarantee above it, and those keep their
// label. For each such guarantee:
//
//   - emit an `alarm` signal at the guarantee's coordinates, unless one
//     already stands for it
//   - if --apply: demote the guarantee to assumption (clearing deps),
//     whether or not the alarm was emitted by an earlier run
//
// A demotion that moves a region's digest makes that region read stale by
// the Comb's staleness rule (comb.md § Staleness). Each alarm and demotion
// is written as the pass goes, so a pass cut by its deadline keeps those it
// made.
func passSettle(ctx context.Context, store *db.Store, opts Options) (Result, error) {
	res, _, err := settle(ctx, store.ReadConn(), store.WriteDB, opts)
	return res, err
}

// SettleInTx is the settle pass with apply, inside tx: `chb hive next
// --apply` runs it for the plan's fix_mss action, so its demotions and
// alarms commit or roll back with the scan. It returns the ids of the
// guarantees it demoted.
func SettleInTx(ctx context.Context, tx *db.Tx) ([]int64, error) {
	opts := DefaultOptions()
	opts.Apply = true
	_, demoted, err := settle(ctx, tx, tx, opts)
	return demoted, err
}

// settleReader is what settle reads through: the read pool, or the Tx.
type settleReader interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	rowQuerier
}

// settle is the pass, reading through read and writing through write: the
// store's pools, or one transaction for both. It also returns the ids of the
// guarantees it demoted.
func settle(ctx context.Context, read settleReader, write db.Conn, opts Options) (Result, []int64, error) {
	g, err := readSettleGraph(ctx, read)
	if err != nil {
		return Result{Status: "failed"}, nil, err
	}
	if len(g.guarantees) == 0 {
		return Result{Status: "completed", Notes: map[string]any{"guarantees": 0}}, nil, nil
	}
	s := &settler{read: read, write: write, opts: opts, nodes: g.nodes, scanned: len(g.guarantees)}
	return s.walk(ctx, g.guarantees)
}

// settleNode is one finding in the dependency graph settle walks.
type settleNode struct {
	label          string
	deps           []int64
	d1, d2, d3, d4 sql.NullInt64
}

// settleGraph is the dependency graph settle walks: every finding by id,
// and the guarantees with dependencies, in id order.
type settleGraph struct {
	nodes      map[int64]*settleNode
	guarantees []int64
}

// readSettleGraph reads every finding into the graph settle walks.
func readSettleGraph(ctx context.Context, read settleReader) (*settleGraph, error) {
	rows, err := read.QueryContext(ctx, `
		SELECT id, mss_label, depends_on_ids, d1, d2, d3, d4
		FROM findings
		ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("scan findings: %w", err)
	}
	defer rows.Close()
	g := &settleGraph{nodes: map[int64]*settleNode{}}
	if err := scanRows(rows, g.add); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan findings: %w", err)
	}
	return g, nil
}

// add scans one finding from rows into the graph. A NULL depends_on_ids
// scans as the empty string, so a guarantee with dependencies is one whose
// column is not empty.
func (g *settleGraph) add(rows *sql.Rows) error {
	var (
		id   int64
		n    settleNode
		deps sql.NullString
	)
	if err := rows.Scan(&id, &n.label, &deps, &n.d1, &n.d2, &n.d3, &n.d4); err != nil {
		return err
	}
	n.deps = parseDeps(deps)
	g.nodes[id] = &n
	if n.label == "guarantee" && deps.String != "" {
		g.guarantees = append(g.guarantees, id)
	}
	return nil
}

// parseDeps reads a depends_on_ids column, ignoring a decode error as the
// walk always has; NULL holds no dependencies.
func parseDeps(deps sql.NullString) []int64 {
	var ids []int64
	if deps.Valid {
		_ = json.Unmarshal([]byte(deps.String), &ids)
	}
	return ids
}

// settler is one settle pass over the guarantees: what it reads and writes
// through, and what it has done so far.
type settler struct {
	read    settleReader
	write   db.Conn
	opts    Options
	nodes   map[int64]*settleNode
	scanned int
	emitted int
	acted   []int64
	demoted []int64
}

// walk visits the guarantees in id order while the pass may act on another,
// and reports the pass.
func (s *settler) walk(ctx context.Context, guarantees []int64) (Result, []int64, error) {
	for _, gid := range guarantees {
		ok, err := admit(ctx, len(s.acted), s.opts.MaxPerPass)
		if !ok {
			return s.report(err)
		}
		if err := s.visit(gid); err != nil {
			return Result{Status: "failed"}, nil, err
		}
	}
	return s.report(nil)
}

// visit acts on guarantee gid when the pass repairs it: it counts as acted
// on, and unless the run is dry it is repaired.
func (s *settler) visit(gid int64) error {
	act, flagged, err := s.needsRepair(gid)
	if err != nil || !act {
		return err
	}
	s.acted = append(s.acted, gid)
	if s.opts.DryRun {
		return nil
	}
	return s.repair(gid, flagged)
}

// needsRepair reports whether the pass acts on guarantee gid, a guarantee
// whose chain reaches an unknown, and whether an alarm already stands for
// it. An earlier alarm stops a second alarm, not the demotion, so a
// guarantee flagged by a recommend-only run — every swarm's dreamer node is
// one — is still demoted under --apply.
func (s *settler) needsRepair(gid int64) (act, flagged bool, err error) {
	if !reachesUnknown(s.nodes, s.nodes[gid].deps) {
		return false, false, nil
	}
	flagged, err = alarmAlreadyOpenFor(s.read, gid)
	if err != nil {
		return false, false, err
	}
	return !flagged || s.opts.Apply, flagged, nil
}

// repair emits guarantee gid's alarm unless one already stands, and under
// --apply demotes it.
func (s *settler) repair(gid int64, flagged bool) error {
	if !flagged {
		if err := s.alarm(gid); err != nil {
			return err
		}
	}
	if s.opts.Apply {
		return s.demote(gid)
	}
	return nil
}

// alarm emits the `alarm` signal at guarantee gid's coordinates.
func (s *settler) alarm(gid int64) error {
	g := s.nodes[gid]
	d1, d2, d3, d4 := nullInt(g.d1), nullInt(g.d2), nullInt(g.d3), nullInt(g.d4)
	if _, err := db.EmitSignalOn(s.write,
		"alarm",
		ptrString("audit"),
		&gid,
		d1, d2, d3, d4,
		map[string]any{
			"reason": "guarantee_dep_unknown",
			"deps":   g.deps,
			"source": "dreamer.settle",
		},
		nil,
	); err != nil {
		return fmt.Errorf("emit alarm: %w", err)
	}
	s.emitted++
	return nil
}

// demote relabels guarantee gid an assumption. Clearing depends_on_ids by
// setting to '[]' avoids retriggering the guarantee-needs-deps validator.
// The same write FindingsRepo.UpdateFinding makes for a label that is not a
// guarantee, stamp included.
func (s *settler) demote(gid int64) error {
	if _, err := s.write.Exec(
		`UPDATE findings SET updated_at = CURRENT_TIMESTAMP, mss_label = 'assumption', depends_on_ids = '[]' WHERE id = ?`,
		gid,
	); err != nil {
		return fmt.Errorf("demote guarantee %d: %w", gid, err)
	}
	s.demoted = append(s.demoted, gid)
	return nil
}

// report is the pass's outcome once its walk stops: failed with the rows
// already acted on when cut is the deadline's error, else complete.
func (s *settler) report(cut error) (Result, []int64, error) {
	if cut != nil {
		return Result{Status: "failed", Touched: len(s.acted)}, s.demoted, cut
	}
	return Result{
		Status:  passStatus(s.opts),
		Touched: len(s.acted),
		Notes: map[string]any{
			"guarantees_scanned": s.scanned,
			"emitted":            s.emitted,
			"demoted":            len(s.demoted),
			"applied":            s.opts.Apply,
		},
	}, s.demoted, nil
}

// reachesUnknown reports whether an unknown finding is reachable from deps
// without passing through another guarantee. Past a guarantee the chain is
// that guarantee's to repair.
func reachesUnknown(nodes map[int64]*settleNode, deps []int64) bool {
	visited := map[int64]bool{}
	queue := append([]int64(nil), deps...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		onward, unknown := walkOn(nodes[id])
		if unknown {
			return true
		}
		queue = append(queue, onward...)
	}
	return false
}

// walkOn is where reachesUnknown goes from node n, and whether n is the
// unknown it looks for. A missing finding and another guarantee lead
// nowhere.
func walkOn(n *settleNode) (onward []int64, unknown bool) {
	if n == nil || n.label == "guarantee" {
		return nil, false
	}
	return n.deps, n.label == "unknown"
}

func alarmAlreadyOpenFor(read rowQuerier, findingID int64) (bool, error) {
	// source_id only means a finding id for some source types. settle emits
	// alarms with source_type='audit' and the hive emits them with
	// source_type='conflict', where source_id is a *conflict* id — so leaving
	// source_type unconstrained let an alarm on conflict 42 suppress a real
	// alarm on finding 42. Two id spaces, one column.
	//
	// reprove's alarm says a dependency changed, not that the chain reaches
	// an unknown, so it does not stand in for settle's.
	var sentinel int
	err := read.QueryRow(`
		SELECT 1 FROM signals
		WHERE signal_type = 'alarm'
		  AND source_type IN ('finding','audit')
		  AND source_id = ?
		  AND COALESCE(json_extract(payload_json, '$.source'), '') != 'dreamer.reprove'
		LIMIT 1`, findingID,
	).Scan(&sentinel)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sentinel == 1, nil
}
