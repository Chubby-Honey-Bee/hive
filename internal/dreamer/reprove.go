package dreamer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// passReprove walks every guarantee whose dependencies have been modified
// since the guarantee was written. Each such guarantee gets an `alarm`
// signal at its coordinates with payload describing the suspect deps: a
// guarantee's foundation is suspect, which is the alarm's meaning. The
// guarantee is *not* automatically demoted — that's reserved for
// `passSettle` (which fires when deps go to `unknown`).
//
// "Modified since" means: an `alarm` or `stop_signal` signal sourced from
// the dep (a finding or an audit) post-dates the guarantee's created_at, OR
// the dep's `updated_at` does — UpdateFinding stamps it on every edit.
//
// A guarantee that rests on one flagged in this pass is flagged too. The
// pass visits guarantees dependencies first, so a dependency's alarm is
// never written after its dependent's, and a second ripen over an unchanged
// comb emits no new signal, a guarantee resting on a higher-id guarantee
// (what promote_finding produces) included.
func passReprove(ctx context.Context, store *db.Store, opts Options) (Result, error) {
	guarantees, err := readGuarantees(ctx, store.ReadConn())
	if err != nil {
		return Result{Status: "failed"}, err
	}
	if len(guarantees) == 0 {
		return Result{Status: "completed", Notes: map[string]any{"guarantees": 0}}, nil
	}
	r := &reprover{store: store, opts: opts, guarantees: guarantees, flagged: map[int64]bool{}}
	return r.walk(ctx, withDeps(guarantees, dependenciesFirst(guarantees)))
}

// guaranteeRow is one guarantee reprove reads: its dependencies, raw and
// parsed, when it was written, and its coordinates.
type guaranteeRow struct {
	id             int64
	deps           string
	depIDs         []int64
	createdAt      string
	d1, d2, d3, d4 sql.NullInt64
}

// readGuarantees reads every guarantee with a depends_on_ids column, in id
// order, its dependencies parsed.
func readGuarantees(ctx context.Context, read *sql.DB) ([]guaranteeRow, error) {
	rows, err := read.QueryContext(ctx, `
		SELECT id, depends_on_ids, created_at, d1, d2, d3, d4
		FROM findings
		WHERE mss_label = 'guarantee' AND depends_on_ids IS NOT NULL AND depends_on_ids != ''
		ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("scan guarantees: %w", err)
	}
	defer rows.Close()
	guarantees, err := collectRows(rows, scanGuarantee)
	if err != nil {
		return nil, err
	}
	// A read cut short must not pass for "no guarantees need reproving".
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan guarantees: %w", err)
	}
	return guarantees, nil
}

// scanGuarantee scans one guarantee from rows, its dependencies parsed.
func scanGuarantee(rows *sql.Rows) (guaranteeRow, error) {
	var g guaranteeRow
	if err := rows.Scan(&g.id, &g.deps, &g.createdAt, &g.d1, &g.d2, &g.d3, &g.d4); err != nil {
		return g, err
	}
	_ = json.Unmarshal([]byte(g.deps), &g.depIDs)
	return g, nil
}

// dependenciesFirst is the indexes of guarantees, each after the guarantees
// it rests on, ties in id order. A cycle, which the QMP gate halts on before
// any pass runs, is broken at the edge that closes it.
func dependenciesFirst(guarantees []guaranteeRow) []int {
	o := &depOrder{
		guarantees: guarantees,
		index:      make(map[int64]int, len(guarantees)),
		seen:       make([]bool, len(guarantees)),
		order:      make([]int, 0, len(guarantees)),
	}
	for i := range guarantees {
		o.index[guarantees[i].id] = i
	}
	for i := range guarantees {
		o.place(i)
	}
	return o.order
}

// depOrder builds dependenciesFirst's order: each guarantee's index by id,
// those already placed, and the order so far.
type depOrder struct {
	guarantees []guaranteeRow
	index      map[int64]int
	seen       []bool
	order      []int
}

// place appends guarantee i to the order after the guarantees it rests on.
func (o *depOrder) place(i int) {
	if o.seen[i] {
		return
	}
	o.seen[i] = true
	for _, dep := range o.guarantees[i].depIDs {
		if j, ok := o.index[dep]; ok {
			o.place(j)
		}
	}
	o.order = append(o.order, i)
}

// withDeps keeps the indexes in order whose guarantee rests on at least one
// dependency; reprove has nothing to check on the others.
func withDeps(guarantees []guaranteeRow, order []int) []int {
	out := make([]int, 0, len(order))
	for _, i := range order {
		if len(guarantees[i].depIDs) > 0 {
			out = append(out, i)
		}
	}
	return out
}

// reprover is one reprove pass over the guarantees: what it read, and what
// it has flagged and emitted so far.
type reprover struct {
	store      *db.Store
	opts       Options
	guarantees []guaranteeRow
	flagged    map[int64]bool
	emitted    int
}

// walk visits the guarantees in order while the pass may act on another,
// and reports the pass.
func (r *reprover) walk(ctx context.Context, order []int) (Result, error) {
	for _, i := range order {
		ok, err := admit(ctx, r.emitted, r.opts.MaxPerPass)
		if !ok {
			return r.report(err)
		}
		if err := r.visit(r.guarantees[i]); err != nil {
			return Result{Status: "failed"}, err
		}
	}
	return r.report(nil)
}

// visit flags guarantee g when a dependency changed since it was last known
// sound, and emits its alarm unless the run is dry.
func (r *reprover) visit(g guaranteeRow) error {
	modified, err := r.modified(g)
	if err != nil || !modified {
		return err
	}
	r.flagged[g.id] = true
	if r.opts.DryRun {
		r.emitted++
		return nil
	}
	return r.alarm(g)
}

// modified reports whether a dependency of g changed since g was last known
// sound. A dependency flagged in this pass has no signal yet on a dry run,
// and its signal is not seen in the same second as this guarantee's
// creation, so the pass asks its own record first.
func (r *reprover) modified(g guaranteeRow) (bool, error) {
	if slices.ContainsFunc(g.depIDs, func(id int64) bool { return r.flagged[id] }) {
		return true, nil
	}
	since, err := reproveSince(r.store.ReadConn(), g.id, g.createdAt)
	if err != nil {
		return false, err
	}
	return depWasModifiedAfter(r.store.ReadConn(), g.depIDs, since)
}

// alarm emits the `alarm` signal at guarantee g's coordinates.
func (r *reprover) alarm(g guaranteeRow) error {
	gid := g.id
	d1, d2, d3, d4 := nullInt(g.d1), nullInt(g.d2), nullInt(g.d3), nullInt(g.d4)
	if _, err := r.store.Signals().EmitSignal(
		"alarm",
		ptrString("audit"),
		&gid,
		d1, d2, d3, d4,
		map[string]any{
			"reason":  "dep_modified_since_guarantee",
			"deps":    g.depIDs,
			"source":  "dreamer.reprove",
			"created": g.createdAt,
		},
		nil,
	); err != nil {
		return fmt.Errorf("emit alarm: %w", err)
	}
	r.emitted++
	return nil
}

// report is the pass's outcome once its walk stops: failed with the rows
// already acted on when cut is the deadline's error, else complete.
func (r *reprover) report(cut error) (Result, error) {
	if cut != nil {
		return Result{Status: "failed", Touched: r.emitted}, cut
	}
	return Result{
		Status:  passStatus(r.opts),
		Touched: r.emitted,
		Notes: map[string]any{
			"guarantees_scanned": len(r.guarantees),
			"emitted":            r.emitted,
		},
	}, nil
}

// depWasModifiedAfter reports whether any of the supplied dependency
// findings changed after the guarantee that rests on them.
//
// Two ways it can have changed. The direct one: findings.updated_at is
// stamped by UpdateFinding, so an edit to a dependency, the case the pass
// exists for, is visible as itself. The indirect one: a later signal
// touching the dep marks it as suspect without editing it. The rule is:
// source_id is a finding id when source_type is 'finding' or 'audit', the
// type every dreamer pass emits, and something else entirely for 'conflict',
// where it is a conflict id.
func depWasModifiedAfter(read *sql.DB, depIDs []int64, since string) (bool, error) {
	if len(depIDs) == 0 {
		return false, nil
	}
	// modernc/sqlite reformats TIMESTAMP columns on Go-side scan
	// (`2026-05-02 10:30:00` → `2026-05-02T10:30:00Z`); the parameter we
	// pass back is in the rewritten form, while the column still stores
	// the space-separated form. Normalising both sides through
	// datetime() avoids the resulting lex-compare mismatch.
	if edited, err := depEditedAfter(read, depIDs, since); err != nil || edited {
		return edited, err
	}
	in, args := inList(depIDs)
	q := `SELECT 1 FROM signals
	      WHERE signal_type IN ('alarm','stop_signal')
	        AND source_type IN ('finding','audit')
	        AND source_id IN (` + in + `) AND datetime(created_at) > datetime(?) LIMIT 1`
	return existsRow(read, q, append(args, since)...)
}

// depEditedAfter answers the direct question: was any of these findings edited
// after the given timestamp? updated_at is NULL until the first edit.
func depEditedAfter(read *sql.DB, depIDs []int64, since string) (bool, error) {
	if len(depIDs) == 0 {
		return false, nil
	}
	in, args := inList(depIDs)
	// datetime() on both sides for the same reason the signal query does it:
	// the driver rewrites TIMESTAMP columns on scan, so a raw lex compare
	// between the stored and the round-tripped form disagrees.
	q := `SELECT 1 FROM findings WHERE updated_at IS NOT NULL AND id IN (` + in + `) AND datetime(updated_at) > datetime(?) LIMIT 1`
	return existsRow(read, q, append(args, since)...)
}

// inList is "?,?,…", one placeholder per id, with the ids as its arguments
// and room for one more.
func inList(ids []int64) (string, []any) {
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	return strings.TrimPrefix(strings.Repeat(",?", len(ids)), ","), args
}

// existsRow runs a SELECT 1 … LIMIT 1 probe. No row is false; any other
// failure is an error, so a failed query never passes for a sound guarantee.
func existsRow(read rowQuerier, q string, args ...any) (bool, error) {
	var one int
	err := read.QueryRow(q, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// reproveSince is when a guarantee was last known sound: its creation, or
// the last alarm reprove emitted for it, whichever is later. A change before
// that has been flagged already, so measuring from created_at alone would
// re-flag the same guarantee on every ripen. Timestamps have one-second
// resolution, so a dep change in the same second as that flag is not seen.
func reproveSince(read *sql.DB, guaranteeID int64, createdAt string) (string, error) {
	var since string
	err := read.QueryRow(`
		SELECT MAX(datetime(?), COALESCE(
		  (SELECT MAX(datetime(created_at)) FROM signals
		    WHERE signal_type = 'alarm' AND source_type = 'audit'
		      AND source_id = ?
		      AND json_extract(payload_json, '$.source') = 'dreamer.reprove'),
		  datetime(?)))`, createdAt, guaranteeID, createdAt).Scan(&since)
	return since, err
}

func nullInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}
