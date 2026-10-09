package mss

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeCascadeStore is a hand-rolled in-memory CascadeStore used to drive
// RunCascade in isolation.
type fakeCascadeStore struct {
	edges     []DependencyEdge
	reverted  []int64
	gaps      []recordedGap
	signals   []recordedSignal
	revertErr error
	gapErr    error
	signalErr error
	loadErr   error
}

type recordedGap struct {
	wave int
	desc string
}

type recordedSignal struct {
	sourceID int64
	wave     int
}

func (f *fakeCascadeStore) LoadDependencyEdges() ([]DependencyEdge, error) {
	return f.edges, f.loadErr
}
func (f *fakeCascadeStore) RevertFindingToUnknown(id int64) error {
	if f.revertErr != nil {
		return f.revertErr
	}
	f.reverted = append(f.reverted, id)
	return nil
}
func (f *fakeCascadeStore) RecordCascadeGap(wave int, desc string, _, _, _, _ *int) error {
	if f.gapErr != nil {
		return f.gapErr
	}
	f.gaps = append(f.gaps, recordedGap{wave, desc})
	return nil
}
func (f *fakeCascadeStore) EmitAlarmSignal(sourceID int64, _, _, _, _ *int, _ map[string]any, wave int) error {
	if f.signalErr != nil {
		return f.signalErr
	}
	f.signals = append(f.signals, recordedSignal{sourceID, wave})
	return nil
}

func TestRunCascade_NoDeps_NoOp(t *testing.T) {
	store := &fakeCascadeStore{}
	got, err := RunCascade(store, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestRunCascade_RevertsDirectDependents(t *testing.T) {
	store := &fakeCascadeStore{
		edges: []DependencyEdge{
			{ID: 2, Label: "guarantee", Wave: 1, Deps: []int64{1}}, // 2 depends on 1
			{ID: 3, Label: "guarantee", Wave: 1, Deps: []int64{1}}, // 3 depends on 1
		},
	}
	got, err := RunCascade(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 reverted, got %d", len(got))
	}
	if len(store.gaps) != 2 || len(store.signals) != 2 {
		t.Errorf("gaps=%d signals=%d (want 2 each)", len(store.gaps), len(store.signals))
	}
}

func TestRunCascade_TransitiveCascade(t *testing.T) {
	store := &fakeCascadeStore{
		edges: []DependencyEdge{
			{ID: 2, Label: "guarantee", Wave: 1, Deps: []int64{1}},
			{ID: 3, Label: "guarantee", Wave: 1, Deps: []int64{2}},
		},
	}
	got, err := RunCascade(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected transitive cascade to revert 2 + 3, got %v", got)
	}
}

func TestRunCascade_UnknownPropagatesWithoutRevert(t *testing.T) {
	store := &fakeCascadeStore{
		edges: []DependencyEdge{
			// 2 is already unknown but depends on 1 — cascade should
			// continue past it without re-reverting.
			{ID: 2, Label: "unknown", Wave: 1, Deps: []int64{1}},
			// 3 is a guarantee that depends on 2 — should be reverted.
			{ID: 3, Label: "guarantee", Wave: 1, Deps: []int64{2}},
		},
	}
	got, err := RunCascade(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 3 {
		t.Errorf("expected only finding 3 to be reverted, got %v", got)
	}
	// The unknown finding should never have been re-reverted.
	for _, id := range store.reverted {
		if id == 2 {
			t.Errorf("finding 2 (already unknown) should not be reverted again")
		}
	}
}

func TestRunCascade_RevertErrorAborts(t *testing.T) {
	store := &fakeCascadeStore{
		edges: []DependencyEdge{
			{ID: 2, Label: "guarantee", Wave: 1, Deps: []int64{1}},
		},
		revertErr: errors.New("write failed"),
	}
	got, err := RunCascade(store, 1)
	if err == nil {
		t.Fatal("expected error from failed revert")
	}
	if len(got) != 0 {
		t.Errorf("expected zero reverted on error, got %v", got)
	}
}

func TestRunCascade_LoadErrorBubbles(t *testing.T) {
	store := &fakeCascadeStore{loadErr: errors.New("disk full")}
	_, err := RunCascade(store, 1)
	if err == nil {
		t.Fatal("expected load error to propagate")
	}
}

func TestBuildReverseGraph(t *testing.T) {
	edges := []DependencyEdge{
		{ID: 10, Deps: []int64{1, 2}},
		{ID: 20, Deps: []int64{1}},
	}
	g := buildReverseGraph(edges)
	if len(g[1]) != 2 {
		t.Errorf("dep 1 should have 2 dependents, got %d", len(g[1]))
	}
	if len(g[2]) != 1 {
		t.Errorf("dep 2 should have 1 dependent, got %d", len(g[2]))
	}
	if len(g[3]) != 0 {
		t.Errorf("dep 3 has no dependents, got %v", g[3])
	}
}

func TestRevertOne_HappyPath(t *testing.T) {
	f := &fakeCascadeStore{}
	dep := DependencyEdge{ID: 5, Label: "guarantee", Wave: 1, Finding: "short"}
	if err := revertOne(f, dep, 99); err != nil {
		t.Fatalf("revertOne: %v", err)
	}
	if len(f.reverted) != 1 || f.reverted[0] != 5 {
		t.Errorf("reverted = %v; want [5]", f.reverted)
	}
	if len(f.gaps) != 1 || !strings.Contains(f.gaps[0].desc, "Alarm cascade") {
		t.Errorf("gaps = %v", f.gaps)
	}
	if len(f.signals) != 1 || f.signals[0].sourceID != 5 {
		t.Errorf("signals = %v", f.signals)
	}
}

func TestRevertOne_RevertError(t *testing.T) {
	f := &fakeCascadeStore{revertErr: errors.New("revert fail")}
	if err := revertOne(f, DependencyEdge{ID: 1}, 99); err == nil {
		t.Fatal("expected error from revert")
	}
	if len(f.gaps) != 0 {
		t.Error("gap should not be recorded after revert failure")
	}
}

func TestRevertOne_GapError(t *testing.T) {
	f := &fakeCascadeStore{gapErr: errors.New("gap fail")}
	if err := revertOne(f, DependencyEdge{ID: 1}, 99); err == nil {
		t.Fatal("expected error from gap")
	}
	if len(f.signals) != 0 {
		t.Error("signal should not be emitted after gap failure")
	}
}

func TestRevertOne_SignalError(t *testing.T) {
	f := &fakeCascadeStore{signalErr: errors.New("signal fail")}
	if err := revertOne(f, DependencyEdge{ID: 1}, 99); err == nil {
		t.Fatal("expected error from signal")
	}
}

func TestRevertOne_TruncatesLongFinding(t *testing.T) {
	f := &fakeCascadeStore{}
	dep := DependencyEdge{ID: 1, Label: "guarantee", Finding: strings.Repeat("a", 200)}
	if err := revertOne(f, dep, 99); err != nil {
		t.Fatal(err)
	}
	if strings.Count(f.gaps[0].desc, "a") > 90 {
		t.Errorf("description not truncated: len=%d", len(f.gaps[0].desc))
	}
}

// The gap a cascade opens quotes the start of the reverted finding, cut on a
// rune boundary, so its description is valid UTF-8.
func TestCascadeGapDescription_CutsOnARuneBoundary(t *testing.T) {
	got := cascadeGapDescription(DependencyEdge{ID: 2, Label: "assumption", Finding: strings.Repeat("a", 79) + "—b"}, 1)
	if !utf8.ValidString(got) {
		t.Errorf("description %q is not valid UTF-8", got)
	}
}
