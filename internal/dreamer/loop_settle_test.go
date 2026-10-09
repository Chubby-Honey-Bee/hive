package dreamer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// launderedStore builds a guarantee that rests on an unknown. The write
// path forbids that, so the unknown dependency is injected afterwards by
// direct SQL — the same recipe TestSettle_GuaranteeWithUnknownDep uses.
type storeT = *db.Store

func launderedStore(t *testing.T) (store storeT, gid int64) {
	t.Helper()
	store = freshStore(t)
	depA := mustAddFinding(t, store, "definition", "fact A", nil)
	depU := mustAddFinding(t, store, "unknown", "we don't know B", nil)
	gid = mustAddFinding(t, store, "guarantee", "A and B imply C", []int64{depA})
	deps, _ := json.Marshal([]int64{depA, depU})
	if _, err := store.WriteDB.Exec(`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, string(deps), gid); err != nil {
		t.Fatalf("inject unknown dep: %v", err)
	}
	if h, r, err := LaunderingDetected(store); err != nil || !h || r != "laundering" {
		t.Fatalf("precondition: want laundering detected, got h=%v r=%q err=%v", h, r, err)
	}
	return store, gid
}

func labelOf(t *testing.T, store storeT, id int64) string {
	t.Helper()
	var l string
	if err := store.ReadDB.QueryRow(`SELECT mss_label FROM findings WHERE id = ?`, id).Scan(&l); err != nil {
		t.Fatalf("read label: %v", err)
	}
	return l
}

// Laundering found ahead of a pass runs settle — the pass that repairs
// laundering — first, and the loop proceeds once the comb is clean.
func TestLoop_LaunderingIsRepairedBySettleBeforeHalt(t *testing.T) {
	store, gid := launderedStore(t)
	opts := DefaultOptions()
	opts.Apply = true

	out, err := Run(context.Background(), store, LoopOptions{Pass: opts})
	if err != nil {
		t.Fatalf("Run: %v (halted=%v reason=%q)", err, out.Halted, out.HaltReason)
	}
	if out.Halted {
		t.Fatalf("loop halted (%s); settle should have repaired the laundering first", out.HaltReason)
	}
	if len(out.Passes) == 0 || out.Passes[0].Name != "settle" {
		names := make([]string, 0, len(out.Passes))
		for _, p := range out.Passes {
			names = append(names, p.Name)
		}
		t.Errorf("expected settle to run first as the repair pass, got %v", names)
	}
	if got := labelOf(t, store, gid); got != "assumption" {
		t.Errorf("guarantee resting on an unknown should be demoted to assumption, got %q", got)
	}
}

// When --passes excludes settle there is no repair available, and the halt
// applies exactly as before.
func TestLoop_LaunderingHaltsWhenSettleExcluded(t *testing.T) {
	store, gid := launderedStore(t)
	opts := DefaultOptions()
	opts.Apply = true

	out, err := Run(context.Background(), store, LoopOptions{Pass: opts, OnlyPasses: []string{"prune"}})
	if !errors.Is(err, ErrHalted) {
		t.Fatalf("expected ErrHalted, got err=%v halted=%v", err, out.Halted)
	}
	if out.HaltReason != "laundering" {
		t.Errorf("halt reason = %q, want laundering", out.HaltReason)
	}
	if got := labelOf(t, store, gid); got != "guarantee" {
		t.Errorf("nothing should have been repaired, label = %q", got)
	}
}

// A recommend-only ripen — what every swarm's dreamer node runs — flags the
// laundered guarantee. A later --apply still demotes it, rather than skip it
// as already flagged and halt on laundering for good.
func TestLoop_ApplyRepairsLaunderingFlaggedEarlier(t *testing.T) {
	store, gid := launderedStore(t)
	if _, err := Run(context.Background(), store, LoopOptions{Pass: DefaultOptions()}); !errors.Is(err, ErrHalted) {
		t.Fatalf("recommend-only run: want ErrHalted, got %v", err)
	}
	opts := DefaultOptions()
	opts.Apply = true
	if res, err := Run(context.Background(), store, LoopOptions{Pass: opts}); err != nil {
		t.Fatalf("apply run: %v (halt reason %q)", err, res.HaltReason)
	}
	if got := labelOf(t, store, gid); got != "assumption" {
		t.Fatalf("label = %q, want assumption", got)
	}
	if got := countSignals(t, store, "alarm"); got != 1 {
		t.Fatalf("%d alarms, want the one from the first run", got)
	}
}

// settle over a sound guarantee changes nothing a digest reads, so the
// guarantee's region stays fresh.
func TestSettle_SoundGuaranteeLeavesRegionFresh(t *testing.T) {
	store := freshStore(t)
	dep := mustAddFinding(t, store, "definition", "fact A", nil)
	mustAddFinding(t, store, "guarantee", "A implies C", []int64{dep})
	region := comb.Coords{"d1": 0}
	d, err := comb.BuildDigest(store, region)
	if err != nil {
		t.Fatal(err)
	}
	if err := comb.WriteRegionVantage(context.Background(), store, d, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := passSettle(context.Background(), store, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	if got, err := comb.Classify(store, region); err != nil || got != comb.StaleFresh {
		t.Fatalf("Classify = %q, %v; want fresh", got, err)
	}
}

// settle --apply's demotion changes the region's digest when it moves the
// dominant label, and then the region reads stale by comb.md § Staleness,
// from the digest alone.
func TestSettle_ApplyLeavesStalenessToTheDigest(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "fact A", nil)
	depU := mustAddFinding(t, store, "unknown", "we don't know B", nil)
	for _, text := range []string{"A and B imply C", "A and B imply D"} {
		gid := mustAddFinding(t, store, "guarantee", text, []int64{depA})
		deps, _ := json.Marshal([]int64{depA, depU})
		if _, err := store.WriteDB.Exec(`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, string(deps), gid); err != nil {
			t.Fatal(err)
		}
	}
	region := comb.Coords{"d1": 0}
	before, err := comb.BuildDigest(store, region)
	if err != nil {
		t.Fatal(err)
	}
	if err := comb.WriteRegionVantage(context.Background(), store, before, "test"); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Apply = true
	if _, err := passSettle(context.Background(), store, opts); err != nil {
		t.Fatal(err)
	}
	after, err := comb.BuildDigest(store, region)
	if err != nil {
		t.Fatal(err)
	}
	same := after.Narrative == before.Narrative && after.Confidence == before.Confidence &&
		after.Contested == before.Contested && after.DominantLabel == before.DominantLabel &&
		after.EvidenceCount == before.EvidenceCount && after.OpenQuestionsCount == before.OpenQuestionsCount
	if same {
		t.Fatal("fixture: settle left the digest unchanged, so the test proves nothing")
	}
	if got, err := comb.Classify(store, region); err != nil || got != comb.StaleStale {
		t.Fatalf("Classify = %q, %v; want stale, because the digest changed", got, err)
	}
}

// setLabel relabels a finding directly, as a cascade or a later edit can,
// without the write path's checks.
func setLabel(t *testing.T, store storeT, id int64, label string) {
	t.Helper()
	if _, err := store.WriteDB.Exec(`UPDATE findings SET mss_label = ? WHERE id = ?`, label, id); err != nil {
		t.Fatalf("relabel %d: %v", id, err)
	}
}

// launderingGuarantees returns the guarantee ids the MSS audit — the QMP
// gate's check — reports as laundering.
func launderingGuarantees(t *testing.T, store storeT) map[int64]bool {
	t.Helper()
	res, err := store.MSSAudit()
	if err != nil {
		t.Fatalf("MSSAudit: %v", err)
	}
	out := map[int64]bool{}
	for _, v := range res.LaunderingViolations {
		id, ok := v["guarantee_id"].(int64)
		if !ok {
			t.Fatalf("guarantee_id %v is %T, want int64", v["guarantee_id"], v["guarantee_id"])
		}
		out[id] = true
	}
	return out
}

// guarantee → assumption → unknown is laundering to the audit, which walks
// the whole chain, and settle walks it too, so `ripen --apply` demotes the
// guarantee rather than halting on laundering.
func TestLoop_ApplyRepairsLaunderingBehindAnAssumption(t *testing.T) {
	store := freshStore(t)
	a := mustAddFinding(t, store, "assumption", "A", nil)
	mid := mustAddFinding(t, store, "guarantee", "B follows from A", []int64{a})
	top := mustAddFinding(t, store, "guarantee", "C follows from B", []int64{mid})
	setLabel(t, store, mid, "assumption")
	setLabel(t, store, a, "unknown")
	flagged := launderingGuarantees(t, store)
	if !flagged[top] {
		t.Fatalf("precondition: audit flags %v, want guarantee %d", flagged, top)
	}

	opts := DefaultOptions()
	opts.Apply = true
	out, err := Run(context.Background(), store, LoopOptions{Pass: opts})
	if err != nil {
		t.Fatalf("Run: %v (halted=%v reason=%q)", err, out.Halted, out.HaltReason)
	}
	for id := range flagged {
		if got := labelOf(t, store, id); got != "assumption" {
			t.Errorf("guarantee %d the gate halts on is still %q, want assumption", id, got)
		}
	}
	if left := launderingGuarantees(t, store); len(left) != 0 {
		t.Errorf("laundering left after settle: %v", left)
	}
}

// On guarantee → guarantee → unknown, demoting the lower guarantee clears its
// dependencies and cuts the chain, so the upper one keeps its label: it now
// rests on an assumption, which is sound.
func TestSettle_DemotesTheLastGuaranteeBeforeTheUnknown(t *testing.T) {
	store := freshStore(t)
	u := mustAddFinding(t, store, "definition", "A", nil)
	lower := mustAddFinding(t, store, "guarantee", "B follows from A", []int64{u})
	upper := mustAddFinding(t, store, "guarantee", "C follows from B", []int64{lower})
	setLabel(t, store, u, "unknown")
	if flagged := launderingGuarantees(t, store); !flagged[lower] || !flagged[upper] {
		t.Fatalf("precondition: audit flags %v, want %d and %d", flagged, lower, upper)
	}

	opts := DefaultOptions()
	opts.Apply = true
	res, err := passSettle(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passSettle: %v", err)
	}
	if res.Touched != 1 {
		t.Errorf("touched %d, want 1", res.Touched)
	}
	if got := labelOf(t, store, lower); got != "assumption" {
		t.Errorf("lower guarantee is %q, want assumption", got)
	}
	if got := labelOf(t, store, upper); got != "guarantee" {
		t.Errorf("upper guarantee is %q, want guarantee", got)
	}
	if left := launderingGuarantees(t, store); len(left) != 0 {
		t.Errorf("laundering left after settle: %v", left)
	}
}
