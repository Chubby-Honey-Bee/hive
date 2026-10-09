package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// personaSectionsOf splits a persona file's body at its "## § N" lines:
// section number → its text, marker line included.
func personaSectionsOf(t *testing.T, name string) map[int]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "foragers", name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(raw), "\n---\n", 2)
	if len(parts) != 2 {
		t.Fatalf("%s.md has no frontmatter", name)
	}
	out := map[int]string{}
	cur := -1
	for _, line := range strings.SplitAfter(parts[1], "\n") {
		if rest, ok := strings.CutPrefix(line, "## § "); ok {
			digits := rest[:len(rest)-len(strings.TrimLeft(rest, "0123456789"))]
			if n, err := strconv.Atoi(digits); err == nil {
				cur = n
			}
		}
		if cur >= 0 {
			out[cur] += line
		}
	}
	return out
}

// itemsIn lists a field's strings: each list item, or the value itself.
func itemsIn(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{strings.TrimSpace(x)}
	case []any:
		var out []string
		for _, it := range x {
			out = append(out, itemsIn(it)...)
		}
		return out
	}
	return nil
}

// The captured balanced run with the coverage pass, replayed under both
// persona profiles with no model. Under lean each forager's prompt is
// shorter than under full by at least the text of the persona sections it
// leaves out, less a marker line and note for each; Queen and the evaluator
// read the ledger, which holds every lens's verdict, recommendation, key
// points, uncertainties and typed claims, and the evidence of each lens
// whose verdict is not the plurality, and not the evidence of those whose
// verdict is. Both runs complete, Queen accepted. Each node's prompt size,
// full and lean, is logged.
func TestReplayBalancedEval_LeanProfile(t *testing.T) {
	fx := loadReplayFixture(t)
	full, _ := runReplayOn(t, &replayServer{fx: fx}, nil)
	lean, _ := runReplayOn(t, &replayServer{fx: fx, profile: foragers.ProfileLean}, nil)
	fullBy, leanBy := full.byRole(), lean.byRole()
	for _, by := range []map[string][]replayRequest{fullBy, leanBy} {
		if len(by["unknown"]) > 0 || len(by["queen"]) != 1 || len(by["evaluate"]) != 1 {
			t.Fatalf("roles %v: want one Queen, one evaluator and no unknown request", keysOf(by))
		}
	}

	size := func(r replayRequest) int { return len(r.system) + len(r.prompt) }
	roles := keysOf(fullBy)
	var fullTotal, leanTotal int
	for _, role := range roles {
		for i := range fullBy[role] {
			f, l := fullBy[role][i], leanBy[role][i]
			fullTotal += size(f)
			leanTotal += size(l)
			t.Logf("%-20s prompt %6d → %6d bytes (%+.0f%%)", role, size(f), size(l), 100*float64(size(l)-size(f))/float64(size(f)))
		}
	}
	t.Logf("%-20s prompt %6d → %6d bytes (%+.0f%%)", "all requests", fullTotal, leanTotal, 100*float64(leanTotal-fullTotal)/float64(fullTotal))

	for name := range fx.Foragers {
		f, l := fullBy["forager:"+name][0].prompt, leanBy["forager:"+name][0].prompt
		minCut := 0
		for num, text := range personaSectionsOf(t, name) {
			if contains(foragers.LeanSections, num) {
				continue
			}
			marker, _, _ := strings.Cut(text, "\n")
			minCut += len(text) - len(marker+" (left out of this prompt)\n\n")
		}
		if cut := len(f) - len(l); cut < minCut {
			t.Errorf("forager %s: lean is %d bytes shorter, want at least %d", name, cut, minCut)
		}
	}

	verdicts := map[string]string{}
	outputs := map[string]map[string]any{}
	for name, reply := range fx.Foragers {
		outputs[name] = workflow.ExtractJSONOutput(reply)
		verdicts[name], _ = outputs[name]["verdict"].(string)
	}
	_, plurality := tallyOf(verdicts)
	var shown []string
	for _, out := range outputs {
		for _, k := range []string{"key_points", "uncertainties", "recommendation", "def_claims", "gua_claims", "asm_claims", "unk_claims"} {
			shown = append(shown, itemsIn(out[k])...)
		}
	}
	for _, role := range []string{"queen", "evaluate"} {
		p := leanBy[role][0].prompt
		if strings.Contains(p, "{verdict.forager:") || leftoverToken.FindString(p) != "" {
			t.Errorf("the lean %s prompt holds a token: %q", role, leftoverToken.FindString(p))
		}
		for name, out := range outputs {
			for _, k := range []string{"key_points", "uncertainties", "recommendation", "def_claims", "gua_claims", "asm_claims", "unk_claims"} {
				for _, item := range itemsIn(out[k]) {
					if !strings.Contains(p, item) {
						t.Errorf("the lean %s prompt lacks %s's %s %q", role, name, k, item)
					}
				}
			}
			for _, item := range itemsIn(out["evidence"]) {
				dissents := verdicts[name] != plurality
				if dissents && !strings.Contains(p, item) {
					t.Errorf("the lean %s prompt lacks evidence %q of %s, whose %s is not the plurality %s", role, item, name, verdicts[name], plurality)
				}
				if !dissents && strings.Contains(p, item) && !containsAny(shown, item) {
					t.Errorf("the lean %s prompt holds evidence %q of %s, who returned the plurality %s", role, item, name, plurality)
				}
			}
		}
	}
	if q := size(leanBy["queen"][0]); q >= size(fullBy["queen"][0]) {
		t.Errorf("the lean Queen prompt is %d bytes, the full one %d", q, size(fullBy["queen"][0]))
	}
}

// The lean balanced swarm with the coverage pass runs whole under a
// 16,384-token window: the guard refuses no call, and every call is sent
// with an output cap that leaves the prompt, as the server counts it, inside
// the window.
func TestReplayBalancedEval_LeanFitsA16kWindow(t *testing.T) {
	const window = 16384
	t.Setenv("OLLAMA_CONTEXT_LENGTH", "")
	orig := modelContextWindow
	modelContextWindow = func(string) int64 { return window }
	defer func() { modelContextWindow = orig }()

	fx := loadReplayFixture(t)
	srv, _ := runReplayOn(t, &replayServer{fx: fx, profile: foragers.ProfileLean}, nil)
	by := srv.byRole()
	if n := len(by["queen"]) + len(by["evaluate"]); n != 2 {
		t.Fatalf("%d Queen and evaluator calls, want 2", n)
	}
	for name := range fx.Foragers {
		if n := len(by["forager:"+name]); n != 1 {
			t.Errorf("forager %s was called %d times, want once", name, n)
		}
	}
	for _, r := range srv.requests {
		var body struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatal(err)
		}
		// The replay server counts a prompt as a quarter of its request's
		// bytes.
		if counted := int64(len(r.body) / 4); body.MaxTokens <= 0 || body.MaxTokens > window-counted {
			t.Errorf("a %s call asks for %d tokens beside a prompt the server counts at %d, past the %d window", r.role, body.MaxTokens, counted, window)
		}
	}
}

func keysOf(m map[string][]replayRequest) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// containsAny reports whether item is part of any string in list.
func containsAny(list []string, item string) bool {
	for _, s := range list {
		if strings.Contains(s, item) {
			return true
		}
	}
	return false
}
