package bench

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

func liveRoster(t *testing.T) []foragers.Forager {
	t.Helper()
	all, err := foragers.Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no foragers in ../../foragers")
	}
	return all
}

func seedRange(from, to int64) []int64 {
	var out []int64
	for s := from; s <= to; s++ {
		out = append(out, s)
	}
	return out
}

// entry is the pack as a reader parses it, independent of the generator's
// own types.
type entry struct {
	Name                 string            `yaml:"name"`
	Archetype            string            `yaml:"archetype"`
	RenderLayer          bool              `yaml:"render_layer"`
	DeliberationEligible *bool             `yaml:"deliberation_eligible"`
	Coverage             map[string]string `yaml:"coverage"`
	Bonds                []struct {
		To   string `yaml:"to"`
		Kind string `yaml:"kind"`
	} `yaml:"bonds"`
}

func parsePack(t *testing.T, pack string) map[string]entry {
	t.Helper()
	var doc struct {
		Roster []entry `yaml:"roster"`
	}
	if err := yaml.Unmarshal([]byte(pack), &doc); err != nil {
		t.Fatalf("pack does not parse: %v\n%s", err, pack)
	}
	out := map[string]entry{}
	for _, e := range doc.Roster {
		out[e.Name] = e
	}
	return out
}

var (
	balancedList = regexp.MustCompile(`holds exactly these foragers: ([a-z, ]+)\.`)
	missingQ     = regexp.MustCompile(`roster entry for ([a-z]+) list a resonates bond to ([a-z]+)\?`)
)

// readerAnswer answers an item the way its question tells a reader to,
// from the question text and the parsed pack alone.
func readerAnswer(t *testing.T, it Item) Answer {
	t.Helper()
	r := parsePack(t, it.Context)
	eligible := func(e entry) bool {
		return e.Archetype != "synthesizer" && !e.RenderLayer && (e.DeliberationEligible == nil || *e.DeliberationEligible)
	}
	minimal := map[string]bool{}
	for n, e := range r {
		if eligible(e) && (e.Coverage["wasp"] != "" || e.Coverage["cde"] != "" || e.Coverage["mss"] != "") {
			minimal[n] = true
		}
	}
	yes := func(b bool) string {
		if b {
			return Support
		}
		return Oppose
	}
	orphans := func(in map[string]bool) Answer {
		set := map[string]bool{}
		for n, e := range r {
			for _, b := range e.Bonds {
				if b.Kind == "resonates" && in[n] != in[b.To] {
					p := []string{n, b.To}
					sort.Strings(p)
					set[p[0]+"⇄"+p[1]] = true
				}
			}
		}
		return Answer{Verdict: yes(len(set) > 0), Tokens: sortedKeys(set)}
	}
	switch it.Family {
	case "F1":
		return orphans(minimal)
	case "F2":
		m := balancedList.FindStringSubmatch(it.Question)
		if m == nil {
			t.Fatalf("%s: the question names no balanced members", it.ID)
		}
		in := map[string]bool{}
		for _, n := range strings.Split(m[1], ", ") {
			in[n] = true
		}
		return orphans(in)
	case "F3":
		in := map[string]bool{}
		for n, e := range r {
			if eligible(e) {
				in[n] = true
			}
		}
		return orphans(in)
	case "F4":
		set := map[string]bool{}
		for n, e := range r {
			if eligible(e) && e.Coverage["wasp"] == "I" {
				set["@"+n] = true
			}
		}
		return Answer{Verdict: yes(len(set) > 0), Tokens: sortedKeys(set)}
	case "F6":
		set := map[string]bool{}
		for n, e := range r {
			for _, b := range e.Bonds {
				if b.Kind == "contradicts" && minimal[n] && minimal[b.To] && n != b.To {
					set[n+"→"+b.To] = true
				}
			}
		}
		return Answer{Verdict: yes(len(set) > 0), Tokens: sortedKeys(set)}
	case "F8":
		set := map[string]bool{}
		for n := range minimal {
			if r[n].Coverage["mss"] == "" {
				set["@"+n] = true
			}
		}
		return Answer{Verdict: yes(len(set) == 0), Tokens: sortedKeys(set)}
	case "AB":
		m := missingQ.FindStringSubmatch(it.Question)
		if m == nil {
			t.Fatalf("%s: the question names no forager", it.ID)
		}
		e, ok := r[m[1]]
		if !ok {
			return Answer{Verdict: Abstain}
		}
		has := false
		for _, b := range e.Bonds {
			if b.Kind == "resonates" && b.To == m[2] {
				has = true
			}
		}
		return Answer{Verdict: yes(has)}
	}
	t.Fatalf("no reader for family %s", it.Family)
	return Answer{}
}

func sortedKeys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The answer an item expects is the one its question and pack give a
// careful reader, for every family and many seeds.
func TestGenerate_ExpectedAnswerFollowsFromQuestionAndPack(t *testing.T) {
	items, err := Generate(liveRoster(t), nil, seedRange(1, 40))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		want := readerAnswer(t, it)
		got := it.Want
		if got.Verdict != want.Verdict || strings.Join(got.Tokens, ",") != strings.Join(want.Tokens, ",") {
			t.Errorf("%s: generator expects %s %v; the question and pack give %s %v", it.ID, got.Verdict, got.Tokens, want.Verdict, want.Tokens)
		}
	}
}

// Twins ask the same question over different packs and expect different
// verdicts, so a model that always gives one verdict gets at most one of
// the two right.
func TestGenerate_TwinsShareTheQuestionAndFlipTheVerdict(t *testing.T) {
	items, err := Generate(liveRoster(t), nil, seedRange(1, 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(items)%2 != 0 {
		t.Fatalf("%d items is not a whole number of pairs", len(items))
	}
	for i := 0; i < len(items); i += 2 {
		a, b := items[i], items[i+1]
		if a.Pair != b.Pair || a.Variant != "a" || b.Variant != "b" {
			t.Fatalf("items %s and %s are not one pair", a.ID, b.ID)
		}
		if a.Question != b.Question {
			t.Errorf("%s: the twins ask different questions", a.Pair)
		}
		if a.Context == b.Context {
			t.Errorf("%s: the twins share a pack", a.Pair)
		}
		if a.Want.Verdict == b.Want.Verdict {
			t.Errorf("%s: both twins expect %s", a.Pair, a.Want.Verdict)
		}
	}
}

// Variant a keeps the live roster's answer; b is the edit that flips it.
// The live answer is the reader's answer over the unedited roster's pack.
func TestGenerate_VariantAKeepsTheLiveAnswer(t *testing.T) {
	live := liveRoster(t)
	items, err := Generate(live, nil, seedRange(1, 8))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Variant != "a" {
			continue
		}
		if it.Family == "AB" {
			if it.Want.Verdict == Abstain {
				t.Errorf("%s: the item whose entry is present expects abstain", it.ID)
			}
			continue
		}
		onLive := it
		onLive.Context = Pack(live)
		if want := readerAnswer(t, onLive).Verdict; it.Want.Verdict != want {
			t.Errorf("%s expects %s; the live roster answers %s", it.ID, it.Want.Verdict, want)
		}
	}
}

// Items are a function of the family and seed lists, distinct from one
// another, and free of the braces the workflow engine substitutes.
func TestGenerate_DeterministicDistinctAndSafeToSubstitute(t *testing.T) {
	live := liveRoster(t)
	seeds := seedRange(1, 12)
	a, err := Generate(live, nil, seeds)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(live, nil, seeds)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(Families())*len(seeds)*2 {
		t.Fatalf("got %d items, want %d", len(a), len(Families())*len(seeds)*2)
	}
	seen := map[string]string{}
	for i := range a {
		if a[i].Hash() != b[i].Hash() || a[i].ID != b[i].ID {
			t.Fatalf("item %d differs between two runs: %s vs %s", i, a[i].ID, b[i].ID)
		}
		if prev, dup := seen[a[i].Hash()]; dup {
			t.Errorf("%s repeats %s", a[i].ID, prev)
		}
		seen[a[i].Hash()] = a[i].ID
		if strings.ContainsAny(a[i].Question+a[i].Context, "{}") {
			t.Errorf("%s contains a brace, which ResolveTemplate may substitute", a[i].ID)
		}
	}
}

func TestGenerate_SelectsFamiliesAndRefusesUnknownOnes(t *testing.T) {
	live := liveRoster(t)
	items, err := Generate(live, []string{"F4", "minimal-mss-axis"}, []int64{3})
	if err != nil {
		t.Fatal(err)
	}
	var fams []string
	for _, it := range items {
		fams = append(fams, it.Family)
	}
	if strings.Join(fams, ",") != "F4,F4,F8,F8" {
		t.Fatalf("families = %v, want F4 then F8, two items each", fams)
	}
	if _, err := Generate(live, []string{"F5"}, []int64{1}); err == nil {
		t.Fatal("an unknown family must be refused")
	}
	if _, err := Generate(live, nil, nil); err == nil {
		t.Fatal("no seeds must be refused")
	}
}

// GenerateAfter draws later seeds' items after the prior seeds' items in
// one call: the prior items are the ones Generate returns for those seeds
// alone, and no later item repeats one of them. Generated on their own,
// the later seeds can repeat prior items, which would void a decision.
func TestGenerateAfter_NoLaterItemRepeatsAPriorOne(t *testing.T) {
	live := liveRoster(t)
	prior, later := seedRange(1, 4), seedRange(101, 140)
	alone, err := Generate(live, nil, prior)
	if err != nil {
		t.Fatal(err)
	}
	combined, err := Generate(live, nil, append(append([]int64(nil), prior...), later...))
	if err != nil {
		t.Fatal(err)
	}
	var head []Item
	for _, it := range combined {
		if it.Seed <= 4 {
			head = append(head, it)
		}
	}
	if len(head) != len(alone) {
		t.Fatalf("%d prior items in the combined call, %d alone", len(head), len(alone))
	}
	priorHashes := map[string]string{}
	for i := range alone {
		if head[i].ID != alone[i].ID || head[i].Hash() != alone[i].Hash() {
			t.Fatalf("prior item %d is %s in the combined call and %s alone", i, head[i].ID, alone[i].ID)
		}
		priorHashes[alone[i].Hash()] = alone[i].ID
	}

	after, err := GenerateAfter(live, nil, prior, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(Families())*len(later)*2 {
		t.Fatalf("%d items, want %d", len(after), len(Families())*len(later)*2)
	}
	for _, it := range after {
		if it.Seed <= 4 {
			t.Fatalf("%s: GenerateAfter returned a prior seed's item", it.ID)
		}
		if id, dup := priorHashes[it.Hash()]; dup {
			t.Errorf("%s repeats prior item %s", it.ID, id)
		}
	}

	separate, err := Generate(live, nil, later)
	if err != nil {
		t.Fatal(err)
	}
	repeats := 0
	for _, it := range separate {
		if priorHashes[it.Hash()] != "" {
			repeats++
		}
	}
	t.Logf("generated on their own, %d of %d later items repeat a prior item", repeats, len(separate))

	if _, err := GenerateAfter(live, nil, prior, []int64{4, 5}); err == nil {
		t.Fatal("a seed on both lists must be refused")
	}
}

// flipRoster edits a copy of the roster so that the family's answer flips,
// by the family's own rule and without the generator's transforms.
func flipRoster(t *testing.T, family string, live []foragers.Forager) []foragers.Forager {
	t.Helper()
	r := cloneRoster(live)
	minimal := memberSet(foragers.Minimal(r))
	switch family {
	case "F1", "F2", "F3":
		in := minimal
		if family == "F2" {
			in = memberSet(foragers.Balanced(r))
		} else if family == "F3" {
			in = memberSet(eligible(r))
		}
		orphaned := false
		for _, f := range r {
			for _, b := range f.Bonds {
				orphaned = orphaned || b.Kind == foragers.BondResonates && in[f.Name] != in[b.To]
			}
		}
		if orphaned {
			for i := range r {
				for j := range r[i].Bonds {
					if r[i].Bonds[j].Kind == foragers.BondResonates {
						r[i].Bonds[j].Kind = foragers.BondCites
					}
				}
			}
			return r
		}
		for i := range r {
			for _, to := range names(r) {
				if in[r[i].Name] && !in[to] && !hasBond(&r[i], to, "") {
					r[i].Bonds = append(r[i].Bonds, foragers.Bond{To: to, Kind: foragers.BondResonates, Weight: 1})
					return r
				}
			}
		}
	case "F4":
		owned := false
		for i := range r {
			if r[i].IsDeliberationEligible() && r[i].Coverage.Wasp == "I" {
				r[i].Coverage.Wasp, owned = "", true
			}
		}
		if owned {
			return r
		}
		for i := range r {
			if r[i].IsDeliberationEligible() {
				r[i].Coverage.Wasp = "I"
				return r
			}
		}
	case "F6":
		found := false
		for i := range r {
			if !minimal[r[i].Name] {
				continue
			}
			kept := r[i].Bonds[:0]
			for _, b := range r[i].Bonds {
				if b.Kind == foragers.BondContradicts && minimal[b.To] && b.To != r[i].Name {
					found = true
					continue
				}
				kept = append(kept, b)
			}
			r[i].Bonds = kept
		}
		if found {
			return r
		}
		for i := range r {
			for _, to := range names(r) {
				if minimal[r[i].Name] && minimal[to] && to != r[i].Name && !hasBond(&r[i], to, "") {
					r[i].Bonds = append(r[i].Bonds, foragers.Bond{To: to, Kind: foragers.BondContradicts, Weight: 1})
					return r
				}
			}
		}
	case "F8":
		lacking := false
		for i := range r {
			if minimal[r[i].Name] && r[i].Coverage.Mss == "" {
				r[i].Coverage.Mss, lacking = "def", true
			}
		}
		if lacking {
			return r
		}
		for i := range r {
			if minimal[r[i].Name] {
				r[i].Coverage.Mss = ""
				if r[i].Coverage.Wasp == "" && r[i].Coverage.Cde == "" {
					r[i].Coverage.Cde = "detect"
				}
				return r
			}
		}
	}
	t.Fatalf("%s: no edit flips the answer on this roster", family)
	return nil
}

// Items follow the roster they are generated from: edit the roster so a
// family's answer flips, and variant a expects the edited roster's answer,
// not the live one. A generator that returned fixed answers would fail.
func TestGenerate_AnswersFollowAnEditedRoster(t *testing.T) {
	live := liveRoster(t)
	seeds := seedRange(1, 8)
	for _, fam := range []string{"F1", "F2", "F3", "F4", "F6", "F8"} {
		edited := flipRoster(t, fam, live)
		liveItems, err := Generate(live, []string{fam}, seeds)
		if err != nil {
			t.Fatal(err)
		}
		editItems, err := Generate(edited, []string{fam}, seeds)
		if err != nil {
			t.Fatalf("%s on the edited roster: %v", fam, err)
		}
		for i, it := range editItems {
			if it.Variant != "a" {
				continue
			}
			onLive, onEdit := liveItems[i], it
			onLive.Context, onEdit.Context = Pack(live), Pack(edited)
			was, now := readerAnswer(t, onLive).Verdict, readerAnswer(t, onEdit).Verdict
			if was == now {
				t.Fatalf("%s: the edit left the roster's answer at %s", fam, was)
			}
			if it.Want.Verdict != now {
				t.Errorf("%s: on the edited roster the generator expects %s; the edited roster answers %s", it.ID, it.Want.Verdict, now)
			}
		}
	}
}
