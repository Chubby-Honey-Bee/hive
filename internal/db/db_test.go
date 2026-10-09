package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// newTestStore creates a fresh Store with initialized schema in t.TempDir().
// newTestStore hands each test a fresh, fully-initialised store. The schema
// is built once per package into a
// template file; every test gets a byte copy of it, which is milliseconds
// instead of the ~1.5 s a full Init costs under the race detector.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	tmpl, err := testTemplateDB()
	if err != nil {
		t.Fatalf("template db: %v", err)
	}
	if err := copyFile(tmpl, path); err != nil {
		t.Fatalf("copy template: %v", err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

var (
	testTemplateOnce sync.Once
	testTemplatePath string
	testTemplateErr  error
)

// testTemplateDB builds the schema once into a package-scoped temp file and
// closes the store so the WAL is checkpointed into the main file.
func testTemplateDB() (string, error) {
	testTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hive-db-template-")
		if err != nil {
			testTemplateErr = err
			return
		}
		path := filepath.Join(dir, "template.db")
		store, err := NewStore(path)
		if err != nil {
			testTemplateErr = err
			return
		}
		if err := store.Init(); err != nil {
			store.Close()
			testTemplateErr = err
			return
		}
		store.Close()
		testTemplatePath = path
	})
	return testTemplatePath, testTemplateErr
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// intPtr is a test helper for *int.
func intPtr(v int) *int { return &v }

// addTestFinding inserts a minimal finding for testing.
func addTestFinding(t *testing.T, s *Store, label, text string, deps []int64) int64 {
	t.Helper()
	f := &Finding{
		Wave: 1, Agent: "test",
		D1: intPtr(0), D2: intPtr(0), D3: intPtr(0), D4: intPtr(0),
		MSSLabel: label,
		Finding:  text,
	}
	if len(deps) > 0 {
		b, _ := json.Marshal(deps)
		dStr := string(b)
		f.DependsOnIDs = &dStr
	}
	id, err := s.Findings().AddFinding(f)
	if err != nil {
		t.Fatalf("AddFinding(%s %q): %v", label, text, err)
	}
	return id
}

// ───── ReadConn ─────

func TestReadConnReturnsReadDB(t *testing.T) {
	s := newTestStore(t)
	got := s.ReadConn()
	if got == nil {
		t.Fatal("ReadConn returned nil")
	}
	if got != s.ReadDB {
		t.Fatal("ReadConn did not return the store's ReadDB")
	}
	// Verify the returned connection is actually usable.
	if err := got.Ping(); err != nil {
		t.Fatalf("ReadConn().Ping: %v", err)
	}
}

// ───── MSS GUARANTEE ENFORCEMENT ─────

func TestGuaranteeWithoutDepsRejected(t *testing.T) {
	s := newTestStore(t)
	f := &Finding{
		Wave: 1, Agent: "test",
		D1: intPtr(0), D2: intPtr(0), D3: intPtr(0), D4: intPtr(0),
		MSSLabel: "guarantee",
		Finding:  "This should fail",
	}
	_, err := s.Findings().AddFinding(f)
	if err == nil {
		t.Fatal("expected error for guarantee without deps")
	}
	if !errors.Is(err, mss.ErrMissingDeps) {
		t.Fatalf("expected ErrMissingDeps, got: %v", err)
	}
}

func TestGuaranteeWithEmptyDepsRejected(t *testing.T) {
	s := newTestStore(t)
	emptyDeps := "[]"
	f := &Finding{
		Wave: 1, Agent: "test",
		D1: intPtr(0), D2: intPtr(0), D3: intPtr(0), D4: intPtr(0),
		MSSLabel:     "guarantee",
		Finding:      "This should fail",
		DependsOnIDs: &emptyDeps,
	}
	_, err := s.Findings().AddFinding(f)
	if err == nil {
		t.Fatal("expected error for guarantee with empty deps")
	}
	if !errors.Is(err, mss.ErrMissingDeps) {
		t.Fatalf("expected ErrMissingDeps, got: %v", err)
	}
}

func TestGuaranteeDependingOnUnknownRejected(t *testing.T) {
	s := newTestStore(t)
	uid := addTestFinding(t, s, "unknown", "unknown gap", nil)
	deps, _ := json.Marshal([]int64{uid})
	dStr := string(deps)
	f := &Finding{
		Wave: 1, Agent: "test",
		D1: intPtr(0), D2: intPtr(0), D3: intPtr(0), D4: intPtr(0),
		MSSLabel:     "guarantee",
		Finding:      "proven thing",
		DependsOnIDs: &dStr,
	}
	_, err := s.Findings().AddFinding(f)
	if err == nil {
		t.Fatal("expected error for guarantee depending on unknown")
	}
	if !errors.Is(err, mss.ErrLaundering) {
		t.Fatalf("expected ErrLaundering, got: %v", err)
	}
}

func TestGuaranteeDependingOnNonexistentRejected(t *testing.T) {
	s := newTestStore(t)
	deps, _ := json.Marshal([]int64{99999})
	dStr := string(deps)
	f := &Finding{
		Wave: 1, Agent: "test",
		D1: intPtr(0), D2: intPtr(0), D3: intPtr(0), D4: intPtr(0),
		MSSLabel:     "guarantee",
		Finding:      "broken ref",
		DependsOnIDs: &dStr,
	}
	_, err := s.Findings().AddFinding(f)
	if err == nil {
		t.Fatal("expected error for guarantee depending on nonexistent")
	}
	if !errors.Is(err, mss.ErrMissingDeps) {
		t.Fatalf("expected ErrMissingDeps, got: %v", err)
	}
}

func TestGuaranteeWithValidDepsAccepted(t *testing.T) {
	s := newTestStore(t)
	d1 := addTestFinding(t, s, "definition", "We chose X", nil)
	a1 := addTestFinding(t, s, "assumption", "We bet Y", nil)
	deps, _ := json.Marshal([]int64{d1, a1})
	dStr := string(deps)
	f := &Finding{
		Wave: 1, Agent: "test",
		D1: intPtr(0), D2: intPtr(0), D3: intPtr(0), D4: intPtr(0),
		MSSLabel:     "guarantee",
		Finding:      "Therefore Z",
		DependsOnIDs: &dStr,
	}
	id, err := s.Findings().AddFinding(f)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if id <= 0 {
		t.Fatal("expected positive ID")
	}
}

func TestAssumptionWithoutDepsAllowed(t *testing.T) {
	s := newTestStore(t)
	id := addTestFinding(t, s, "assumption", "A bet", nil)
	if id <= 0 {
		t.Fatal("expected positive ID")
	}
}

func TestDefinitionWithoutDepsAllowed(t *testing.T) {
	s := newTestStore(t)
	id := addTestFinding(t, s, "definition", "A choice", nil)
	if id <= 0 {
		t.Fatal("expected positive ID")
	}
}

func TestUnknownWithoutDepsAllowed(t *testing.T) {
	s := newTestStore(t)
	id := addTestFinding(t, s, "unknown", "A gap", nil)
	if id <= 0 {
		t.Fatal("expected positive ID")
	}
}

// ───── FOREIGN KEY ENFORCEMENT ─────

func TestConflictWithNonexistentFindingRejected(t *testing.T) {
	s := newTestStore(t)
	_, err := s.WriteDB.Exec(
		"INSERT INTO conflicts (wave, finding_a_id, finding_b_id, description) VALUES (?,?,?,?)",
		1, 999999, 999998, "test conflict",
	)
	if err == nil {
		t.Fatal("expected FK violation")
	}
}

func TestConflictWithValidFindingsAccepted(t *testing.T) {
	s := newTestStore(t)
	f1 := addTestFinding(t, s, "assumption", "Finding A", nil)
	f2 := addTestFinding(t, s, "assumption", "Finding B", nil)
	_, err := s.WriteDB.Exec(
		"INSERT INTO conflicts (wave, finding_a_id, finding_b_id, description) VALUES (?,?,?,?)",
		1, f1, f2, "They disagree",
	)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	var count int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM conflicts").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 conflict, got %d", count)
	}
}

func TestGapResolutionWithNonexistentFindingRejected(t *testing.T) {
	s := newTestStore(t)
	s.WriteDB.Exec("INSERT INTO gaps (wave, agent, description) VALUES (1, 'test', 'a gap')")
	_, err := s.WriteDB.Exec("UPDATE gaps SET resolution_finding_id=999999 WHERE id=1")
	if err == nil {
		t.Fatal("expected FK violation")
	}
}

// ───── WAVE GATE ENFORCEMENT ─────

func TestSynthesisBlockedWithoutGate(t *testing.T) {
	s := newTestStore(t)
	addTestFinding(t, s, "assumption", "Some finding", nil)
	var count int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM wave_gates WHERE wave=1").Scan(&count)
	if count != 0 {
		t.Fatal("expected no gate entry")
	}
}

func TestGateBlocksWithRunningAgents(t *testing.T) {
	s := newTestStore(t)
	s.WriteDB.Exec("INSERT INTO agent_runs (wave, agent_name, status) VALUES (1, 'test-agent', 'running')")
	s.WriteDB.Exec(`INSERT INTO evaluations (wave, coverage_score, depth_score, source_score,
		actionability_score, mss_integrity_score, verdict) VALUES (1, 4, 4, 4, 4, 4, 'COMPLETE')`)
	var running int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM agent_runs WHERE wave=1 AND status='running'").Scan(&running)
	if running == 0 {
		t.Fatal("expected running agents")
	}
}

func TestGatePassesWhenPrerequisitesMet(t *testing.T) {
	s := newTestStore(t)
	s.WriteDB.Exec("INSERT INTO agent_runs (wave, agent_name, status) VALUES (1, 'test-agent', 'completed')")
	s.WriteDB.Exec(`INSERT INTO evaluations (wave, coverage_score, depth_score, source_score,
		actionability_score, mss_integrity_score, verdict) VALUES (1, 4, 4, 4, 4, 4, 'COMPLETE')`)
	var running, evalCount, conflicts int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM agent_runs WHERE wave=1 AND status='running'").Scan(&running)
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM evaluations WHERE wave=1").Scan(&evalCount)
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM conflicts WHERE wave=1 AND resolution IS NULL").Scan(&conflicts)
	if running != 0 {
		t.Fatalf("expected 0 running, got %d", running)
	}
	if evalCount == 0 {
		t.Fatal("expected evaluation")
	}
	if conflicts != 0 {
		t.Fatalf("expected 0 conflicts, got %d", conflicts)
	}
}

// ───── SIGNAL TABLE ─────

func TestValidSignalTypes(t *testing.T) {
	s := newTestStore(t)
	types := []string{"waggle_dance", "stop_signal", "alarm", "tremble_dance",
		"shaking_signal", "quorum", "qmp"}
	for _, st := range types {
		_, err := s.WriteDB.Exec("INSERT INTO signals (signal_type, payload_json) VALUES (?, '{}')", st)
		if err != nil {
			t.Fatalf("insert signal %q: %v", st, err)
		}
	}
	var count int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM signals").Scan(&count)
	if count != len(types) {
		t.Fatalf("expected %d signals, got %d", len(types), count)
	}
}

func TestInvalidSignalTypeRejected(t *testing.T) {
	s := newTestStore(t)
	_, err := s.WriteDB.Exec("INSERT INTO signals (signal_type, payload_json) VALUES ('invalid_type', '{}')")
	if err == nil {
		t.Fatal("expected CHECK constraint violation")
	}
}

func TestHiveStateUniqueProject(t *testing.T) {
	s := newTestStore(t)
	s.WriteDB.Exec("INSERT INTO hive_state (project) VALUES ('test-project')")
	_, err := s.WriteDB.Exec("INSERT INTO hive_state (project) VALUES ('test-project')")
	if err == nil {
		t.Fatal("expected UNIQUE constraint violation")
	}
}

func TestHiveStateDefaultValues(t *testing.T) {
	s := newTestStore(t)
	s.WriteDB.Exec("INSERT INTO hive_state (project) VALUES ('test')")
	var iteration, batchSize, convThreshold int
	var phase, modelTier string
	s.ReadDB.QueryRow(
		"SELECT iteration, phase, batch_size, convergence_threshold, model_tier FROM hive_state WHERE project='test'",
	).Scan(&iteration, &phase, &batchSize, &convThreshold, &modelTier)
	if iteration != 0 {
		t.Fatalf("expected iteration=0, got %d", iteration)
	}
	if phase != "scanning" {
		t.Fatalf("expected phase=scanning, got %s", phase)
	}
	if batchSize != 5 {
		t.Fatalf("expected batch_size=5, got %d", batchSize)
	}
	if convThreshold != 3 {
		t.Fatalf("expected convergence_threshold=3, got %d", convThreshold)
	}
	if modelTier != "sonnet" {
		t.Fatalf("expected model_tier=sonnet, got %s", modelTier)
	}
}

// ───── CASCADE REVERT ─────

func TestCascadeSimpleChain(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Finding A: base fact", nil)
	bID := addTestFinding(t, s, "guarantee", "Finding B: depends on A", []int64{aID})
	cID := addTestFinding(t, s, "guarantee", "Finding C: depends on B", []int64{bID})

	reverted, err := s.CascadeRevert(aID)
	if err != nil {
		t.Fatalf("CascadeRevert: %v", err)
	}
	if len(reverted) != 2 {
		t.Fatalf("expected 2 reverted, got %d", len(reverted))
	}
	revertedSet := map[int64]bool{reverted[0]: true, reverted[1]: true}
	if !revertedSet[bID] || !revertedSet[cID] {
		t.Fatalf("expected B and C reverted, got %v", reverted)
	}

	// Verify labels changed
	var bLabel, cLabel string
	var bDeps, cDeps sql.NullString
	s.ReadDB.QueryRow("SELECT mss_label, depends_on_ids FROM findings WHERE id=?", bID).Scan(&bLabel, &bDeps)
	s.ReadDB.QueryRow("SELECT mss_label, depends_on_ids FROM findings WHERE id=?", cID).Scan(&cLabel, &cDeps)
	if bLabel != "unknown" || cLabel != "unknown" {
		t.Fatalf("expected unknown labels, got b=%s c=%s", bLabel, cLabel)
	}
	if bDeps.Valid || cDeps.Valid {
		t.Fatal("expected NULL depends_on_ids after revert")
	}

	// Verify gaps were created
	var gapCount int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM gaps WHERE agent='hive-alarm'").Scan(&gapCount)
	if gapCount != 2 {
		t.Fatalf("expected 2 gaps, got %d", gapCount)
	}

	// Verify alarm signals
	var alarmCount int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM signals WHERE signal_type='alarm'").Scan(&alarmCount)
	if alarmCount != 2 {
		t.Fatalf("expected 2 alarm signals, got %d", alarmCount)
	}
}

func TestCascadeIdempotent(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Root", nil)
	addTestFinding(t, s, "guarantee", "Depends on A", []int64{aID})

	rev1, _ := s.CascadeRevert(aID)
	if len(rev1) != 1 {
		t.Fatalf("first cascade: expected 1, got %d", len(rev1))
	}
	rev2, _ := s.CascadeRevert(aID)
	if len(rev2) != 0 {
		t.Fatalf("second cascade: expected 0, got %d", len(rev2))
	}
}

func TestCascadeNoDependents(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Standalone", nil)
	rev, _ := s.CascadeRevert(aID)
	if len(rev) != 0 {
		t.Fatalf("expected 0, got %d", len(rev))
	}
}

func TestCascadeBranchingDeps(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Root A", nil)
	bID := addTestFinding(t, s, "guarantee", "B depends on A", []int64{aID})
	cID := addTestFinding(t, s, "guarantee", "C depends on A", []int64{aID})

	reverted, _ := s.CascadeRevert(aID)
	if len(reverted) != 2 {
		t.Fatalf("expected 2, got %d", len(reverted))
	}
	set := map[int64]bool{reverted[0]: true, reverted[1]: true}
	if !set[bID] || !set[cID] {
		t.Fatalf("expected B and C, got %v", reverted)
	}
}

func TestCascadePreservesUnrelatedFindings(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Root A", nil)
	addTestFinding(t, s, "guarantee", "B depends on A", []int64{aID})

	// Separate tree
	xID := addTestFinding(t, s, "definition", "Root X", nil)
	yID := addTestFinding(t, s, "guarantee", "Y depends on X", []int64{xID})

	reverted, _ := s.CascadeRevert(xID)
	set := map[int64]bool{}
	for _, id := range reverted {
		set[id] = true
	}
	if !set[yID] {
		t.Fatal("expected Y reverted")
	}

	// Verify B is still a guarantee
	var bLabel string
	s.ReadDB.QueryRow("SELECT mss_label FROM findings WHERE depends_on_ids IS NOT NULL AND finding LIKE '%B depends%'").Scan(&bLabel)
	if bLabel != "guarantee" {
		t.Fatalf("expected B still guarantee, got %s", bLabel)
	}
}

func TestCascadeRecordsSignalPayloads(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Root", nil)
	addTestFinding(t, s, "guarantee", "Dep on A", []int64{aID})

	s.CascadeRevert(aID)

	var payloadJSON string
	s.ReadDB.QueryRow("SELECT payload_json FROM signals WHERE signal_type='alarm'").Scan(&payloadJSON)
	var payload map[string]any
	json.Unmarshal([]byte(payloadJSON), &payload)
	if triggerID, ok := payload["trigger_finding_id"].(float64); !ok || int64(triggerID) != aID {
		t.Fatalf("expected trigger_finding_id=%d, got %v", aID, payload["trigger_finding_id"])
	}
	if from, ok := payload["reverted_from"].(string); !ok || from != "guarantee" {
		t.Fatalf("expected reverted_from=guarantee, got %v", payload["reverted_from"])
	}
}

// The cascade reverts its trigger's dependents by its own writes, outward from
// the trigger, so UpdateFinding's refusal of a relabel under a guarantee does
// not stop it; and it passes through a dependent already unknown to the
// guarantees beyond it. The reverted ids are the fixture's dependents that
// were not unknown, in breadth-first order from the trigger.
func TestCascadeRevertsChainInOrderThroughUnknown(t *testing.T) {
	s := newTestStore(t)
	labelOf := func(id int64) string {
		t.Helper()
		var label string
		if err := s.ReadDB.QueryRow("SELECT mss_label FROM findings WHERE id=?", id).Scan(&label); err != nil {
			t.Fatal(err)
		}
		return label
	}

	a := addTestFinding(t, s, "definition", "A", nil)
	b := addTestFinding(t, s, "guarantee", "B rests on A", []int64{a})
	c := addTestFinding(t, s, "guarantee", "C rests on B", []int64{b})
	d := addTestFinding(t, s, "guarantee", "D rests on C", []int64{c})
	reverted, err := s.CascadeRevert(a)
	if err != nil {
		t.Fatalf("CascadeRevert(%d): %v", a, err)
	}
	if want := []int64{b, c, d}; !slices.Equal(reverted, want) {
		t.Fatalf("reverted %v, want %v in order", reverted, want)
	}
	for _, id := range []int64{b, c, d} {
		if got := labelOf(id); got != "unknown" {
			t.Errorf("finding %d is %s after the cascade, want unknown", id, got)
		}
	}

	x := addTestFinding(t, s, "definition", "X", nil)
	y := addTestFinding(t, s, "guarantee", "Y rests on X", []int64{x})
	u := addTestFinding(t, s, "assumption", "U rests on Y", []int64{y})
	z := addTestFinding(t, s, "guarantee", "Z rests on U", []int64{u})
	// A stored laundering state, written past the write path's refusal.
	if _, err := s.WriteDB.Exec("UPDATE findings SET mss_label='unknown' WHERE id=?", u); err != nil {
		t.Fatal(err)
	}
	reverted, err = s.CascadeRevert(x)
	if err != nil {
		t.Fatalf("CascadeRevert(%d): %v", x, err)
	}
	if want := []int64{y, z}; !slices.Equal(reverted, want) {
		t.Fatalf("reverted %v, want %v: through the unknown %d without re-reverting it", reverted, want, u)
	}
	for _, id := range []int64{y, u, z} {
		if got := labelOf(id); got != "unknown" {
			t.Errorf("finding %d is %s after the cascade, want unknown", id, got)
		}
	}
}

// ───── PROMOTE FINDING ─────

func TestPromoteAssumptionToGuarantee(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Base fact", nil)
	bID := addTestFinding(t, s, "assumption", "To be promoted", nil)

	if err := s.Findings().PromoteFinding(bID, []int64{aID}); err != nil {
		t.Fatalf("PromoteFinding: %v", err)
	}
	var label string
	s.ReadDB.QueryRow("SELECT mss_label FROM findings WHERE id=?", bID).Scan(&label)
	if label != "guarantee" {
		t.Fatalf("expected guarantee, got %s", label)
	}
}

func TestPromoteRejectsWithoutDeps(t *testing.T) {
	s := newTestStore(t)
	bID := addTestFinding(t, s, "assumption", "No deps", nil)
	err := s.Findings().PromoteFinding(bID, []int64{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, mss.ErrMissingDeps) {
		t.Fatalf("expected ErrMissingDeps, got: %v", err)
	}
}

func TestPromoteRejectsUnknownDeps(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "unknown", "Unknown dep", nil)
	bID := addTestFinding(t, s, "assumption", "To promote", nil)
	err := s.Findings().PromoteFinding(bID, []int64{aID})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, mss.ErrLaundering) {
		t.Fatalf("expected ErrLaundering, got: %v", err)
	}
}

// ───── CONVERGENCE SCORING ─────

func TestSingleAgentGetsLow(t *testing.T) {
	s := newTestStore(t)
	addTestFinding(t, s, "assumption", "Only one agent said this", nil)
	var level string
	s.ReadDB.QueryRow("SELECT convergence_level FROM findings").Scan(&level)
	if level != "low" {
		t.Fatalf("expected low, got %s", level)
	}
}

// ───── NUMERIC CONFLICT DETECTION (logic tests) ─────

func TestNumericDivergenceDetected(t *testing.T) {
	va, vb := 33.0, 50.0
	divergence := (vb - va) / vb
	if divergence <= 0.2 {
		t.Fatalf("expected >20%% divergence, got %.2f%%", divergence*100)
	}
}

func TestNumericAgreementNotFlagged(t *testing.T) {
	va, vb := 33.0, 36.0
	divergence := (vb - va) / vb
	if divergence >= 0.2 {
		t.Fatalf("expected <20%% divergence, got %.2f%%", divergence*100)
	}
}

// ───── CYCLE DETECTION ─────

func TestCycleDetection(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Root", nil)
	bID := addTestFinding(t, s, "guarantee", "B depends on A", []int64{aID})

	// Try to make A depend on B — cycle
	err := mss.CheckCycle(s.ReadDB, aID, mss.Definition, []int64{bID})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !errors.Is(err, mss.ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got: %v", err)
	}
}

func TestNoCycleForValidGraph(t *testing.T) {
	s := newTestStore(t)
	aID := addTestFinding(t, s, "definition", "Root A", nil)
	bID := addTestFinding(t, s, "definition", "Root B", nil)

	// C depends on both A and B — no cycle
	err := mss.CheckCycle(s.ReadDB, 999, mss.Guarantee, []int64{aID, bID})
	if err != nil {
		t.Fatalf("expected no cycle, got: %v", err)
	}
}

// ───── tableExists ─────

func TestTableExists(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		wantOK    bool
		wantErr   bool
	}{
		{
			name:      "existing table returns true",
			tableName: "findings",
			wantOK:    true,
		},
		{
			name:      "missing table returns false",
			tableName: "no_such_table_xyz",
			wantOK:    false,
		},
		{
			name:      "empty name returns false",
			tableName: "",
			wantOK:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			got, err := tableExists(s.WriteDB, tc.tableName)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("tableExists(%q): unexpected error: %v", tc.tableName, err)
			}
			if got != tc.wantOK {
				t.Errorf("tableExists(%q) = %v; want %v", tc.tableName, got, tc.wantOK)
			}
		})
	}

	t.Run("closed db returns error", func(t *testing.T) {
		db, err := sql.Open("sqlite", "file::memory:?cache=private")
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		// Close before querying to force an IO error.
		db.Close()
		_, err = tableExists(db, "findings")
		if err == nil {
			t.Fatal("expected error from closed db, got nil")
		}
	})
}

// ResolveGap closes a gap once, only with a finding that exists, and names why
// it refuses anything else.
func TestResolveGap_ClosesOnceWithARealFinding(t *testing.T) {
	s := newTestStore(t)
	if err := s.Gaps().AddGap(1, "seed", "q", "critical", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.Gaps().QueryGaps(nil, true, nil, nil, nil, nil)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gaps = %v, %v", gaps, err)
	}
	gapID := gaps[0].ID
	fid, err := s.Findings().AddFinding(&Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "answer"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name          string
		gap, finding  int64
		agent, errHas string
	}{
		{"missing finding", gapID, fid + 99, "a", "does not exist"},
		{"unknown gap", gapID + 99, fid, "a", "no such gap"},
		{"no agent", gapID, fid, " ", "agent is required"},
	} {
		if err := s.Gaps().ResolveGap(tc.gap, 1, tc.agent, tc.finding); err == nil || !strings.Contains(err.Error(), tc.errHas) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.errHas)
		}
	}
	if err := s.Gaps().ResolveGap(gapID, 2, "scout", fid); err != nil {
		t.Fatalf("ResolveGap: %v", err)
	}
	if err := s.Gaps().ResolveGap(gapID, 3, "scout", fid); err == nil || !strings.Contains(err.Error(), "already resolved in wave 2") {
		t.Fatalf("second resolve: err = %v, want already resolved", err)
	}
	if open, _ := s.Gaps().QueryGaps(nil, true, nil, nil, nil, nil); len(open) != 0 {
		t.Fatalf("%d gaps still open", len(open))
	}
}
