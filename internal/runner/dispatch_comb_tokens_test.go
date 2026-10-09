package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

func freshStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rt.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Dimensions().AddDimension("dim1", "test", `["0","1"]`); err != nil {
		t.Fatalf("AddDimension: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func intP(v int) *int { return &v }

func seedFinding(t *testing.T, store *db.Store, label, text string, d1 *int) {
	t.Helper()
	f := &db.Finding{
		Wave: 1, Agent: "test", MSSLabel: label, Finding: text, D1: d1,
	}
	if _, err := store.Findings().AddFinding(f); err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
}

func TestResolveCombTokens_NoToken_PassThrough(t *testing.T) {
	store := freshStore(t)
	node := workflow.DispatchNode{
		ResolvedPrompt: "Just a plain prompt with no template tokens.",
	}
	got, _ := resolveCombTokens(node, store)
	if got != node.ResolvedPrompt {
		t.Fatalf("expected pass-through, got %q", got)
	}
}

func TestResolveCombTokens_RegionToken_FromNodeField(t *testing.T) {
	store := freshStore(t)
	seedFinding(t, store, "definition", "X is true", intP(0))
	if _, err := comb.BuildAllRegions(context.Background(), store); err != nil {
		t.Fatalf("BuildAllRegions: %v", err)
	}

	node := workflow.DispatchNode{
		CombVantage:    "d1=0",
		ResolvedPrompt: "Context: {comb.region}\n\nDo work.",
	}
	got, _ := resolveCombTokens(node, store)
	if strings.Contains(got, "{comb.region}") {
		t.Fatalf("token should be substituted: %q", got)
	}
	if !strings.Contains(got, "evidence=1") {
		t.Fatalf("expected substituted narrative to mention evidence count: %q", got)
	}
}

func TestResolveCombTokens_InlineKey(t *testing.T) {
	store := freshStore(t)
	seedFinding(t, store, "definition", "X is true", intP(0))
	if _, err := comb.BuildAllRegions(context.Background(), store); err != nil {
		t.Fatalf("BuildAllRegions: %v", err)
	}

	node := workflow.DispatchNode{
		ResolvedPrompt: "Inline: {comb.d1=0}",
	}
	got, _ := resolveCombTokens(node, store)
	if strings.Contains(got, "{comb.d1=0}") {
		t.Fatalf("inline token not substituted: %q", got)
	}
	if !strings.Contains(got, "evidence=1") {
		t.Fatalf("expected substituted narrative, got: %q", got)
	}
}

func TestResolveCombTokens_MalformedKey_ResolvesToEmpty(t *testing.T) {
	store := freshStore(t)
	node := workflow.DispatchNode{
		ResolvedPrompt: "X: {comb.totally invalid key}",
	}
	got, _ := resolveCombTokens(node, store)
	if strings.Contains(got, "{comb.") {
		t.Fatalf("malformed token should be replaced (with empty), got %q", got)
	}
	if !strings.HasPrefix(got, "X: ") {
		t.Fatalf("expected literal prefix preserved, got %q", got)
	}
}

func TestResolveCombTokens_RegionMiss_ResolvesToEmpty(t *testing.T) {
	store := freshStore(t)
	node := workflow.DispatchNode{
		CombVantage:    "d1=99",
		ResolvedPrompt: "Context: [{comb.region}]",
	}
	got, _ := resolveCombTokens(node, store)
	if !strings.Contains(got, "Context: []") {
		t.Fatalf("expected empty replacement on miss, got %q", got)
	}
}
