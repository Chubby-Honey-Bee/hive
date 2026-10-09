// Package foragers is the Swarm of Foragers registry.
//
// A forager is a single analytical lens — a markdown file under foragers/
// with a YAML frontmatter header (name, title, description, default,
// tags) followed by the persona body. Each forager becomes one agent
// node in the chb workflow.
//
// This package provides:
//   - Forager struct + frontmatter parser
//   - Load(dir), LoadFS(fsys) — scans a tree for forager files
//   - Resolve(dirs...) — picks the tree: the env var, a directory on
//     disk, else the copy the binary carries
//   - Default() — returns the foragers marked default:true
//   - Filter(names) — turns a user-supplied list (incl. "default") into
//     a concrete forager slice with predictable error messages
//
// The internal/cli/ask.go command is a thin wrapper that calls
// these helpers, generates a workflow YAML, and dispatches via the
// existing runner. No new persona-loading or workflow-engine plumbing.
package foragers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
)

// Bond is one typed dependency from this forager onto another. Declared
// in the forager's frontmatter as part of the swarm; rendered into the
// generated workflow YAML as a real edge (cites/contradicts) or recorded
// as a convergence pair the synthesizer surfaces as ∇ (resonates).
//
//	bonds:
//	  - to: optimist
//	    kind: cites           # I read their verdict before producing mine
//	  - to: empiricist
//	    kind: contradicts     # my role is to invert their conclusions
//	  - to: skeptic
//	    kind: resonates       # convergence with them is surfaced as ∇ in synthesis
type Bond struct {
	To     string  `yaml:"to"`     // upstream forager name
	Kind   string  `yaml:"kind"`   // cites | contradicts | resonates
	Weight float64 `yaml:"weight"` // optional; defaults to 1.0
}

// Coverage declares which WASP / CDE / MSS axes a forager explicitly owns.
// Used by the `minimal` preset to assemble the smallest swarm that covers
// every load-bearing axis of the chronomancy framework.
//
// WASP axes: k (workload), E (environment), I (intent), T (time), F (fidelity).
// CDE phases: detect, decompose, encode, execute. (Encode is owned by
// Queen and never declared on a lens forager.)
// MSS labels: def, gua, asm, unk.
//
// A forager may cover one, two, or all three frameworks. A forager without
// any coverage entry is valid — it's specialist material that doesn't
// participate in framework-completeness audits.
type Coverage struct {
	Wasp string `yaml:"wasp"` // "k" | "E" | "I" | "T" | "F"
	Cde  string `yaml:"cde"`  // "detect" | "decompose" | "execute" (encode = Queen)
	Mss  string `yaml:"mss"`  // "def" | "gua" | "asm" | "unk"
}

// declared reports whether the coverage names any axis.
func (c Coverage) declared() bool {
	return c.Wasp != "" || c.Cde != "" || c.Mss != ""
}

// Forager is one analytical lens — the parsed frontmatter + persona body
// of a single foragers/<name>.md file.
//
// Three archetypes:
//
//	lens     (default) — takes {question, context}, emits a verdict.
//	         Every shipped forager but Dreamer and Queen is a lens.
//	dreamer  — takes the live Comb (no question), runs the five
//	         consolidation passes (prune→reprove→contradict→
//	         hypothesize→settle), emits signals + marks vantages stale.
//	         Dispatched by the runner via internal/dreamer.Run instead
//	         of an LLM backend.
//	synthesizer — Queen. Reads every lens forager's full verdict and
//	         returns one JSON object, her Markdown in `report`. The swarm
//	         generator always adds it as the `queen` node; no preset
//	         selects it and naming it is refused.
//
// Optional Jungian / IFS tags are decorative — they document the lens
// the forager takes (e.g. shadow, magician, sage) and the IFS role
// (manager, firefighter, exile, self) so future tooling can compose
// swarms by archetype family.
type Forager struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Default     bool     `yaml:"default"`
	Tags        []string `yaml:"tags"`

	// Coverage: which WASP/CDE/MSS axes this forager owns. Optional.
	// When non-empty, the forager is eligible for the `minimal` preset.
	Coverage Coverage `yaml:"coverage"`

	// Archetype: lens | dreamer | synthesizer (default lens).
	// NormalizeArchetype refuses any other value.
	Archetype string `yaml:"archetype"`

	// Bonds: typed dependencies onto other foragers. Empty slice when
	// omitted. The shorthand `depends_on: [name1, name2]` is equivalent
	// to `bonds: [{to: name1, kind: cites}, {to: name2, kind: cites}]`.
	Bonds []Bond `yaml:"bonds"`

	// DependsOn: shorthand for `bonds: [{to: <name>, kind: cites}, …]`.
	// Merged into Bonds during NormalizeArchetype.
	DependsOn []string `yaml:"depends_on"`

	// Jungian: optional tag — magician/sage/shadow/trickster/anima/
	// animus/hero/self. Decorative; surfaces in `chb list`.
	Jungian string `yaml:"jungian"`

	// IFSRole: optional tag — manager/firefighter/exile/self. Same.
	IFSRole string `yaml:"ifs_role"`

	// Sigil is the single-glyph visual marker rendered next to the
	// forager's name in `chb list` and the /comb HTML page. The
	// shipped foragers each carry a distinct glyph that matches their
	// archetype (e.g. ☉ for optimist, ♂ for skeptic).
	// Empty defaults to "•" so unstyled custom foragers still render.
	Sigil string `yaml:"sigil"`

	// Accent is the forager's hex color (`#RRGGBB`) used for ANSI
	// 24-bit terminal output and HTML rendering on /comb. Empty
	// defaults to a neutral gray that reads on the dark Comb palette.
	Accent string `yaml:"accent"`

	// RenderLayer (template-v1) — when true, the forager is a render-time
	// persona only, excluded from all deliberation presets (default,
	// minimal, balanced, all). Pairs with DeliberationEligible: false.
	// Editor is the first forager to carry this flag.
	RenderLayer bool `yaml:"render_layer"`

	// DeliberationEligible (template-v1) — when explicitly set to false,
	// excludes this forager from deliberation presets. Defaults to true
	// (a nil/missing value means eligible). Pairs with RenderLayer: true.
	DeliberationEligible *bool `yaml:"deliberation_eligible"`

	// OutputSchema is the persona's output_schema, its properties in the
	// order the frontmatter lists them, with length_caps' max_items
	// compiled in as maxItems. nil when the file declares none. The swarm
	// generator puts it on the persona's node.
	OutputSchema *schema.Schema `yaml:"-"`
	// ContractErr is why output_schema or length_caps would not parse. The
	// file still loads, so naming it is not an unknown forager; generation
	// refuses a swarm or synthesizer holding one.
	ContractErr error `yaml:"-"`

	// CounterBias is behavioral_floor.counter_bias_clause, and
	// CounterBiasWhenAbsent the forager whose absence from the swarm the
	// clause is conditioned on (behavioral_floor.counter_bias_when_absent),
	// lowercased; "" when it always applies. Generation writes the clause,
	// resolved against the swarm, into the persona's prompt.
	CounterBias           string `yaml:"-"`
	CounterBiasWhenAbsent string `yaml:"-"`

	// Body is the persona content after the frontmatter. The runner
	// loads it as the agent's system prompt.
	Body string `yaml:"-"`
	// File is the forager's file name within its tree (Load's directory,
	// or the copy the binary carries), such as optimist.md.
	File string `yaml:"-"`
}

// IsDeliberationEligible reports whether the forager can participate in
// deliberation presets (default, minimal, balanced, all). Returns false
// when render_layer is true OR deliberation_eligible is explicitly false
// OR the archetype is synthesizer (which is dispatched separately by
// the swarm generator, not as a deliberation seat).
//
// A nil DeliberationEligible defaults to eligible — so any forager that
// omits both flags participates as before.
func (w *Forager) IsDeliberationEligible() bool {
	return w.Archetype != ArchetypeSynthesizer && !w.RenderLayer && !w.optedOut()
}

// optedOut reports whether deliberation_eligible is explicitly false.
func (w *Forager) optedOut() bool {
	return w.DeliberationEligible != nil && !*w.DeliberationEligible
}

// DefaultSigil is the fallback rendered when a forager's frontmatter
// omits `sigil:`. Round bullet so it always lays out cleanly.
const DefaultSigil = "•"

// DefaultAccent is the fallback hex color rendered when a forager's
// frontmatter omits `accent:`. Mid-gray, readable on the dark Comb
// palette without competing for attention.
const DefaultAccent = "#9098A8"

// Archetype constants. Strings match forager frontmatter values.
const (
	ArchetypeLens        = "lens"
	ArchetypeDreamer     = "dreamer"
	ArchetypeSynthesizer = "synthesizer"
)

// Bond kinds (string values match the forager_bonds.bond_kind enum and
// the db.BondKind constants).
const (
	BondCites       = "cites"
	BondContradicts = "contradicts"
	BondResonates   = "resonates"
)

// NormalizeArchetype fills Archetype with the default ("lens") when
// blank, validates Bonds + DependsOn, and merges DependsOn into Bonds
// as cites-edges. Returns an error when archetype or bond kind is
// invalid. Idempotent — re-running on a normalised forager is a no-op.
func (w *Forager) NormalizeArchetype() error {
	if err := w.setArchetype(); err != nil {
		return err
	}
	w.mergeDependsOn()
	if err := w.normalizeBonds(); err != nil {
		return err
	}
	w.applyThemeDefaults()
	return nil
}

// setArchetype defaults a blank archetype to lens, lower-cases it, and
// refuses one that is not lens, dreamer or synthesizer.
func (w *Forager) setArchetype() error {
	if strings.TrimSpace(w.Archetype) == "" {
		w.Archetype = ArchetypeLens
	}
	w.Archetype = strings.ToLower(strings.TrimSpace(w.Archetype))
	if !validArchetype(w.Archetype) {
		return fmt.Errorf(
			"forager %q: unknown archetype %q (want lens|dreamer|synthesizer)",
			w.Name, w.Archetype,
		)
	}
	return nil
}

// validArchetype reports whether a is lens, dreamer or synthesizer.
func validArchetype(a string) bool {
	switch a {
	case ArchetypeLens, ArchetypeDreamer, ArchetypeSynthesizer:
		return true
	}
	return false
}

// mergeDependsOn merges the depends_on shorthand into Bonds as cites.
func (w *Forager) mergeDependsOn() {
	for _, dep := range w.DependsOn {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		w.Bonds = append(w.Bonds, Bond{To: dep, Kind: BondCites, Weight: 1.0})
	}
	w.DependsOn = nil
}

// normalizeBonds validates every bond and fills its defaults.
func (w *Forager) normalizeBonds() error {
	for i := range w.Bonds {
		if err := w.normalizeBond(i); err != nil {
			return err
		}
	}
	return nil
}

// normalizeBond trims bond i, defaults its kind to cites and its weight to
// 1.0, and refuses an empty target or an unknown kind.
func (w *Forager) normalizeBond(i int) error {
	b := &w.Bonds[i]
	b.To = strings.TrimSpace(b.To)
	b.Kind = strings.ToLower(strings.TrimSpace(b.Kind))
	if b.To == "" {
		return fmt.Errorf("forager %q: bond %d has empty `to`", w.Name, i)
	}
	b.Kind = orDefault(b.Kind, BondCites)
	if !validBondKind(b.Kind) {
		return fmt.Errorf(
			"forager %q: bond %d has unknown kind %q (want cites|contradicts|resonates)",
			w.Name, i, b.Kind,
		)
	}
	if b.Weight == 0 {
		b.Weight = 1.0
	}
	return nil
}

// validBondKind reports whether k is cites, contradicts or resonates.
func validBondKind(k string) bool {
	switch k {
	case BondCites, BondContradicts, BondResonates:
		return true
	}
	return false
}

// applyThemeDefaults fills the title, sigil and accent a forager omits.
func (w *Forager) applyThemeDefaults() {
	// A blank title defaults to the capitalised name here, so every
	// registry consumer (palette.json, `chb list`) sees the title the
	// generated prompt uses, not only GenerateWorkflow.
	if strings.TrimSpace(w.Title) == "" {
		w.Title = capitalize(w.Name)
	}
	// Theme defaults — applied after archetype + bond normalization so
	// the rest of the registry still sees the user's frontmatter values
	// when set, falls back to neutral when omitted. The shipped foragers
	// always set these explicitly; defaults exist so a user-authored
	// forager without theming still renders.
	if strings.TrimSpace(w.Sigil) == "" {
		w.Sigil = DefaultSigil
	}
	w.Accent = normalizedAccent(w.Accent)
}

// normalizedAccent is the accent trimmed, DefaultAccent when blank. A bare
// RGB string quietly gains an explicit hash prefix so consumers can write
// `style="color:{accent}"` blindly.
func normalizedAccent(accent string) string {
	accent = strings.TrimSpace(accent)
	if accent == "" {
		return DefaultAccent
	}
	if !strings.HasPrefix(accent, "#") {
		return "#" + accent
	}
	return accent
}

// IsLens reports whether the forager runs as a lens (the default
// archetype — takes a question, emits a verdict).
func (w *Forager) IsLens() bool {
	return w.Archetype == "" || w.Archetype == ArchetypeLens
}

// AccentRGB parses the forager's Accent ("#RRGGBB") into three uint8
// channels. Returns (0,0,0) on parse failure — callers should treat
// that as "use the terminal default" rather than emit a black sigil.
func (w *Forager) AccentRGB() (r, g, b uint8) {
	s := strings.TrimPrefix(w.Accent, "#")
	if len(s) != 6 {
		return 0, 0, 0
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v)
}

// AnsiPrefix returns the 24-bit ANSI escape code that sets the
// foreground to the forager's accent color. Pair with AnsiReset
// (or "\x1b[0m") at the end of the styled span. Returns "" when
// the accent is unparsable — caller falls back to plain text.
func (w *Forager) AnsiPrefix() string {
	r, g, b := w.AccentRGB()
	if r == 0 && g == 0 && b == 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// AnsiReset is the universal ANSI sequence to reset all attributes
// back to the terminal default. Always pair with AnsiPrefix.
const AnsiReset = "\x1b[0m"

// IsDreamer reports whether the forager runs the dreamer dispatch path.
func (w *Forager) IsDreamer() bool {
	return w.Archetype == ArchetypeDreamer
}
