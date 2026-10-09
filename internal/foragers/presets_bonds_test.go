package foragers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A forager that gains a coverage axis joins `minimal`, so it joins
// `balanced` too: balanced is minimal plus Optimist and Historian, not a
// fixed list of nine names.
func TestBalanced_FollowsMinimal(t *testing.T) {
	all, err := Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, Forager{Name: "economist", Archetype: ArchetypeLens, Coverage: Coverage{Wasp: "I"}})

	want := map[string]bool{}
	for _, w := range all {
		covered := w.Coverage.Wasp != "" || w.Coverage.Cde != "" || w.Coverage.Mss != ""
		named := w.Name == "optimist" || w.Name == "historian"
		if (covered || named) && w.IsDeliberationEligible() {
			want[w.Name] = true
		}
	}
	got := Balanced(all)
	gotSet := map[string]bool{}
	for _, w := range got {
		gotSet[w.Name] = true
	}
	if len(got) != len(want) {
		t.Errorf("Balanced = %v, want %d foragers", names(got), len(want))
	}
	for name := range want {
		if !gotSet[name] {
			t.Errorf("Balanced is missing %q: %v", name, names(got))
		}
	}
}

// Uniqueness uses the key lookup uses: `name: Optimist` in an earlier file
// shadows `name: optimist`, and every way of naming it reaches that file.
func TestLoad_DuplicateNameIgnoresCase(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"a-optimist.md": "Optimist", "optimist.md": "optimist"}
	for file, name := range files {
		body := "---\nname: " + name + "\ncoverage:\n  wasp: E\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := "a-optimist.md" // file-name order: a-optimist.md < optimist.md

	all, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].File != first {
		t.Fatalf("Load = %+v, want only the forager from %s", all, first)
	}
	for _, sel := range []string{"optimist", "balanced", "minimal"} {
		got, err := Filter(all, []string{sel})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].File != first {
			t.Errorf("Filter(%s) = %+v, want the forager from %s", sel, got, first)
		}
	}
}

// A blank title defaults to the capitalised name in the registry, so the
// palette shows the title the prompt uses.
func TestLoad_BlankTitleDefaultsToCapitalisedName(t *testing.T) {
	dir := t.TempDir()
	writeForagerFile(t, dir, "plain", "name: plain\n", "body\n")
	writeForagerFile(t, dir, "titled", "name: titled\ntitle: The Titled\n", "body\n")
	all, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"plain":  strings.ToUpper("plain"[:1]) + "plain"[1:],
		"titled": "The Titled",
	}
	if len(all) != len(want) {
		t.Fatalf("Load = %v, want %d foragers", names(all), len(want))
	}
	for _, w := range all {
		if w.Title != want[w.Name] {
			t.Errorf("%s: Title = %q, want %q", w.Name, w.Title, want[w.Name])
		}
	}
	raw, err := PaletteJSON(all)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"title": "`+want["plain"]+`"`) {
		t.Errorf("palette lacks the defaulted title:\n%s", raw)
	}
}

// A dreamer's cites/contradicts bond would render nothing (no edge, no
// digest), and its resonates bond could never fire (a dreamer always
// abstains), so ValidateSwarm refuses each rather than dropping it.
func TestValidateSwarm_RefusesDreamerDeclaredBonds(t *testing.T) {
	for _, tc := range []struct{ kind, to string }{
		{BondCites, "dreamy"},
		{BondContradicts, "lensa"},
		{BondResonates, "lensa"},
	} {
		swarm := []Forager{
			{Name: "lensa", Archetype: ArchetypeLens},
			{Name: "dreamy", Archetype: ArchetypeDreamer},
			{Name: "dreamz", Archetype: ArchetypeDreamer, Bonds: []Bond{{To: tc.to, Kind: tc.kind, Weight: 1}}},
		}
		want := `dreamer "dreamz" declares a ` + tc.kind + ` bond to "` + tc.to + `"`
		if err := ValidateSwarm(swarm); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s → %s: ValidateSwarm = %v, want the dreamer's bond refused", tc.kind, tc.to, err)
		}
	}
}

// generated parses a generated workflow into its node names, its edges as
// "from→to" strings, and its resonates pairs.
func generated(t *testing.T, swarm []Forager, opts WorkflowOptions) (map[string]any, map[string]bool, [][]string) {
	t.Helper()
	text, err := GenerateWorkflow(swarm, opts)
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	var doc struct {
		Nodes     map[string]any      `yaml:"nodes"`
		Edges     []map[string]string `yaml:"edges"`
		Resonates [][]string          `yaml:"resonates"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("parse generated YAML: %v\n%s", err, text)
	}
	edges := map[string]bool{}
	for _, e := range doc.Edges {
		edges[e["from"]+"→"+e["to"]] = true
	}
	return doc.Nodes, edges, doc.Resonates
}

// A resonates pair with a dreamer on either side can never fire, so
// generation refuses it instead of registering the pair and telling the
// lens it can converge with the dreamer.
func TestGenerateWorkflow_RefusesDreamerResonates(t *testing.T) {
	const dreamer = "dreamr"
	for _, swarm := range [][]Forager{
		{
			{Name: "lensa", Archetype: ArchetypeLens},
			{Name: dreamer, Archetype: ArchetypeDreamer, Bonds: []Bond{{To: "lensa", Kind: BondResonates, Weight: 1}}},
		},
		{
			{Name: "lensa", Archetype: ArchetypeLens, Bonds: []Bond{{To: dreamer, Kind: BondResonates, Weight: 1}}},
			{Name: dreamer, Archetype: ArchetypeDreamer},
		},
	} {
		text, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "t"})
		if err == nil {
			t.Errorf("generated a workflow with a resonates bond to a dreamer:\n%s", text)
		} else if !strings.Contains(err.Error(), `"`+dreamer+`"`) {
			t.Errorf("error does not name the dreamer %q: %v", dreamer, err)
		}
	}
}

// With the human gate on, every dreamer waits on the gate, so a reject
// halts the ripening pass; without it, dreamers hang off queen.
func TestGenerateWorkflow_HumanGateGatesDreamers(t *testing.T) {
	swarm := []Forager{
		{Name: "lensa", Archetype: ArchetypeLens},
		{Name: "d1", Archetype: ArchetypeDreamer},
		{Name: "d2", Archetype: ArchetypeDreamer},
	}
	for _, gate := range []bool{true, false} {
		nodes, edges, _ := generated(t, swarm, WorkflowOptions{Name: "t", HumanGate: gate})
		head := "queen"
		if gate {
			head = "human-gate"
			if _, ok := nodes["human-gate"]; !ok {
				t.Fatalf("gate on: no human-gate node")
			}
			if !edges["queen→human-gate"] {
				t.Errorf("gate on: missing queen→human-gate, edges %v", edges)
			}
		}
		for _, d := range []string{"d1", "d2"} {
			node := "dreamer-" + d
			for e := range edges {
				if strings.HasSuffix(e, "→"+node) && e != head+"→"+node {
					t.Errorf("gate=%v: %s has predecessor edge %s, want only %s→%s", gate, node, e, head, node)
				}
			}
			if !edges[head+"→"+node] {
				t.Errorf("gate=%v: missing %s→%s, edges %v", gate, head, node, edges)
			}
		}
	}
}

// Scope feeds only the bond-free foragers; a bonded forager waits on its
// upstream, which waits on scope.
func TestGenerateWorkflow_ScopeSkipsBondedForagers(t *testing.T) {
	swarm := []Forager{
		{Name: "alpha", Archetype: ArchetypeLens},
		{Name: "beta", Archetype: ArchetypeLens, Bonds: []Bond{{To: "alpha", Kind: BondCites, Weight: 1}}},
		{Name: "gamma", Archetype: ArchetypeLens, Bonds: []Bond{{To: "alpha", Kind: BondContradicts, Weight: 1}}},
		{Name: "delta", Archetype: ArchetypeLens, Bonds: []Bond{{To: "alpha", Kind: BondResonates, Weight: 1}}},
	}
	_, edges, _ := generated(t, swarm, WorkflowOptions{Name: "t", Scope: true})
	for _, w := range swarm {
		bonded := false
		for _, b := range w.Bonds {
			bonded = bonded || b.Kind == BondCites || b.Kind == BondContradicts
		}
		if has := edges["scope→forager-"+w.Name]; has == bonded {
			t.Errorf("scope→forager-%s present=%v, want %v (bonded=%v)", w.Name, has, !bonded, bonded)
		}
	}
}
