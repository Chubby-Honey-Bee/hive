package foragers

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
)

// foragerBond doubles as the generic edge type for the YAML `edges:`
// list. For bond edges From/To carry the bare forager names + Kind;
// for the fully-wired edge list (built below) From/To carry the
// final node names and Condition carries a decision predicate.
type foragerBond struct {
	To        string // dependent forager name (this forager depends on `From`)
	From      string // upstream forager name
	Kind      string
	Weight    float64
	Condition string // decision-branch predicate (empty for unconditional edges)
}

// foragerNode is one lens forager's node in the template.
type foragerNode struct {
	Name       string
	Model      string // the pinned model; "" when the tier chooses
	Title      string
	Lens       string
	Jungian    string
	IFSRole    string
	Schema     string   // the persona's output_schema as one line of JSON; "" when none
	Persona    string   // body of the forager persona file (block-quoted)
	CitesFrom  []string // upstream foragers whose verdicts get substituted
	Inversions []string // upstream foragers whose verdicts get inverted
	Resonators []string // foragers in this swarm bonded resonates (no edge)
	// AbsentResonators are the resonates partners outside this swarm,
	// with which no ∇ can fire.
	AbsentResonators []string
	// Part and ContextKey: under ContextSplit, which part of the
	// context this lens reads, 1-based, and the input it reads it from.
	Part       int
	ContextKey string
}

// directNode is the direct voice's node: the solo prompt, indented for
// the YAML block scalar, its schema, and its pinned model when the
// lenses have one.
type directNode struct {
	Name   string
	Title  string
	Model  string
	Prompt string
	Schema string
}

// dreamerNode is one dreamer's node in the template.
type dreamerNode struct {
	Name    string
	Title   string
	Persona string
}

// resonatePair is a machine-readable resonates bond emitted as a
// top-level `resonates:` list so the runner can register it with the
// live ∇ quorum sensor (resonates bonds produce no workflow edge).
type resonatePair struct {
	A string
	B string
}

// tmplData is what the swarm template renders.
type tmplData struct {
	Name             string
	Date             string
	Model            string
	ForagerTier      string
	SynthesizerModel string
	SynthesizerTier  string
	Reasoning        reasoningLevels
	Foragers         []foragerNode
	Edges            []foragerBond
	Dreamers         []dreamerNode
	Resonates        []resonatePair
	HumanGate        bool
	Scope            bool
	Evaluate         bool
	Followups        int
	MinDissent       int
	LensTools        string // the lens nodes' `tools:` line; empty writes none
	// SynthesizerPersona (template-v1) — when non-empty, the
	// inlined fallback synthesis prompt is replaced with this forager's
	// persona body. Already indented for YAML block-scalar nesting.
	SynthesizerPersona string
	// SynthesizerSchema is Queen's output_schema as one line of JSON:
	// the persona's, else the fallback prompt's; "" when a persona
	// declares none.
	SynthesizerSchema string
	// Ledger makes Queen and the evaluator read the swarm ledger in
	// place of every full verdict (the lean profile); Uncovered names
	// the axes no lens in the swarm owns, which the ledger's readers
	// are told.
	Ledger    bool
	Uncovered string
	// Direct is the direct voice's node; nil when DirectVoice is off.
	// ContextSplit gives each lens its own context input.
	Direct       *directNode
	ContextSplit bool
}

// templateData builds the template's data: the nodes, then the edges,
// then Queen's prompt and schema. The persona warnings follow that order.
func (s *swarmSpec) templateData(swarm []Forager) (tmplData, error) {
	foragers, err := s.foragerNodes()
	if err != nil {
		return tmplData{}, err
	}
	dreamers := s.dreamerNodes()
	edges := s.edges(foragers, dreamers)
	synthPersona, synthSchema, err := s.synthesizer()
	if err != nil {
		return tmplData{}, err
	}
	data := s.optionData()
	data.Foragers, data.Edges, data.Dreamers = foragers, edges, dreamers
	data.Resonates = resonatePairs(swarm)
	data.SynthesizerPersona, data.SynthesizerSchema = synthPersona, synthSchema
	data.Direct = s.directNode()
	return data, nil
}

// optionData is the template data the options and the lenses settle.
func (s *swarmSpec) optionData() tmplData {
	return tmplData{
		Name:             s.opts.Name,
		Date:             time.Now().UTC().Format("2006-01-02 15:04:05Z"),
		Model:            s.lensModel(0),
		ForagerTier:      s.opts.ForagerTier,
		SynthesizerModel: s.opts.SynthesizerModel,
		SynthesizerTier:  s.opts.SynthesizerTier,
		Reasoning:        reasoningLevels{s.opts.ForagerReasoning, s.opts.SynthesizerReasoning},
		HumanGate:        s.opts.HumanGate,
		Scope:            s.opts.Scope,
		Evaluate:         s.opts.Evaluate,
		Followups:        s.opts.Followups,
		MinDissent:       MinDissentChars,
		LensTools:        s.lensTools,
		Ledger:           s.lean,
		Uncovered:        uncoveredLabel(s.lens),
		ContextSplit:     s.opts.ContextSplit,
	}
}

// uncoveredLabel names the axes no lens owns, "none" when every axis is
// owned.
func uncoveredLabel(lens []Forager) string {
	if axes := uncoveredAxes(lens); len(axes) > 0 {
		return strings.Join(axes, ", ")
	}
	return "none"
}

// foragerNodes builds each lens's node, in name order.
func (s *swarmSpec) foragerNodes() ([]foragerNode, error) {
	wn := make([]foragerNode, 0, len(s.lens))
	for i, w := range s.lens {
		n, err := s.foragerNode(i, w)
		if err != nil {
			return nil, err
		}
		wn = append(wn, n)
	}
	return wn, nil
}

// foragerNode builds lens i's node.
func (s *swarmSpec) foragerNode(i int, w Forager) (foragerNode, error) {
	sch, err := schemaJSON(dispatchSchema(w))
	if err != nil {
		return foragerNode{}, fmt.Errorf("forager %q: output_schema: %w", w.Name, err)
	}
	n := foragerNode{
		Name:       w.Name,
		Model:      s.lensModel(i),
		Title:      firstNonEmpty(w.Title, capitalize(w.Name)),
		Lens:       firstLine(w.Description),
		Schema:     sch,
		Persona:    s.persona(w),
		Part:       i + 1,
		ContextKey: ContextKey(w.Name),
	}
	if !s.lean {
		n.Jungian, n.IFSRole = w.Jungian, w.IFSRole
	}
	for _, b := range w.Bonds {
		n.addBond(b, s.inSwarm)
	}
	return n, nil
}

// addBond records one of the forager's bonds on its node.
func (n *foragerNode) addBond(b Bond, inSwarm map[string]bool) {
	switch b.Kind {
	case BondCites:
		n.CitesFrom = append(n.CitesFrom, b.To)
	case BondContradicts:
		n.Inversions = append(n.Inversions, b.To)
	case BondResonates:
		n.addResonator(b.To, inSwarm)
	}
}

// addResonator records a resonates partner, in this swarm or absent from
// it.
func (n *foragerNode) addResonator(to string, inSwarm map[string]bool) {
	if inSwarm[strings.ToLower(to)] {
		n.Resonators = append(n.Resonators, to)
		return
	}
	n.AbsentResonators = append(n.AbsentResonators, to)
}

// bondFree reports whether the node waits on no upstream forager.
func (n *foragerNode) bondFree() bool {
	return len(n.CitesFrom) == 0 && len(n.Inversions) == 0
}

// dreamerNodes builds each dreamer's node, in name order.
func (s *swarmSpec) dreamerNodes() []dreamerNode {
	dn := make([]dreamerNode, 0, len(s.dreamers))
	for _, d := range s.dreamers {
		dn = append(dn, dreamerNode{
			Name:    d.Name,
			Title:   firstNonEmpty(d.Title, capitalize(d.Name)),
			Persona: indent(d.Body, "      "),
		})
	}
	return dn
}

// directNode is the direct voice's node, nil when the voice is off.
func (s *swarmSpec) directNode() *directNode {
	if !s.opts.DirectVoice {
		return nil
	}
	return &directNode{Name: DirectVoiceName, Title: directVoiceTitle, Model: s.lensModel(0), Prompt: indent(DirectPrompt, "      "), Schema: DirectSchemaJSON}
}

// edges is the full edge list with final node names baked in (the
// template emits `{from: <From>, to: <To>}` verbatim). Order:
// bonds → scope → fan-in → post-synthesis (diamond or direct).
func (s *swarmSpec) edges(wn []foragerNode, dn []dreamerNode) []foragerBond {
	edges := bondEdges(s.lens)
	edges = append(edges, s.scopeEdges(wn)...)
	edges = append(edges, s.fanInEdges()...)
	return append(edges, s.postSynthesisEdges(dn)...)
}

// bondEdges are the cites/contradicts bonds: upstream → dependent.
func bondEdges(lens []Forager) []foragerBond {
	var edges []foragerBond
	for _, w := range lens {
		for _, b := range w.Bonds {
			if b.Kind == BondResonates {
				continue // not a workflow edge — surfaced in synthesis only
			}
			edges = append(edges, foragerBond{
				From: "forager-" + b.To, To: "forager-" + w.Name, Kind: b.Kind, Weight: b.Weight,
			})
		}
	}
	return edges
}

// scopeEdges run scope before the bond-free foragers only. A forager with
// a cites/contradicts bond is already gated on its upstream, and readiness
// is join-all, so a second scope edge would be redundant. Bond-free
// foragers anchor every chain, so scope still runs before the whole swarm.
func (s *swarmSpec) scopeEdges(wn []foragerNode) []foragerBond {
	if !s.opts.Scope {
		return nil
	}
	var edges []foragerBond
	for i := range wn {
		if wn[i].bondFree() {
			edges = append(edges, foragerBond{From: "scope", To: "forager-" + wn[i].Name})
		}
	}
	return edges
}

// fanInEdges fan the lenses, and the direct voice, into synthesis. Without
// eval, lenses fan straight into Queen. With eval, they fan into
// swarm-evaluate first. Its gaps pick the path, so the evaluator's verdict
// is len(gaps) == 0, computed rather than asked for: with gaps,
// swarm-followup runs and then queen; with none, the fan is skipped and
// queen runs straight after the evaluator. Either way synthesis happens
// exactly once.
func (s *swarmSpec) fanInEdges() []foragerBond {
	synthHead := s.synthHead()
	var edges []foragerBond
	for _, w := range s.lens {
		edges = append(edges, foragerBond{From: "forager-" + w.Name, To: synthHead})
	}
	if s.opts.DirectVoice {
		edges = append(edges, foragerBond{From: "forager-" + DirectVoiceName, To: synthHead})
	}
	return append(edges, s.evaluateEdges()...)
}

// synthHead is the node the lenses fan into: swarm-evaluate under eval,
// else queen.
func (s *swarmSpec) synthHead() string {
	if s.opts.Evaluate {
		return "swarm-evaluate"
	}
	return "queen"
}

// evaluateEdges are the coverage pass's edges, under eval.
func (s *swarmSpec) evaluateEdges() []foragerBond {
	if !s.opts.Evaluate {
		return nil
	}
	return []foragerBond{
		{From: "swarm-evaluate", To: "swarm-followup", Condition: "len(gaps) > 0"},
		{From: "swarm-evaluate", To: "queen", Condition: "len(gaps) == 0"},
		{From: "swarm-followup", To: "queen"},
	}
}

// postSynthesisEdges hang the human gate off the final synthesis (always
// `queen`), and the dreamers off the gate when there is one, else off queen.
// As siblings of the gate, the dreamers would run their ripening pass before
// anyone approved, and a reject would have nothing left to halt.
func (s *swarmSpec) postSynthesisEdges(dn []dreamerNode) []foragerBond {
	dreamerHead := "queen"
	var edges []foragerBond
	if s.opts.HumanGate {
		edges = append(edges, foragerBond{From: "queen", To: "human-gate"})
		dreamerHead = "human-gate"
	}
	for _, d := range dn {
		edges = append(edges, foragerBond{From: dreamerHead, To: "dreamer-" + d.Name})
	}
	return edges
}

// synthesizer is Queen's persona text and her output_schema as one line of
// JSON.
func (s *swarmSpec) synthesizer() (string, string, error) {
	persona, sch, err := s.synthesizerSource()
	if err != nil {
		return "", "", err
	}
	text, err := schemaJSON(sch)
	if err != nil {
		return "", "", fmt.Errorf("synthesizer output_schema: %w", err)
	}
	return persona, text, nil
}

// synthesizerSource is the Queen persona's prompt and schema, or no prompt
// and the fallback schema when no Queen persona is given. A Queen whose
// contract does not parse is refused.
func (s *swarmSpec) synthesizerSource() (string, *schema.Schema, error) {
	q := s.opts.Synthesizer
	if q.Name == "" || q.Body == "" {
		return "", fallbackQueenSchema, nil
	}
	if q.ContractErr != nil {
		return "", nil, fmt.Errorf("synthesizer %q: %w", q.Name, q.ContractErr)
	}
	return s.persona(q), dispatchSchema(q), nil
}

// resonatePairs are the resonates pairs (machine-readable, for the ∇
// quorum sensor). The symmetric bonds are deduplicated and each pair and
// the list ordered, so the generated YAML is byte-stable. ValidateSwarm has
// already refused any bond with a dreamer in the swarm.
func resonatePairs(swarm []Forager) []resonatePair {
	set := &resonateSet{seen: map[string]bool{}}
	for _, w := range swarm {
		set.addBonds(w)
	}
	sort.Slice(set.pairs, func(i, j int) bool { return set.pairs[i].less(set.pairs[j]) })
	return set.pairs
}

// resonateSet collects resonates pairs, each once.
type resonateSet struct {
	pairs []resonatePair
	seen  map[string]bool
}

// addBonds adds a forager's resonates bonds.
func (r *resonateSet) addBonds(w Forager) {
	for _, b := range w.Bonds {
		if b.Kind == BondResonates {
			r.add(orderedPair(w.Name, b.To))
		}
	}
}

// add adds a pair unless it is there already.
func (r *resonateSet) add(p resonatePair) {
	key := p.A + "\x00" + p.B
	if r.seen[key] {
		return
	}
	r.seen[key] = true
	r.pairs = append(r.pairs, p)
}

// orderedPair is the pair of two names, the lesser first.
func orderedPair(a, b string) resonatePair {
	if a > b {
		a, b = b, a
	}
	return resonatePair{A: a, B: b}
}

// less orders pairs by their first name, then their second.
func (p resonatePair) less(q resonatePair) bool {
	if p.A != q.A {
		return p.A < q.A
	}
	return p.B < q.B
}
