package workflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// ledgerSwarm is tokenSwarm with a Queen who reads the ledger, as the lean
// profile's does.
var ledgerSwarm = strings.Replace(tokenSwarm,
	`prompt: "A: {verdict.forager:a}\nB: {verdict.forager:b}\nC: {verdict.forager:c}\nD: {verdict.forager:d}\nG: {verdict.forager:ghost}\nTALLY: {tally}\nNABLA: {nabla.fired}\nDIVERSITY: {diversity}\n"`,
	`prompt: "LEDGER: {swarm.ledger}\nTALLY: {tally}\n"`, 1)

// startLedgerRun starts ledgerSwarm as startTokenRun starts tokenSwarm.
func startLedgerRun(t *testing.T, store *db.Store, outputs map[string]map[string]any, rejected ...string) int64 {
	t.Helper()
	if ledgerSwarm == tokenSwarm {
		t.Fatal("ledgerSwarm did not replace the Queen's prompt")
	}
	repo := store.Workflows()
	runID, err := InitWorkflow(repo, writeWorkflow(t, ledgerSwarm), map[string]any{"question": "q"})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range outputs {
		if err := CompleteNode(repo, runID, "forager-"+name, out); err != nil {
			t.Fatalf("complete forager-%s: %v", name, err)
		}
	}
	for _, name := range rejected {
		if err := repo.MarkNodeRejected(runID, "forager-"+name, "accept rejected", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return runID
}

// ledgerOf hands the Queen out and returns her resolved ledger.
func ledgerOf(t *testing.T, store *db.Store, runID int64) string {
	t.Helper()
	next, err := GetNextNodes(store.Workflows(), runID)
	if err != nil || len(next) != 1 || next[0].Node != "queen" {
		t.Fatalf("next = %+v, %v; want queen", next, err)
	}
	return section(t, next[0].ResolvedPrompt, "LEDGER:", "TALLY:")
}

// The claim fields and their MSS labels, restated from comb.md.
var wantClaimLabels = map[string]string{"def_claims": "definition", "gua_claims": "guarantee", "asm_claims": "assumption", "unk_claims": "unknown"}

// The ledger gives every lens its verdict, recommendation, key points and
// uncertainties; the typed claims under their MSS labels; the resonates
// pairs with a forager that has no node; and the rest of each verdict that
// is not the plurality, evidence first. The plurality's evidence and other
// fields stay out.
func TestSwarmLedger_ThisRunsOutputs(t *testing.T) {
	outputs := map[string]map[string]any{
		"a": {"verdict": "support", "key_points": []any{"key-a1", "key-a2"}, "uncertainties": []any{"unsure-a"}, "evidence": []any{"evidence-a <file.go:12>"}, "def_claims": []any{"def-a"}, "falsification": "falsify-a", "recommendation": "rec-a"},
		"b": {"verdict": "support", "key_points": []any{"key-b"}, "evidence": []any{"evidence-b"}, "asm_claims": []any{"asm-b"}, "recommendation": "rec-b"},
		"c": {"verdict": "oppose", "key_points": []any{"key-c"}, "uncertainties": []any{"unsure-c"}, "evidence": []any{"evidence-c"}, "unk_claims": []any{"unk-c"}, "references": []any{map[string]any{"title": "Title-c"}}, "recommendation": "rec-c"},
	}
	store := newTestStore(t)
	runID := startLedgerRun(t, store, outputs, "d")
	got := ledgerOf(t, store, runID)

	verdicts := map[string]string{}
	for name, out := range outputs {
		verdicts[name], _ = out["verdict"].(string)
	}
	plurality := wantPlurality(verdicts)
	lensEnd := "The claims the lenses labelled"
	rows := section(t, got, "Lenses", lensEnd)
	claims := section(t, got, lensEnd, "Resonates pairs")
	detail := ""
	if i := strings.Index(got, "evidence first:"); i >= 0 {
		detail = got[i:]
	}

	for name, out := range outputs {
		want := []string{fmt.Sprintf("- %s: %s — %s", name, out["verdict"], out["recommendation"])}
		for _, k := range []string{"key_points", "uncertainties"} {
			if v, ok := out[k]; ok {
				want = append(want, itemsOf(v)...)
			}
		}
		for _, w := range want {
			if !strings.Contains(rows, w) {
				t.Errorf("%s's row lacks %q:\n%s", name, w, rows)
			}
		}
		for k, label := range wantClaimLabels {
			v, ok := out[k]
			if !ok {
				continue
			}
			for _, item := range itemsOf(v) {
				head := fmt.Sprintf("%s (%s):", label, k)
				if !strings.Contains(labelBlock(claims, head), name+": "+item) {
					t.Errorf("claim %q of %s is not under %s:\n%s", item, name, head, claims)
				}
			}
		}
		rest := []string{}
		for k, v := range out {
			switch k {
			case "verdict", "recommendation", "key_points", "uncertainties", "def_claims", "gua_claims", "asm_claims", "unk_claims":
				continue
			}
			rest = append(rest, itemsOf(v)...)
		}
		for _, item := range rest {
			switch {
			case verdicts[name] != plurality && !strings.Contains(detail, item):
				t.Errorf("%s departs from %s, and its %q is not given in full:\n%s", name, plurality, item, got)
			case verdicts[name] == plurality && strings.Contains(got, item):
				t.Errorf("%s returned the plurality %s, and its %q is in the ledger:\n%s", name, plurality, item, got)
			}
		}
	}
	if !strings.Contains(rows, "- d: (no verdict: forager-d is rejected in this run)") {
		t.Errorf("the rejected lens has no row saying so:\n%s", rows)
	}

	nodes := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	var orphans []string
	for _, p := range ResonatesPairs(mustDefn(t, ledgerSwarm)) {
		if !nodes[p[0]] || !nodes[p[1]] {
			a, b := p[0], p[1]
			if b < a {
				a, b = b, a
			}
			orphans = append(orphans, a+"↔"+b)
		}
	}
	sort.Strings(orphans)
	line := section(t, got, "so they cannot fire:", "\n")
	if line != strings.Join(orphans, "; ") {
		t.Errorf("orphan pairs %q, want %q", line, strings.Join(orphans, "; "))
	}
}

// labelBlock is the part of the claims block under head: its lines up to
// the next label, which is indented less than an item.
func labelBlock(claims, head string) string {
	i := strings.Index(claims, head)
	if i < 0 {
		return ""
	}
	var out []string
	for _, line := range strings.Split(claims[i+len(head):], "\n")[1:] {
		if !strings.HasPrefix(line, "    ") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// With no plurality there is nothing to depart from, and every verdict is
// given in full; when every lens returns the plurality, none is.
func TestSwarmLedger_TieAndAgreement(t *testing.T) {
	cases := map[string]map[string]string{
		"a tie":            {"a": "support", "b": "oppose", "c": "abstain", "d": "abstain"},
		"every lens":       {"a": "support", "b": "support", "c": "support", "d": "support"},
		"one abstains":     {"a": "oppose", "b": "oppose", "c": "oppose", "d": "abstain"},
		"conditional ties": {"a": "conditional", "b": "support", "c": "conditional", "d": "support"},
	}
	for name, verdicts := range cases {
		t.Run(name, func(t *testing.T) {
			outputs := map[string]map[string]any{}
			for f, v := range verdicts {
				outputs[f] = map[string]any{"verdict": v, "evidence": []any{"evidence-of-" + f}, "recommendation": "r"}
			}
			store := newTestStore(t)
			got := ledgerOf(t, store, startLedgerRun(t, store, outputs))
			plurality := wantPlurality(verdicts)
			for f, v := range verdicts {
				full := plurality == "" || v != plurality
				if has := strings.Contains(got, "evidence-of-"+f); has != full {
					t.Errorf("%s (%s) with plurality %q: evidence given = %v, want %v:\n%s", f, v, plurality, has, full, got)
				}
			}
		})
	}
}

// The manual path hands the Queen out with her ledger filled, as agent-run
// does.
func TestSwarmLedger_ManualPath(t *testing.T) {
	store := newTestStore(t)
	runID := startLedgerRun(t, store, map[string]map[string]any{
		"a": {"verdict": "support", "key_points": []any{"key-a"}}, "b": {"verdict": "support"},
		"c": {"verdict": "oppose", "evidence": []any{"evidence-c"}}, "d": {"verdict": "support"},
	})
	next, err := GetNextNodesManual(store.Workflows(), runID)
	if err != nil || len(next) != 1 || next[0].Node != "queen" {
		t.Fatalf("next = %+v, %v; want queen", next, err)
	}
	p := next[0].ResolvedPrompt
	if m := leftoverRunToken(p); m != "" {
		t.Errorf("the manual path handed out the literal %s", m)
	}
	for _, want := range []string{"key-a", "evidence-c"} {
		if !strings.Contains(p, want) {
			t.Errorf("the manual path's ledger lacks %q:\n%s", want, p)
		}
	}
}

// A read error resolves the ledger to unavailable, as every run token.
func TestSwarmLedger_ReadErrorIsUnavailable(t *testing.T) {
	store := newTestStore(t)
	runID := startLedgerRun(t, store, map[string]map[string]any{"a": {"verdict": "support"}})
	readErr := errors.New("database is locked")
	repo := statesFailRepo{WorkflowsRepo: store.Workflows(), err: readErr}
	got, _ := resolveNodePrompt("{swarm.ledger}", nil, repo, runID, mustDefn(t, ledgerSwarm))
	if want := fmt.Sprintf("unavailable (%v)", readErr); got != want {
		t.Errorf("resolved %q, want %q", got, want)
	}
}
