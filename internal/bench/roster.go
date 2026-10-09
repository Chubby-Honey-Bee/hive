// Package bench generates graded twin items from the live forager roster,
// grades a model's answers to them, and applies the pre-registered rule
// that decides between model configurations.
//
// Every item is a question the roster answers. Each comes paired with a
// twin whose roster was edited so the answer flips, so a model that always
// gives one verdict scores 0 on every pair. The pack a model reads and the
// answer the grade expects are computed from the same edited roster.
package bench

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// cloneRoster deep-copies the fields a transform may edit.
func cloneRoster(r []foragers.Forager) []foragers.Forager {
	out := make([]foragers.Forager, len(r))
	for i, f := range r {
		f.Bonds = append([]foragers.Bond(nil), f.Bonds...)
		if f.DeliberationEligible != nil {
			v := *f.DeliberationEligible
			f.DeliberationEligible = &v
		}
		out[i] = f
	}
	return out
}

// find is the forager named name, nil when the roster has none.
func find(r []foragers.Forager, name string) *foragers.Forager {
	for i := range r {
		if r[i].Name == name {
			return &r[i]
		}
	}
	return nil
}

// names is the roster's names, sorted.
func names(r []foragers.Forager) []string {
	out := make([]string, 0, len(r))
	for _, f := range r {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// memberSet is the foragers' names as a set.
func memberSet(fs []foragers.Forager) map[string]bool {
	in := make(map[string]bool, len(fs))
	for _, f := range fs {
		in[f.Name] = true
	}
	return in
}

// eligible is the deliberation-eligible foragers.
func eligible(r []foragers.Forager) []foragers.Forager {
	var out []foragers.Forager
	for _, f := range r {
		if f.IsDeliberationEligible() {
			out = append(out, f)
		}
	}
	return out
}

// allPreset is what `--foragers all` dispatches.
func allPreset(r []foragers.Forager) []foragers.Forager {
	out, _ := foragers.Filter(r, []string{"all"})
	return out
}

// hasBond: f declares a bond to to, of kind, or of any kind when kind is "".
func hasBond(f *foragers.Forager, to, kind string) bool {
	for _, b := range f.Bonds {
		if bondMatches(b, to, kind) {
			return true
		}
	}
	return false
}

// bondMatches: b goes to to and is of kind, or of any kind when kind is "".
func bondMatches(b foragers.Bond, to, kind string) bool {
	return b.To == to && (kind == "" || b.Kind == kind)
}

// addBond adds a bond of kind from from to to, of weight 1.
func addBond(r []foragers.Forager, from, to, kind string) {
	f := find(r, from)
	f.Bonds = append(f.Bonds, foragers.Bond{To: to, Kind: kind, Weight: 1})
}

// removeBonds drops every bond of kind between a and b, in either direction.
func removeBonds(r []foragers.Forager, a, b, kind string) {
	for i := range r {
		if other, ok := bondPartner(r[i].Name, a, b); ok {
			dropBondsTo(&r[i], other, kind)
		}
	}
}

// bondPartner is the other end of the a–b pair for the forager named name,
// when it is one of the two.
func bondPartner(name, a, b string) (string, bool) {
	switch name {
	case a:
		return b, true
	case b:
		return a, true
	}
	return "", false
}

// dropBondsTo removes the bonds of kind that f declares on to.
func dropBondsTo(f *foragers.Forager, to, kind string) {
	kept := f.Bonds[:0]
	for _, bd := range f.Bonds {
		if !(bd.Kind == kind && bd.To == to) {
			kept = append(kept, bd)
		}
	}
	f.Bonds = kept
}

type packBond struct {
	To   string `yaml:"to"`
	Kind string `yaml:"kind"`
}

type packCoverage struct {
	Wasp string `yaml:"wasp,omitempty"`
	Cde  string `yaml:"cde,omitempty"`
	Mss  string `yaml:"mss,omitempty"`
}

type packEntry struct {
	Name                 string        `yaml:"name"`
	Archetype            string        `yaml:"archetype"`
	RenderLayer          bool          `yaml:"render_layer,omitempty"`
	DeliberationEligible *bool         `yaml:"deliberation_eligible,omitempty"`
	Coverage             *packCoverage `yaml:"coverage,omitempty"`
	Bonds                []packBond    `yaml:"bonds,omitempty"`
}

// Pack renders the frontmatter fields the families read — name, archetype,
// the eligibility flags, coverage and bonds — as one YAML document, sorted
// by name. It is the context pack every item passes through `--context`.
func Pack(r []foragers.Forager) string {
	sorted := append([]foragers.Forager(nil), r...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	entries := make([]packEntry, 0, len(sorted))
	for _, f := range sorted {
		e := packEntry{
			Name:                 f.Name,
			Archetype:            f.Archetype,
			RenderLayer:          f.RenderLayer,
			DeliberationEligible: f.DeliberationEligible,
		}
		if f.Coverage != (foragers.Coverage{}) {
			e.Coverage = &packCoverage{Wasp: f.Coverage.Wasp, Cde: f.Coverage.Cde, Mss: f.Coverage.Mss}
		}
		for _, b := range f.Bonds {
			e.Bonds = append(e.Bonds, packBond{To: b.To, Kind: b.Kind})
		}
		entries = append(entries, e)
	}
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	_ = enc.Encode(struct {
		Roster []packEntry `yaml:"roster"`
	}{entries})
	_ = enc.Close()
	return sb.String()
}
