package workflow

import (
	"strings"
	"testing"
)

// wantDiversity restates the diversity rule over the lenses that returned
// a verdict. Fewer than two: not checked. Two or more verdicts, or two or
// more recorded models: not low. Otherwise a lens with no recorded model
// leaves it unknown, and one recorded model makes it low. It also returns
// what the line must name: the verdict and the model when low; the
// verdicts when they differ, else the models, when not low; the lenses
// with no recorded model when unknown.
func wantDiversity(verdicts, models map[string]string) (string, []string) {
	vs, ms := map[string]bool{}, map[string]bool{}
	var unrecorded []string
	n := 0
	for name, v := range verdicts {
		if v == "" {
			continue
		}
		n++
		vs[v] = true
		if models[name] == "" {
			unrecorded = append(unrecorded, name)
		} else {
			ms[models[name]] = true
		}
	}
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	switch {
	case n < 2:
		return DiversityNotChecked, nil
	case len(vs) > 1:
		return DiversityNotLow, keys(vs)
	case len(ms) > 1:
		return DiversityNotLow, keys(ms)
	case len(unrecorded) > 0:
		return DiversityUnknown, unrecorded
	}
	return DiversityLow, append(keys(vs), keys(ms)...)
}

// The Queen's {diversity} line, and RunDiversity for the artifact, give
// the state the restated rule gives. A lens that was rejected casts
// nothing. The line opens with the state and names what decided it.
func TestRunTokens_Diversity(t *testing.T) {
	one := map[string]string{"a": "qwen3.5:4b", "b": "qwen3.5:4b", "c": "qwen3.5:4b", "d": "qwen3.5:4b"}
	mixed := map[string]string{"a": "qwen3.5:4b", "b": "ministral-3:8b", "c": "qwen3.5:4b", "d": "ministral-3:8b"}
	unrecorded := map[string]string{"a": "qwen3.5:4b", "b": "qwen3.5:4b", "c": "", "d": "qwen3.5:4b"}
	mixedUnrecorded := map[string]string{"a": "qwen3.5:4b", "b": "qwen3.5:4b", "c": "ministral-3:8b", "d": ""}
	none := map[string]string{"a": "", "b": "", "c": "", "d": ""}
	all := func(v string) map[string]string { return map[string]string{"a": v, "b": v, "c": v, "d": v} }
	opens := map[string]string{DiversityLow: "low:", DiversityNotLow: "not low:", DiversityUnknown: "unknown:", DiversityNotChecked: "not checked:"}
	cases := []struct {
		name     string
		verdicts map[string]string
		models   map[string]string
		rejected []string
	}{
		{"one model, unanimous", all("support"), one, nil},
		{"one model, every lens abstains", all("abstain"), one, nil},
		{"two models, unanimous", all("support"), mixed, nil},
		{"one model, split", map[string]string{"a": "support", "b": "support", "c": "oppose", "d": "support"}, one, nil},
		{"a model unrecorded", all("oppose"), unrecorded, nil},
		{"two models and one unrecorded", all("support"), mixedUnrecorded, nil},
		{"no model recorded", all("support"), none, nil},
		{"one lens answered", map[string]string{"a": "support"}, one, []string{"b", "c", "d"}},
		{"a rejected dissenter", map[string]string{"a": "support", "b": "support", "c": "support"}, one, []string{"d"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			outputs := map[string]map[string]any{}
			for f, v := range c.verdicts {
				outputs[f] = map[string]any{"verdict": v, "recommendation": "r"}
			}
			store := newTestStore(t)
			runID := startTokenRun(t, store, outputs, c.rejected...)
			for f, m := range c.models {
				if m == "" {
					continue
				}
				if err := store.Workflows().UpdateNodeResolvedModel(runID, "forager-"+f, m); err != nil {
					t.Fatal(err)
				}
			}
			state, names := wantDiversity(c.verdicts, c.models)

			next, err := GetNextNodes(store.Workflows(), runID)
			if err != nil || len(next) != 1 {
				t.Fatalf("next = %+v, %v", next, err)
			}
			line := section(t, next[0].ResolvedPrompt, "DIVERSITY:", "")
			if !strings.HasPrefix(line, opens[state]) {
				t.Errorf("line %q; want it to open %q (%s by the rule)", line, opens[state], state)
			}
			for _, s := range names {
				if !strings.Contains(line, s) {
					t.Errorf("line %q does not name %s", line, s)
				}
			}
			d, err := RunDiversity(store.Workflows(), runID)
			if err != nil || d.State() != state {
				t.Errorf("RunDiversity %s (%v); want %s", d.State(), err, state)
			}
			lenses, err := RunLensAnswers(store.Workflows(), runID)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"a", "b", "c", "d"} {
				if got, want := lenses[f], (LensAnswer{Verdict: c.verdicts[f], Model: c.models[f]}); got != want {
					t.Errorf("lens %s: %+v, want %+v", f, got, want)
				}
			}
		})
	}
}
