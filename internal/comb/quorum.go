package comb

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// QuorumSensor watches the Comb event bus for ∇ convergence between
// resonates-bonded forager pairs. When two bonded foragers write congruent
// verdicts to the Comb within the same swarm run — the sensor ignores events
// stamped with a different run, because the bus is shared by every run in
// the process and a bond row must name the run that produced it — the
// sensor records a fired `forager_bonds` row and writes a `nabla` signal row
// at the involved coordinates.
//
// It changes no label. Agreement is not derivation: a guarantee must follow
// from premises named in its depends_on_ids.
//
// The sensor runs as a single goroutine subscribed to the bus. Stop
// it by cancelling the context passed to Start.
type QuorumSensor struct {
	store *db.Store
	bus   *EventBus

	mu        sync.Mutex
	resonants map[string]map[string]struct{} // forager → set of foragers it resonates with
	verdicts  map[string]string              // forager → most-recent verdict in this run
	fired     map[string]struct{}            // pair-key → already fired (dedup)
	runID     int64
}

// NewQuorumSensor returns a sensor wired to the given store and bus.
// Pass comb.Default for the bus unless you're testing.
func NewQuorumSensor(store *db.Store, bus *EventBus) *QuorumSensor {
	return &QuorumSensor{
		store:     store,
		bus:       bus,
		resonants: map[string]map[string]struct{}{},
		verdicts:  map[string]string{},
		fired:     map[string]struct{}{},
	}
}

// Register declares a resonates pair. Symmetric: Register(a, b)
// implies Register(b, a). Idempotent. Call once per resonates bond
// at swarm start.
func (q *QuorumSensor) Register(a, b string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.resonants[a]; !ok {
		q.resonants[a] = map[string]struct{}{}
	}
	if _, ok := q.resonants[b]; !ok {
		q.resonants[b] = map[string]struct{}{}
	}
	q.resonants[a][b] = struct{}{}
	q.resonants[b][a] = struct{}{}
}

// SetRunID associates a workflow run id with the sensor so forager_bonds
// rows it writes are scoped to this swarm run.
func (q *QuorumSensor) SetRunID(id int64) {
	q.mu.Lock()
	q.runID = id
	q.mu.Unlock()
}

// Resume loads what an earlier segment of the same run already did, for a
// run picked up with `agent-run --resume`: each forager's latest verdict in
// the run and the bonds that already fired. The sensor keeps that state in
// memory only, so without it a resumed run's fresh sensor would never fire
// for a bonded pair whose verdicts straddle the interruption. Verdicts come
// from the forager revisions anchored to the run's ticks; a revision whose
// raw output holds no readable verdict is left out. Call it after SetRunID
// and before Start.
func (q *QuorumSensor) Resume() error {
	q.mu.Lock()
	runID := q.runID
	q.mu.Unlock()
	verdicts, err := q.runVerdicts(runID)
	if err != nil {
		return fmt.Errorf("seed verdicts: %w", err)
	}
	bonds, err := q.store.ForagerBonds().ListByRun(runID)
	if err != nil {
		return fmt.Errorf("seed bonds: %w", err)
	}
	q.seed(verdicts, bonds)
	return nil
}

// seed loads verdicts, and the resonates bonds among bonds that already
// fired, into the sensor.
func (q *QuorumSensor) seed(verdicts map[string]string, bonds []*db.ForagerBondRow) {
	q.mu.Lock()
	defer q.mu.Unlock()
	maps.Copy(q.verdicts, verdicts)
	for _, b := range bonds {
		if b.Kind == db.BondResonates && b.Fired {
			q.fired[pairKey(b.From, b.To)] = struct{}{}
		}
	}
}

// runVerdicts is each forager's latest verdict in run runID, read from the
// forager revisions anchored to the run's ticks. A revision whose raw output
// holds no readable verdict is left out.
func (q *QuorumSensor) runVerdicts(runID int64) (map[string]string, error) {
	rows, err := q.store.ReadConn().Query(`
		SELECT r.vantage_key, COALESCE(r.raw_json, '')
		FROM comb_revisions r JOIN time_wheel t ON t.id = r.tick_id
		WHERE t.run_id = ? AND r.vantage_kind = 'forager'
		ORDER BY r.id`, runID)
	if err != nil {
		return nil, err
	}
	verdicts := map[string]string{}
	err = eachRow(rows, func() error {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return err
		}
		if v := verdictFromRaw(raw); v != "" {
			verdicts[strings.TrimPrefix(key, "forager:")] = v
		}
		return nil
	})
	return verdicts, err
}

// verdictFromRaw reads the verdict from a forager's raw output: the whole
// text as JSON once a fence is stripped, else the widest {...} span in it.
func verdictFromRaw(raw string) string {
	s := stripJSONFence(raw)
	if v, ok := verdictOf(s); ok {
		return v
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		v, _ := verdictOf(s[i : j+1])
		return v
	}
	return ""
}

// verdictOf is the verdict field of s read as JSON; ok is false when s does
// not read.
func verdictOf(s string) (string, bool) {
	var v struct {
		Verdict string `json:"verdict"`
	}
	if json.Unmarshal([]byte(s), &v) != nil {
		return "", false
	}
	return v.Verdict, true
}

// Start launches the sensor goroutine and returns immediately. Cancel ctx
// to stop it: it then handles what is already buffered and exits. The
// returned channel closes once it has, so a caller can wait for the drain
// before the run it watches returns. A panic while it handles an event
// stops it too, logged, and leaves the process running.
func (q *QuorumSensor) Start(ctx context.Context) <-chan struct{} {
	ch, cancel := q.bus.Subscribe(64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("comb/quorum: sensor stopped: panic: %v\n%s", rec, debug.Stack())
			}
		}()
		for {
			e, ok := nextEvent(ctx, ch)
			if !ok {
				return
			}
			q.handle(e)
		}
	}()
	return done
}

// nextEvent is the next event the sensor handles; ok is false once it
// stops: ch closed, or ctx cancelled with nothing left buffered. select
// picks randomly among ready cases, so a verdict already buffered when the
// run ends could be dropped along with the ∇ it completes. A cancelled
// sensor handles what is already here.
func nextEvent(ctx context.Context, ch <-chan Event) (Event, bool) {
	select {
	case <-ctx.Done():
		return bufferedEvent(ch)
	case e, ok := <-ch:
		return e, ok
	}
}

// bufferedEvent is the event already waiting on ch; ok is false when none
// is or ch is closed.
func bufferedEvent(ch <-chan Event) (Event, bool) {
	select {
	case e, ok := <-ch:
		return e, ok
	default:
		return Event{}, false
	}
}

func (q *QuorumSensor) handle(e Event) {
	foragerName, verdict, ok := q.foragerVerdict(e)
	if !ok {
		return
	}
	fires, runID := q.record(foragerName, verdict)
	for _, p := range fires {
		q.fire(p.a, p.b, verdict, runID)
	}
}

// foragerVerdict is the forager and the verdict that e, a forager vantage
// write of the sensor's run, carries; ok is false for any other event or a
// write missing either.
func (q *QuorumSensor) foragerVerdict(e Event) (foragerName, verdict string, ok bool) {
	if !isForagerWrite(e) || !q.ownsRun(e.RunID) {
		return "", "", false
	}
	foragerName, _ = e.Payload["forager"].(string)
	verdict, _ = e.Payload["verdict"].(string)
	return foragerName, verdict, foragerName != "" && verdict != ""
}

// isForagerWrite reports whether e is a forager vantage write.
func isForagerWrite(e Event) bool {
	return e.Kind == EventVantageWritten && e.VantageKind == string(db.VantageForager)
}

// ownsRun reports whether an event stamped runID belongs to the sensor's
// run. Another run's verdict does not: two concurrent runs in one process —
// the MCP server, `chb replicate` — share the Default bus, and a sensor
// consuming the other run's verdicts would write forager_bonds rows stamped
// with a run that did not generate the bond. An unstamped event, or a sensor
// with no run, is everyone's.
func (q *QuorumSensor) ownsRun(runID int64) bool {
	q.mu.Lock()
	mine := q.runID
	q.mu.Unlock()
	return runID == 0 || mine == 0 || runID == mine
}

// pair is two bonded foragers whose verdicts converged.
type pair struct{ a, b string }

// record stores foragerName's verdict and returns the bonded pairs it
// converges with for the first time, with the sensor's run.
func (q *QuorumSensor) record(foragerName, verdict string) ([]pair, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.verdicts[foragerName] = verdict
	var fires []pair
	for peer := range q.resonants[foragerName] {
		if q.firstConvergence(foragerName, verdict, peer) {
			fires = append(fires, pair{foragerName, peer})
		}
	}
	return fires, q.runID
}

// firstConvergence reports whether verdict converges with peer's for the
// first time, and marks the pair fired when it does. q.mu must be held.
func (q *QuorumSensor) firstConvergence(foragerName, verdict, peer string) bool {
	peerVerdict, ok := q.verdicts[peer]
	if !ok || !verdictsConverge(verdict, peerVerdict) {
		return false
	}
	key := pairKey(foragerName, peer)
	if _, already := q.fired[key]; already {
		return false
	}
	q.fired[key] = struct{}{}
	return true
}

// fire records the ∇ event: writes the bond row and the nabla signal.
func (q *QuorumSensor) fire(a, b, verdict string, runID int64) {
	// forager_bonds row — observation that the resonates bond fired.
	// Best-effort, but log on failure: a dropped write loses the ∇
	// convergence with no trace, which contradicts the project's
	// no-silent-provenance-loss discipline (cf. dispatch.go).
	payload := map[string]any{
		"foragers": []string{a, b},
		"verdict":  verdict,
		"reason":   "resonates_convergence",
		"source":   "comb.quorum_sensor",
	}
	// RecordFired inserts the row already fired (fired=1), in one statement,
	// so no reader sees the bond with fired=0.
	if _, err := q.store.ForagerBonds().RecordFired(runID, a, b, db.BondResonates, 1.0, payload); err != nil {
		log.Printf("comb/quorum: bond write %s<->%s failed (∇ provenance lost): %v", a, b, err)
	}

	// nabla signal — the record `chb hive signals --type nabla` reads back.
	// No hive plan step acts on it.
	if _, err := q.store.Signals().EmitSignal(
		"nabla",
		ptrString("system"),
		nil, // no source_id — convergence is across foragers, not a single finding
		nil, nil, nil, nil,
		payload,
		nil,
	); err != nil {
		log.Printf("comb/quorum: nabla signal write %s<->%s failed (∇ provenance lost): %v", a, b, err)
	}
}

// verdictsConverge reports whether two verdict strings agree closely
// enough to count as ∇ convergence. Exact match for support/oppose;
// conditional matches conditional only; abstain never converges.
func verdictsConverge(a, b string) bool {
	return a == b && a != "" && a != "abstain"
}

// ConvergedPairs applies the sensor's rule to a pair list at once: the pairs
// whose two verdicts converge, a missing verdict never converging. Each pair
// is written with the lesser name first, once, and the list is sorted.
func ConvergedPairs(pairs [][2]string, verdicts map[string]string) [][2]string {
	seen := map[[2]string]bool{}
	var out [][2]string
	for _, p := range pairs {
		k := lesserFirst(p)
		if seen[k] || !verdictsConverge(verdicts[k[0]], verdicts[k[1]]) {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	slices.SortFunc(out, func(x, y [2]string) int {
		return cmp.Or(strings.Compare(x[0], y[0]), strings.Compare(x[1], y[1]))
	})
	return out
}

// lesserFirst is the pair p with the lesser name first.
func lesserFirst(p [2]string) [2]string {
	if p[1] < p[0] {
		return [2]string{p[1], p[0]}
	}
	return p
}

func pairKey(a, b string) string {
	if a < b {
		return a + "↔" + b
	}
	return b + "↔" + a
}

func ptrString(s string) *string { return &s }
