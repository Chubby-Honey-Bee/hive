package foragers

import (
	"fmt"
	"sort"
	"strings"
)

// Default returns the subset of foragers with `default: true` in their
// frontmatter AND that are deliberation-eligible, sorted by Name.
// Render-layer foragers (Editor) and synthesizers (Queen) are
// excluded — they participate via separate dispatch paths.
func Default(all []Forager) []Forager {
	out := make([]Forager, 0, len(all))
	for _, w := range all {
		if w.Default && w.IsDeliberationEligible() {
			out = append(out, w)
		}
	}
	return out
}

// Balanced returns the empirically-grounded default swarm: Minimal(all)
// plus Optimist and Historian (9 foragers in the shipped repo). It is
// computed from Minimal, so a forager that gains or loses a coverage
// axis joins or leaves both presets together.
//
// Optimist anchors verdict decisiveness (the persona self-evaluation,
// docs/specs/swarm.md § The empirical record: Skeptic-with-Optimist is
// decisive, Skeptic-without-Optimist hedges). Historian closes the
// Timekeeper⇄Historian and Scholar⇄Historian ∇-resonances that
// would otherwise be orphaned in minimal.
//
// Returns deliberation-eligible foragers only (synthesizers and
// render-layer personas are excluded automatically).
func Balanced(all []Forager) []Forager {
	out := Minimal(all)
	for _, name := range []string{"optimist", "historian"} {
		if w, ok := eligibleByName(all, name); ok && !hasName(out, name) {
			out = append(out, w)
		}
	}
	sortByName(out)
	return out
}

// eligibleByName is the forager ByName finds, when it is
// deliberation-eligible.
func eligibleByName(all []Forager, name string) (Forager, bool) {
	w, ok := ByName(all, name)
	return w, ok && w.IsDeliberationEligible()
}

// hasName reports whether ByName finds name among ws.
func hasName(ws []Forager, name string) bool {
	_, ok := ByName(ws, name)
	return ok
}

// ByName returns the forager whose Name matches `name` (case-insensitive),
// or zero-Forager + false.
func ByName(all []Forager, name string) (Forager, bool) {
	target := strings.ToLower(strings.TrimSpace(name))
	for _, w := range all {
		if strings.ToLower(w.Name) == target {
			return w, true
		}
	}
	return Forager{}, false
}

// Minimal returns the smallest WASP/CDE/MSS-complete deliberation
// swarm — every deliberation-eligible forager whose Coverage
// frontmatter declares at least one axis.
//
// In template-v1 the shipped 7 axis-owners are: architect, skeptic,
// timekeeper, empiricist, scholar, steward, pragmatist.
// (Editor is render-layer; Pragmatist owns the WASP-k
// execution-constraint half. docs/specs/swarm.md § The empirical record
// describes the self-evaluation behind the roster.)
//
// The synthesizer (Queen) carries `coverage.cde: encode` but is
// filtered out by IsDeliberationEligible — it owns the CDE-Encode
// phase as the synthesizer, not as a deliberation seat.
func Minimal(all []Forager) []Forager {
	out := make([]Forager, 0, len(all))
	for _, w := range all {
		if w.IsDeliberationEligible() && w.Coverage.declared() {
			out = append(out, w)
		}
	}
	sortByName(out)
	return out
}

// deliberationEligible is every deliberation-eligible forager: the "all"
// preset.
func deliberationEligible(all []Forager) []Forager {
	var out []Forager
	for _, w := range all {
		if w.IsDeliberationEligible() {
			out = append(out, w)
		}
	}
	return out
}

// presets are the pseudo-names Filter expands, each to the foragers it
// stands for.
var presets = map[string]func(all []Forager) []Forager{
	"default":  Default,
	"defaults": Default,
	"all":      deliberationEligible,
	"minimal":  Minimal,
	"balanced": Balanced,
}

// Filter resolves a user-supplied forager-name list into a concrete
// slice of Foragers. Pseudo-names:
//
//	"default"  — every default:true forager (deliberation-eligible only)
//	"all"      — every deliberation-eligible forager
//	"minimal"  — every forager with a coverage: tag (smallest axis-complete set)
//	"balanced" — minimal + Optimist + Historian (the new empirical default)
//
// Render-layer foragers (Editor) and synthesizers (Queen) are
// excluded from "default", "all", "minimal", and "balanced". Naming a
// render-layer forager adds it as a lens; naming a synthesizer is refused
// by ValidateSwarm, since every swarm already runs Queen.
//
// Unknown names produce a single error listing them all.
//
// Examples:
//
//	Filter(all, []string{"default"})                   // 10 defaults
//	Filter(all, []string{"minimal"})                    // 7 axis-owners
//	Filter(all, []string{"balanced"})                   // 9 (new default)
//	Filter(all, []string{"optimist","skeptic","forecaster"})
//	Filter(all, []string{"balanced","framer"})  // 9 + 1
//	Filter(all, []string{"all"})                        // every deliberation-eligible forager
func Filter(all []Forager, names []string) ([]Forager, error) {
	if len(names) == 0 {
		return Default(all), nil
	}
	chosen, unknown := choose(all, names)
	if len(unknown) > 0 {
		return nil, unknownForagersError(all, unknown)
	}
	return byNameOrder(chosen), nil
}

// choose resolves each requested name, a preset or a forager, into the
// chosen foragers by name, and lists the names that match neither.
func choose(all []Forager, requested []string) (map[string]Forager, []string) {
	chosen := make(map[string]Forager, len(requested))
	var unknown []string
	for _, n := range requested {
		if !addChoice(chosen, all, n) {
			unknown = append(unknown, n)
		}
	}
	return chosen, unknown
}

// addChoice adds what one requested name stands for, and reports false
// when it names neither a preset nor a forager. A blank name stands for
// nothing.
func addChoice(chosen map[string]Forager, all []Forager, n string) bool {
	key := strings.ToLower(strings.TrimSpace(n))
	if key == "" {
		return true
	}
	if preset, ok := presets[key]; ok {
		addForagers(chosen, preset(all))
		return true
	}
	w, ok := ByName(all, key)
	if ok {
		chosen[w.Name] = w
	}
	return ok
}

// addForagers adds each forager to chosen by name.
func addForagers(chosen map[string]Forager, ws []Forager) {
	for _, w := range ws {
		chosen[w.Name] = w
	}
}

// unknownForagersError lists the unknown names and every name available.
func unknownForagersError(all []Forager, unknown []string) error {
	available := make([]string, 0, len(all))
	for _, w := range all {
		available = append(available, w.Name)
	}
	return fmt.Errorf(
		"unknown forager(s): %s — available: %s, default, all, minimal, balanced",
		strings.Join(unknown, ", "),
		strings.Join(available, ", "),
	)
}

// byNameOrder lists the chosen foragers sorted by Name.
func byNameOrder(chosen map[string]Forager) []Forager {
	out := make([]Forager, 0, len(chosen))
	for _, w := range chosen {
		out = append(out, w)
	}
	sortByName(out)
	return out
}

// SplitByArchetype partitions a swarm into lens foragers and dreamer
// foragers (in that frontmatter-declared order, sorted by name within
// each group). The runner uses this to emit the workflow YAML in two
// blocks: lens nodes (parallel + Queen fan-in) and dreamer
// nodes (sequential post-synthesis ripening).
func SplitByArchetype(swarm []Forager) (lens, dreamers []Forager) {
	for _, w := range swarm {
		if w.IsDreamer() {
			dreamers = append(dreamers, w)
		} else {
			lens = append(lens, w)
		}
	}
	sortByName(lens)
	sortByName(dreamers)
	return lens, dreamers
}

// sortByName sorts foragers by Name, keeping the order of equal names.
func sortByName(ws []Forager) {
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].Name < ws[j].Name })
}
