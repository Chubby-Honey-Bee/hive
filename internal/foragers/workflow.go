package foragers

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/schema"
)

//go:embed swarm.go.tmpl
var swarmTemplate string

// WorkflowOptions parameterizes the generated chb workflow YAML.
type WorkflowOptions struct {
	// Name is embedded into the workflow YAML's `name:` field. Default:
	// "swarm-of-foragers-<date>".
	Name string

	// Model used for each forager agent. Empty string + empty ForagerTier
	// causes the generator to emit `tier: synthesist` (the new default
	// as of 2026-05-06 — every swarm respects --budget-mode). Set
	// Model explicitly to "sonnet" / "haiku" / a literal id to pin a
	// specific model and bypass the tier system. A comma-separated list
	// mixes models across the lenses: the lenses, in name order, take the
	// models in turn, and the follow-up fan takes the first.
	Model string

	// ForagerTier controls the tier role for each forager agent when
	// Model is empty. Default: "synthesist". The runner's tier
	// resolver picks the model per --budget-mode (premium → sonnet,
	// standard → haiku, etc.).
	ForagerTier string

	// SynthesizerModel is the model used by Queen, the
	// synthesis node. Empty string + empty SynthesizerTier causes the
	// generator to emit `tier: planner` so Queen respects
	// --budget-mode.
	SynthesizerModel string

	// SynthesizerTier controls the tier role for Queen when
	// SynthesizerModel is empty. Default: "planner".
	SynthesizerTier string

	// ForagerReasoning is the `reasoning:` level (none, low, medium or
	// high; models.ReasoningLevels) of the lens nodes: the foragers and
	// swarm-followup. SynthesizerReasoning is the same for scope,
	// swarm-evaluate and Queen. Empty writes no `reasoning:` key, so the
	// server's default applies; a Qwen model on Ollama then thinks.
	ForagerReasoning     string
	SynthesizerReasoning string

	// HumanGate inserts a `waiting_human` checkpoint after Queen
	// so a human can approve the verdict before the dreamer ripening
	// pass runs: every dreamer node hangs off the gate, not off Queen,
	// so a reject leaves them pending. Default: false (fully automated).
	// Surface the flag only when the user explicitly opts in.
	HumanGate bool

	// Scope adds a planner-tier `scope` pre-node that sharpens the raw
	// question into one decidable sentence (+ named assumptions) before
	// the lenses fan out. Every forager then reasons about {scoped_question}
	// instead of the bare {question}. The scope→forager edge is added only
	// for bond-free foragers (no cites/contradicts). Readiness is join-all
	// (findReadyNodes waits for every predecessor), and a bonded forager
	// already waits on an upstream that waits on scope, so its own scope
	// edge would be redundant. Specified in docs/specs/swarm.md
	// § Workflow generation.
	Scope bool

	// Evaluate puts a coverage pass between the lenses and Queen:
	// foragers → `swarm-evaluate` (scores coverage, names gaps) →
	// `swarm-followup` (one fresh lens for each of the first Followups
	// gaps) → queen, which synthesizes once with the follow-up findings
	// and the gaps past the limit by name. The edges test the evaluator's
	// gaps: with none, the follow-up fan is skipped and queen runs straight
	// after the evaluator. There is no draft synthesis and no decision node.
	Evaluate bool

	// Followups caps the follow-up fan: the first Followups gaps get a
	// fresh lens, and the rest reach Queen by name as {gaps_unfollowed}.
	// Default 3; a negative value is refused.
	Followups int

	// LensTools names the tools each forager and follow-up lens may use:
	// "none" (the default) sends no tools, "read" allows read_file, glob
	// and grep for a question that needs the repository, and "all" writes
	// no tools: key, so the lens keeps every tool. Scope, the evaluator and
	// Queen never get tools.
	LensTools string

	// Synthesizer is the Queen forager loaded from foragers/queen.md
	// (template-v1). When set, the generator uses this forager's persona
	// body as the synthesis prompt. When zero-valued — a roster with no
	// queen.md, or a test — it uses the inlined fallback synthesis prompt.
	//
	// The caller is responsible for looking up Queen by name
	// from the full registry — Queen is filtered out of
	// deliberation presets by IsDeliberationEligible, so it won't
	// appear in the `swarm` argument to GenerateWorkflow.
	Synthesizer Forager

	// PersonaProfile is ProfileFull (the default) or ProfileLean: how much
	// of each persona the prompts carry, and whether Queen and the coverage
	// evaluator read every forager's full verdict or the swarm ledger.
	PersonaProfile string

	// PersonaSections, when non-nil, names the persona body sections every
	// forager and Queen render, in place of the profile's: the whole body
	// under full, LeanSections under lean.
	PersonaSections []int

	// Warn, when set, is told of each persona that renders whole because
	// sections were asked for and its body has no section markers.
	Warn func(string)

	// DirectVoice adds forager-<DirectVoiceName>: the model's own answer to
	// the question and the whole context, with no persona, under
	// DirectPrompt. It casts one vote in the tally, declares no bond and
	// fires no ∇ (swarm.md § The direct voice).
	DirectVoice bool

	// ContextSplit gives each lens forager, in name order, its own part of
	// the context, read from the input ContextKey(name), which the caller
	// fills with ContextParts. The direct voice and scope keep {context}
	// whole (swarm.md § The context split).
	ContextSplit bool
}

// WithDefaults fills in empty fields.
//
// Tier-vs-model precedence: an explicit Model wins (Model="sonnet"
// emits `model: sonnet`, ignoring ForagerTier). Otherwise ForagerTier
// wins (default "synthesist"). Same precedence for synthesizer.
func (o WorkflowOptions) WithDefaults() WorkflowOptions {
	out := o
	out.Name = orDefault(out.Name, "swarm-of-foragers-"+time.Now().UTC().Format("2006-01-02"))
	out.ForagerTier = tierDefault(out.Model, out.ForagerTier, "synthesist")
	out.SynthesizerTier = tierDefault(out.SynthesizerModel, out.SynthesizerTier, "planner")
	if out.Followups == 0 {
		out.Followups = DefaultFollowups
	}
	out.LensTools = orDefault(out.LensTools, "none")
	out.PersonaProfile = orDefault(out.PersonaProfile, ProfileFull)
	return out
}

// orDefault is v, or def when v is empty.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// tierDefault is the tier a node takes: tier, or def when neither a model
// nor a tier is set.
func tierDefault(model, tier, def string) string {
	if model == "" && tier == "" {
		return def
	}
	return tier
}

// DefaultFollowups is how many evaluator gaps get a follow-up lens when
// WorkflowOptions.Followups is unset.
const DefaultFollowups = 3

// MinDissentChars is the shortest dissent_from_plurality Queen's accept:
// takes as a reason for departing from the tally's plurality. It refuses the
// placeholders small models write into a field they mean to leave empty
// ("None", "N/A", "Not applicable"); it cannot tell a sentence with a reason
// from one without.
const MinDissentChars = 20

// lensToolSets maps each --lens-tools value to the `tools:` line a lens
// node carries. "all" carries none, so the lens keeps every tool.
var lensToolSets = map[string]string{
	"none": "tools: []",
	"read": "tools: [read_file, glob, grep]",
	"all":  "",
}

// reasoningLevels are the `reasoning:` levels the template writes: Forager
// on the lens nodes, Synthesizer on scope, swarm-evaluate and Queen; ""
// writes no key.
type reasoningLevels struct{ Forager, Synthesizer string }

// GenerateWorkflow produces a workflow YAML where each lens forager
// becomes one parallel agent node, plus Queen (the synthesizer)
// fanned in by every lens, optionally followed by a dreamer ripening
// pass.
//
// Bonds are rendered as real workflow edges:
//
//	cites        forager-<from> → forager-<to>; the dependent forager's
//	             prompt has {comb.forager:<from>} substituted at dispatch.
//	contradicts  same edge as cites, but the dependent forager's prompt
//	             template is wrapped with an inversion preamble.
//	resonates    no workflow edge; instead emitted as a machine-readable
//	             `resonates:` pair so the runner registers it with the live
//	             ∇ quorum sensor (internal/comb.QuorumSensor). The sensor
//	             watches the Comb bus and records each bonded-verdict
//	             convergence as it fires (a forager_bonds row + a `nabla`
//	             signal). Queen reads the fired pairs through
//	             {nabla.fired}, which the engine computes from this run's
//	             verdicts, and reports them in its ∇ Convergences section.
//	             (Convergence never makes a guarantee, for
//	             verdicts or findings.)
//
// The function calls ValidateSwarm first; bond cycles, missing-name
// references, and unknown archetypes all error out before generation.
func GenerateWorkflow(swarm []Forager, opts WorkflowOptions) (string, error) {
	spec, err := newSwarmSpec(swarm, opts)
	if err != nil {
		return "", err
	}
	data, err := spec.templateData(swarm)
	if err != nil {
		return "", err
	}
	return renderWorkflow(data)
}

// swarmSpec is what GenerateWorkflow resolves before it builds a node: the
// options with their defaults, the lens nodes' tools line, the persona
// sections, the lens models, and the swarm split by archetype.
type swarmSpec struct {
	opts       WorkflowOptions
	lensTools  string
	sections   []int
	lean       bool
	lensModels []string
	lens       []Forager
	dreamers   []Forager
	// inSwarm holds every forager's name, lower-cased.
	inSwarm map[string]bool
}

// newSwarmSpec checks the swarm and the options, in the order their errors
// are reported, and resolves them.
func newSwarmSpec(swarm []Forager, opts WorkflowOptions) (*swarmSpec, error) {
	if err := checkSwarm(swarm); err != nil {
		return nil, err
	}
	s := &swarmSpec{opts: opts.WithDefaults()}
	if err := s.resolveOptions(); err != nil {
		return nil, err
	}
	if err := s.splitSwarm(swarm); err != nil {
		return nil, err
	}
	return s, nil
}

// checkSwarm refuses an empty swarm and one ValidateSwarm refuses.
func checkSwarm(swarm []Forager) error {
	if len(swarm) == 0 {
		return errors.New("swarm must contain at least one forager")
	}
	return ValidateSwarm(swarm)
}

// resolveOptions checks and resolves each option in turn.
func (s *swarmSpec) resolveOptions() error {
	for _, resolve := range []func() error{
		s.resolveLensTools,
		s.checkFollowups,
		s.resolveSections,
		s.checkReasoning,
		s.resolveLensModels,
	} {
		if err := resolve(); err != nil {
			return err
		}
	}
	return nil
}

// resolveLensTools looks up the lens nodes' tools line.
func (s *swarmSpec) resolveLensTools() error {
	tools, ok := lensToolSets[s.opts.LensTools]
	if !ok {
		return fmt.Errorf("lens tools %q: want none, read or all", s.opts.LensTools)
	}
	s.lensTools = tools
	return nil
}

// checkFollowups refuses a negative follow-up count.
func (s *swarmSpec) checkFollowups() error {
	if s.opts.Followups < 0 {
		return fmt.Errorf("followups %d: want a positive count", s.opts.Followups)
	}
	return nil
}

// resolveSections resolves the persona profile into the sections every
// persona renders: PersonaSections, else under lean LeanSections, else the
// whole body.
func (s *swarmSpec) resolveSections() error {
	s.sections = s.opts.PersonaSections
	switch s.opts.PersonaProfile {
	case ProfileFull:
	case ProfileLean:
		if s.sections == nil {
			s.sections = LeanSections
		}
	default:
		return fmt.Errorf("persona profile %q: want %s or %s", s.opts.PersonaProfile, ProfileFull, ProfileLean)
	}
	s.lean = s.opts.PersonaProfile == ProfileLean
	return nil
}

// checkReasoning refuses a reasoning level the workflow engine does not
// know.
func (s *swarmSpec) checkReasoning() error {
	for _, r := range []struct{ role, level string }{{"forager", s.opts.ForagerReasoning}, {"synthesizer", s.opts.SynthesizerReasoning}} {
		if r.level == "" {
			continue
		}
		if err := models.CheckReasoningLevel(r.level); err != nil {
			return fmt.Errorf("%s %w", r.role, err)
		}
	}
	return nil
}

// resolveLensModels splits the Model option into the lenses' models.
func (s *swarmSpec) resolveLensModels() error {
	list, err := lensModelList(s.opts.Model)
	s.lensModels = list
	return err
}

// lensModelList is a comma-separated model list, refusing an empty entry;
// nil for no model.
func lensModelList(model string) ([]string, error) {
	if model == "" {
		return nil, nil
	}
	var list []string
	for _, m := range strings.Split(model, ",") {
		if m = strings.TrimSpace(m); m == "" {
			return nil, fmt.Errorf("model list %q: an entry is empty", model)
		}
		list = append(list, m)
	}
	return list, nil
}

// splitSwarm splits the swarm into lenses and dreamers, refusing one with
// no lens, and refuses a forager whose node would collide with the direct
// voice's.
func (s *swarmSpec) splitSwarm(swarm []Forager) error {
	s.lens, s.dreamers = SplitByArchetype(swarm)
	if len(s.lens) == 0 {
		return errors.New("swarm must contain at least one lens forager (archetype: lens or unset)")
	}
	s.inSwarm = lowerCaseNames(swarm)
	if s.directVoiceCollides() {
		return fmt.Errorf("forager %q collides with the direct voice's node, forager-%s; rename it or leave --direct-voice off", DirectVoiceName, DirectVoiceName)
	}
	return nil
}

// lowerCaseNames is the set of the swarm's names, lower-cased.
func lowerCaseNames(swarm []Forager) map[string]bool {
	set := make(map[string]bool, len(swarm))
	for _, w := range swarm {
		set[strings.ToLower(w.Name)] = true
	}
	return set
}

// directVoiceCollides reports whether the direct voice is on and a forager
// in the swarm, or the synthesizer, has its name.
func (s *swarmSpec) directVoiceCollides() bool {
	return s.opts.DirectVoice && (s.inSwarm[DirectVoiceName] || strings.EqualFold(s.opts.Synthesizer.Name, DirectVoiceName))
}

// lensModel is the model lens i is pinned to: the lens models in turn, or
// "" when the tier chooses.
func (s *swarmSpec) lensModel(i int) string {
	if len(s.lensModels) == 0 {
		return ""
	}
	return s.lensModels[i%len(s.lensModels)]
}

// persona is the persona text a node's prompt carries, indented for the
// YAML block scalar: the body as the profile renders it, then the
// counter-bias clause resolved against this swarm.
func (s *swarmSpec) persona(w Forager) string {
	text, ok := renderPersona(w.Body, s.sections)
	if !ok && s.opts.Warn != nil {
		s.opts.Warn(fmt.Sprintf("%s's persona has no § section markers, so it renders whole", w.Name))
	}
	if cb := counterBiasBlock(w, s.inSwarm); cb != "" {
		text = strings.TrimRight(text, "\n") + "\n\n" + cb
	}
	return indent(text, "      ")
}

// renderWorkflow executes the swarm template over data.
func renderWorkflow(data tmplData) (string, error) {
	t, err := template.New("swarm").Delims("<<", ">>").Parse(swarmTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// fallbackQueenSchema is the schema of the inlined fallback synthesis prompt,
// the shape queen.md declares, decision fields last. Every property is
// required, so a grammar writes them all in this order.
var fallbackQueenSchema = mustSchema(`
type: object
required: [report, convergence, coverage, gaps, dissent_from_plurality, verdict, recommendation]
properties:
  report: {type: string}
  convergence: {enum: [high, medium, low]}
  coverage: {type: integer, minimum: 1, maximum: 5}
  gaps: {type: array, items: {type: string}}
  dissent_from_plurality: {type: string}
  verdict: {enum: [support, oppose, conditional, abstain]}
  recommendation: {type: string}
`)

func mustSchema(text string) *schema.Schema {
	s, err := schema.ParseYAML([]byte(text))
	if err != nil {
		panic(err)
	}
	return s
}

// schemaJSON renders a schema as one line of JSON, which is YAML too and
// keeps the property order; "" for nil.
func schemaJSON(s *schema.Schema) (string, error) {
	if s == nil {
		return "", nil
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// indent prefixes every line of s with prefix. Used to nest the forager
// persona body inside a YAML block scalar without breaking indentation.
func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// capitalize upper-cases s's first character, whatever its width.
func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return ""
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
