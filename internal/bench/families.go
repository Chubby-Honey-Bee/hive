package bench

import (
	"crypto/sha256"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// Verdicts an item can expect.
const (
	Support = "support"
	Oppose  = "oppose"
	Abstain = "abstain"
)

// TokenKind names the canonical token a family's question asks the model
// to write for each thing it names.
type TokenKind string

// The token kinds; TokensNone asks for no token.
const (
	TokensNone  TokenKind = ""
	TokensPair  TokenKind = "pair"  // a⇄b, the two names in alphabetical order
	TokensArrow TokenKind = "arrow" // a→b, where a declares the bond
	TokensName  TokenKind = "name"  // @name
)

// Answer is what an item expects: a verdict, and the tokens the question
// asks for (empty when there is nothing to name).
type Answer struct {
	Verdict string   `json:"verdict"`
	Tokens  []string `json:"tokens,omitempty"`
}

// Item is one question, the roster pack it is asked over, and its answer.
type Item struct {
	ID       string    `json:"id"`      // <family>-s<seed>-<variant>
	Pair     string    `json:"pair"`    // <family>-s<seed>
	Family   string    `json:"family"`  // F1, F2, F3, F4, F6, F8, AB
	Seed     int64     `json:"seed"`    // generator seed
	Variant  string    `json:"variant"` // a: derived from the live roster; b: its answer-flipping twin
	Question string    `json:"question"`
	Context  string    `json:"context"`
	Tokens   TokenKind `json:"token_kind,omitempty"`
	Want     Answer    `json:"want"`
	// Names are the roster names a token may use; the extractor ignores any other.
	Names []string `json:"-"`
}

// Hash identifies an item by content, so two runs pair on the same
// question and pack even if the live roster changed between them.
func (it Item) Hash() string {
	sum := sha256.Sum256([]byte(it.Question + "\x00" + it.Context))
	return fmt.Sprintf("%x", sum[:8])
}

// built is one twin pair before it becomes two Items: the question both
// ask, the two rosters, and the answer function bound to the question.
type built struct {
	question string
	a, b     []foragers.Forager
	answer   func([]foragers.Forager) Answer
}

type family struct {
	id, slug string
	tokens   TokenKind
	build    func(live []foragers.Forager, rng *rand.Rand) (built, error)
}

const (
	preamble = "Answer from the forager roster in the context below. It may differ from any forager files you can see; use only the roster. " +
		"A forager is deliberation-eligible unless its archetype is synthesizer, it sets render_layer: true, or it sets deliberation_eligible: false. "
	minimalRule = "The minimal preset holds every deliberation-eligible forager that declares at least one coverage axis (wasp, cde or mss). "
	orphanRule  = "A resonates bond is orphaned when exactly one of its two foragers is in the preset. A pair counts once, whichever forager declares it. "
	closing     = " Emit exactly one verdict: support, oppose or abstain. Emit abstain if the roster lacks an entry the question needs."
)

var tokenAsk = map[TokenKind]string{
	TokensPair:  " Name each one as `a⇄b`, the two forager names in alphabetical order, and write no other pair in that form.",
	TokensArrow: " Name each one as `a→b`, where a is the forager that declares the bond, and write no other bond in that form.",
	TokensName:  " Name each one as `@name`, and write no other forager in that form.",
}

// Families lists every family in generation order.
func Families() []string {
	out := make([]string, 0, len(registry))
	for _, f := range registry {
		out = append(out, f.id)
	}
	return out
}

var registry = []family{
	orphanFamily("F1", "minimal-orphan-resonates", "minimal", func(r []foragers.Forager) (string, []foragers.Forager) {
		return minimalRule, foragers.Minimal(r)
	}),
	orphanFamily("F2", "balanced-orphan-resonates", "balanced", func(r []foragers.Forager) (string, []foragers.Forager) {
		m := foragers.Balanced(r)
		return "The balanced preset holds exactly these foragers: " + strings.Join(names(m), ", ") + ". ", m
	}),
	orphanFamily("F3", "all-orphan-resonates", "all", func(r []foragers.Forager) (string, []foragers.Forager) {
		return "The all preset holds every deliberation-eligible forager. ", allPreset(r)
	}),
	{id: "F4", slug: "wasp-i-owner", tokens: TokensName, build: buildWaspI},
	{id: "F6", slug: "minimal-in-preset-contradicts", tokens: TokensArrow, build: buildContradicts},
	{id: "F8", slug: "minimal-mss-axis", tokens: TokensName, build: buildMSS},
	{id: "AB", slug: "missing-entry", tokens: TokensNone, build: buildMissing},
}

// Generate builds one twin pair per family per seed from the live roster.
// Family IDs select a subset (empty means all). A pair whose question and
// pack repeat one already generated in this call is redrawn from the same
// seed's generator, so the item set is a function of the family and seed
// lists alone.
func Generate(live []foragers.Forager, families []string, seeds []int64) ([]Item, error) {
	fams, err := selectFamilies(families)
	if err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("no seeds")
	}
	g := generator{live: live, roster: names(live), seen: map[string]string{}}
	return g.items(fams, seeds)
}

// maxDraws is how many times a pair is redrawn before Generate gives up on
// one distinct from those already generated.
const maxDraws = 64

// generator draws twin pairs from the live roster and remembers every item
// it has drawn, so that none repeats.
type generator struct {
	live   []foragers.Forager
	roster []string
	seen   map[string]string // item hash → item ID
}

// items draws one pair per family per seed, families outermost.
func (g *generator) items(fams []family, seeds []int64) ([]Item, error) {
	var items []Item
	for _, f := range fams {
		for _, seed := range seeds {
			pair, err := g.pair(f, seed)
			if err != nil {
				return nil, err
			}
			items = append(items, pair...)
		}
	}
	return items, nil
}

// pair draws f's twin pair for seed, redrawing from the same seed's
// generator until neither item repeats one already drawn, and records it.
func (g *generator) pair(f family, seed int64) ([]Item, error) {
	rng := rand.New(rand.NewPCG(uint64(seed), salt(f.id)))
	for attempt := 0; attempt < maxDraws; attempt++ {
		pair, err := g.draw(f, seed, rng)
		if err != nil {
			return nil, err
		}
		if g.fresh(pair) {
			g.record(pair)
			return pair, nil
		}
	}
	return nil, fmt.Errorf("%s seed %d: no pair distinct from those already generated after %d draws", f.id, seed, maxDraws)
}

// draw builds one twin pair of f from a fresh copy of the live roster. A
// pair whose twins expect one verdict is an error.
func (g *generator) draw(f family, seed int64, rng *rand.Rand) ([]Item, error) {
	bt, err := f.build(cloneRoster(g.live), rng)
	if err != nil {
		return nil, fmt.Errorf("%s seed %d: %w", f.id, seed, err)
	}
	pair := f.items(bt, seed, g.roster)
	if pair[0].Want.Verdict == pair[1].Want.Verdict {
		return nil, fmt.Errorf("%s seed %d: the twin did not flip the answer (%s)", f.id, seed, pair[0].Want.Verdict)
	}
	return pair, nil
}

// fresh: neither item of the pair repeats one already drawn, and the two
// differ.
func (g *generator) fresh(pair []Item) bool {
	a, b := pair[0].Hash(), pair[1].Hash()
	return g.seen[a] == "" && g.seen[b] == "" && a != b
}

// record remembers the pair's items.
func (g *generator) record(pair []Item) {
	for _, it := range pair {
		g.seen[it.Hash()] = it.ID
	}
}

// GenerateAfter returns the items of seeds from one Generate call over
// prior followed by seeds, so none repeats an item of prior. The items of
// prior are drawn only to be avoided. Generate draws each family's seeds
// in list order, and no two families ask the same question, so the prior
// items drawn here are the ones Generate(live, families, prior) returns. A
// confirmation run generated after the selection seeds therefore cannot
// repeat a selection item.
func GenerateAfter(live []foragers.Forager, families []string, prior, seeds []int64) ([]Item, error) {
	if s, ok := firstSharedSeed(prior, seeds); ok {
		return nil, fmt.Errorf("seed %d is both a seed and a prior seed", s)
	}
	all, err := Generate(live, families, append(append([]int64(nil), prior...), seeds...))
	if err != nil {
		return nil, err
	}
	return itemsOutside(all, seedSet(prior)), nil
}

// itemsOutside is the items whose seed seeds does not hold.
func itemsOutside(items []Item, seeds map[int64]bool) []Item {
	var out []Item
	for _, it := range items {
		if !seeds[it.Seed] {
			out = append(out, it)
		}
	}
	return out
}

// items turns a built pair into its two Items, a and b.
func (f family) items(bt built, seed int64, roster []string) []Item {
	q := preamble + bt.question + closing
	pair := fmt.Sprintf("%s-s%d", f.id, seed)
	out := make([]Item, 0, 2)
	for _, v := range []struct {
		name   string
		roster []foragers.Forager
	}{{"a", bt.a}, {"b", bt.b}} {
		out = append(out, Item{
			ID: pair + "-" + v.name, Pair: pair, Family: f.id, Seed: seed, Variant: v.name,
			Question: q, Context: Pack(v.roster), Tokens: f.tokens, Want: bt.answer(v.roster), Names: roster,
		})
	}
	return out
}

// selectFamilies is the families ids names, by ID in any case or by slug,
// in the order named; every family when ids is empty.
func selectFamilies(ids []string) ([]family, error) {
	if len(ids) == 0 {
		return registry, nil
	}
	var out []family
	for _, id := range ids {
		f, ok := familyByID(id)
		if !ok {
			return nil, fmt.Errorf("unknown family %q (have %s)", id, strings.Join(Families(), ", "))
		}
		out = append(out, f)
	}
	return out, nil
}

// familyByID finds a family by its ID in any case, or by its slug.
func familyByID(id string) (family, bool) {
	for _, f := range registry {
		if strings.EqualFold(f.id, id) || f.slug == id {
			return f, true
		}
	}
	return family{}, false
}

// salt seeds a family's generator apart from every other family's.
func salt(id string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return h.Sum64()
}

// verdict is Support when yes, else Oppose.
func verdict(yes bool) string {
	if yes {
		return Support
	}
	return Oppose
}

// pick draws one of xs.
func pick[T any](rng *rand.Rand, xs []T) T { return xs[rng.IntN(len(xs))] }

// someButNotAll draws a proper subset of xs, possibly empty, in a random
// order: rng.Perm, then rng.IntN for its size. It draws nothing when xs
// holds fewer than two.
func someButNotAll[T any](rng *rand.Rand, xs []T) []T {
	if len(xs) < 2 {
		return nil
	}
	perm := rng.Perm(len(xs))
	var out []T
	for _, i := range perm[:rng.IntN(len(xs))] {
		out = append(out, xs[i])
	}
	return out
}

// edge is a candidate bond a transform may add.
type edge struct{ from, to, kind string }

// addOneOf adds one bond chosen from the first non-empty option picked at
// random; options with no candidates are skipped. It reports whether it
// added anything.
func addOneOf(r []foragers.Forager, rng *rand.Rand, options ...[]edge) bool {
	var live [][]edge
	for _, o := range options {
		if len(o) > 0 {
			live = append(live, o)
		}
	}
	if len(live) == 0 {
		return false
	}
	e := pick(rng, pick(rng, live))
	addBond(r, e.from, e.to, e.kind)
	return true
}

// edges lists every from→to between the two name sets, of the given kind,
// that from does not already declare (of any kind) and that is not a self-bond.
func edges(r []foragers.Forager, from, to []string, kind string) []edge {
	var out []edge
	for _, a := range from {
		out = append(out, newEdges(find(r, a), to, kind)...)
	}
	return out
}

// newEdges lists f→b of kind for every b in to that is not f itself and
// that f declares no bond to.
func newEdges(f *foragers.Forager, to []string, kind string) []edge {
	var out []edge
	for _, b := range to {
		if b != f.Name && !hasBond(f, b, "") {
			out = append(out, edge{f.Name, b, kind})
		}
	}
	return out
}

// split partitions the roster's names into preset members and the rest.
func split(r []foragers.Forager, in map[string]bool) (members, others []string) {
	for _, n := range names(r) {
		if in[n] {
			members = append(members, n)
		} else {
			others = append(others, n)
		}
	}
	return members, others
}

// ── F1 F2 F3: orphaned resonates pairs ─────────────────────────────────

// orphanPairs lists r's orphaned resonates pairs under the preset in, as
// a⇄b tokens, sorted.
func orphanPairs(r []foragers.Forager, in map[string]bool) []string {
	set := map[string]bool{}
	for _, f := range r {
		addOrphanedPairs(set, f, in)
	}
	return orderedKeys(set)
}

// addOrphanedPairs adds each resonates bond f declares with exactly one end
// in the preset.
func addOrphanedPairs(set map[string]bool, f foragers.Forager, in map[string]bool) {
	for _, b := range f.Bonds {
		if b.Kind == foragers.BondResonates && in[f.Name] != in[b.To] {
			set[pairToken(f.Name, b.To)] = true
		}
	}
}

// pairToken writes a pair as a⇄b, its names in alphabetical order.
func pairToken(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "⇄" + b
}

// removeResonatesPairs drops the resonates bonds of each a⇄b pair.
func removeResonatesPairs(r []foragers.Forager, pairs []string) {
	for _, pair := range pairs {
		a, c, _ := strings.Cut(pair, "⇄")
		removeBonds(r, a, c, foragers.BondResonates)
	}
}

// orphanFamily is F1, F2 or F3: whether a preset leaves an orphaned
// resonates pair. rule gives a roster's preset members and how the
// question states them.
func orphanFamily(id, slug, preset string, rule func([]foragers.Forager) (string, []foragers.Forager)) family {
	o := orphanBuilder{preset: preset, rule: rule}
	return family{id: id, slug: slug, tokens: TokensPair, build: o.build}
}

// orphanBuilder builds the twin pairs of one orphan family.
type orphanBuilder struct {
	preset string
	rule   func([]foragers.Forager) (string, []foragers.Forager)
}

// answer is the orphaned pairs of the roster's preset.
func (o orphanBuilder) answer(x []foragers.Forager) Answer {
	_, mx := o.rule(x)
	p := orphanPairs(x, memberSet(mx))
	return Answer{Verdict: verdict(len(p) > 0), Tokens: p}
}

func (o orphanBuilder) build(r []foragers.Forager, rng *rand.Rand) (built, error) {
	text, m := o.rule(r)
	in := memberSet(m)
	members, others := split(r, in)

	// Keep the answer, vary the item: drop some orphaned pairs but not
	// all, then add one bond that orphans nothing — resonates inside
	// the preset, resonates outside it, or a cites bond across it.
	removeResonatesPairs(r, someButNotAll(rng, orphanPairs(r, in)))
	addOneOf(r, rng,
		resonatesWithin(r, members),
		resonatesWithin(r, others),
		append(edges(r, members, others, foragers.BondCites), edges(r, others, members, foragers.BondCites)...),
	)

	b, err := o.flip(cloneRoster(r), rng, in, members, others)
	if err != nil {
		return built{}, err
	}
	q := text + orphanRule + fmt.Sprintf("Does the %s preset leave at least one orphaned resonates pair? Emit support if at least one does and oppose if none does.", o.preset) + tokenAsk[TokensPair]
	return built{question: q, a: r, b: b, answer: o.answer}, nil
}

// flip flips b's answer: it removes every orphaned pair when there is one,
// and otherwise orphans one between a member and a non-member.
func (o orphanBuilder) flip(b []foragers.Forager, rng *rand.Rand, in map[string]bool, members, others []string) ([]foragers.Forager, error) {
	if p := orphanPairs(b, in); len(p) > 0 {
		removeResonatesPairs(b, p)
		return b, nil
	}
	cand := append(resonatesAcross(b, members, others), resonatesAcross(b, others, members)...)
	if len(cand) == 0 {
		return nil, fmt.Errorf("no member and non-member of %s to orphan a pair between", o.preset)
	}
	e := pick(rng, cand)
	addBond(b, e.from, e.to, e.kind)
	return b, nil
}

// resonatesWithin lists new resonates bonds between two names of one set,
// skipping pairs already bonded by resonates in either direction.
func resonatesWithin(r []foragers.Forager, set []string) []edge {
	return resonatesAcross(r, set, set)
}

// resonatesAcross lists new resonates bonds from a name of from to a name
// of to, skipping pairs already bonded by resonates in either direction.
func resonatesAcross(r []foragers.Forager, from, to []string) []edge {
	var out []edge
	for _, e := range edges(r, from, to, foragers.BondResonates) {
		if !hasBond(find(r, e.to), e.from, foragers.BondResonates) {
			out = append(out, e)
		}
	}
	return out
}

// ── F4: does any deliberation-eligible forager own WASP-I ──────────────

var waspAxes = []string{"k", "k_execution", "E", "T", "F"}

// waspIOwners lists the deliberation-eligible foragers that declare wasp: I,
// as @name tokens, sorted.
func waspIOwners(r []foragers.Forager) []string {
	var out []string
	for _, f := range eligible(r) {
		if f.Coverage.Wasp == "I" {
			out = append(out, "@"+f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// waspIAnswer is F4's answer: the WASP-I owners.
func waspIAnswer(x []foragers.Forager) Answer {
	o := waspIOwners(x)
	return Answer{Verdict: verdict(len(o) > 0), Tokens: o}
}

func buildWaspI(r []foragers.Forager, rng *rand.Rand) (built, error) {
	elig := names(eligible(r))
	if len(elig) == 0 {
		return built{}, fmt.Errorf("no deliberation-eligible forager")
	}
	_, ineligible := split(r, memberSet(eligible(r)))

	// Keep the answer, vary the item: clear some owners but not all, move
	// one non-owner to another WASP axis, and half the time give an
	// ineligible forager wasp: I, which does not count.
	clearWasp(r, someButNotAll(rng, waspIOwners(r)))
	moveOneWaspAxis(r, rng, elig)
	maybeGiveIneligibleWaspI(r, rng, ineligible)

	b := cloneRoster(r)
	flipWaspI(b, rng, elig)
	q := "Does any deliberation-eligible forager declare wasp: I under coverage? Emit support if at least one does and oppose if none does." + tokenAsk[TokensName]
	return built{question: q, a: r, b: b, answer: waspIAnswer}, nil
}

// clearWasp clears the WASP axis of each @name forager.
func clearWasp(r []foragers.Forager, tokens []string) {
	for _, n := range tokens {
		find(r, strings.TrimPrefix(n, "@")).Coverage.Wasp = ""
	}
}

// moveOneWaspAxis moves one eligible forager that does not own WASP-I to
// another WASP axis.
func moveOneWaspAxis(r []foragers.Forager, rng *rand.Rand, elig []string) {
	var nonOwners []string
	for _, n := range elig {
		if find(r, n).Coverage.Wasp != "I" {
			nonOwners = append(nonOwners, n)
		}
	}
	if len(nonOwners) > 0 {
		f := find(r, pick(rng, nonOwners))
		f.Coverage.Wasp = pick(rng, without(waspAxes, f.Coverage.Wasp))
	}
}

// maybeGiveIneligibleWaspI gives one ineligible forager wasp: I half the
// time; it draws nothing when there is no ineligible forager.
func maybeGiveIneligibleWaspI(r []foragers.Forager, rng *rand.Rand, ineligible []string) {
	if len(ineligible) > 0 && rng.IntN(2) == 0 {
		find(r, pick(rng, ineligible)).Coverage.Wasp = "I"
	}
}

// flipWaspI clears every WASP-I owner of b, or gives one eligible forager
// wasp: I when there is none.
func flipWaspI(b []foragers.Forager, rng *rand.Rand, elig []string) {
	if o := waspIOwners(b); len(o) > 0 {
		clearWasp(b, o)
		return
	}
	find(b, pick(rng, elig)).Coverage.Wasp = "I"
}

// without is xs less every drop.
func without(xs []string, drop string) []string {
	var out []string
	for _, x := range xs {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}

// ── F6: a contradicts bond inside minimal ──────────────────────────────

// inPresetContradicts lists the contradicts bonds a minimal forager
// declares to another minimal forager, as from→to tokens, sorted.
func inPresetContradicts(r []foragers.Forager) []string {
	in := memberSet(foragers.Minimal(r))
	set := map[string]bool{}
	for _, f := range r {
		addContradictsWithin(set, f, in)
	}
	return orderedKeys(set)
}

// addContradictsWithin adds the contradicts bonds f declares to another
// member of the preset in, when f is a member itself.
func addContradictsWithin(set map[string]bool, f foragers.Forager, in map[string]bool) {
	if !in[f.Name] {
		return
	}
	for _, b := range f.Bonds {
		if contradictsMember(b, f.Name, in) {
			set[f.Name+"→"+b.To] = true
		}
	}
}

// contradictsMember: b, declared by from, contradicts another member of in.
func contradictsMember(b foragers.Bond, from string, in map[string]bool) bool {
	return b.Kind == foragers.BondContradicts && in[b.To] && b.To != from
}

// contradictsAnswer is F6's answer: the in-preset contradicts bonds.
func contradictsAnswer(x []foragers.Forager) Answer {
	a := inPresetContradicts(x)
	return Answer{Verdict: verdict(len(a) > 0), Tokens: a}
}

func buildContradicts(r []foragers.Forager, rng *rand.Rand) (built, error) {
	members, others := split(r, memberSet(foragers.Minimal(r)))
	if len(members) < 2 {
		return built{}, fmt.Errorf("minimal holds %d forager(s); a contradicts bond inside it needs two", len(members))
	}

	// Keep the answer, vary the item: drop some in-preset contradicts but
	// not all, then add one bond that does not count — contradicts across
	// the preset boundary either way, or cites inside it.
	dropContradicts(r, someButNotAll(rng, inPresetContradicts(r)))
	addOneOf(r, rng,
		edges(r, members, others, foragers.BondContradicts),
		edges(r, others, members, foragers.BondContradicts),
		edges(r, members, members, foragers.BondCites),
	)

	b := cloneRoster(r)
	if err := flipContradicts(b, rng, members); err != nil {
		return built{}, err
	}
	q := minimalRule + "Does any forager in the minimal preset declare a contradicts bond to another forager in the minimal preset? Emit support if at least one does and oppose if none does." + tokenAsk[TokensArrow]
	return built{question: q, a: r, b: b, answer: contradictsAnswer}, nil
}

// dropContradicts removes the contradicts bond of each from→to arrow.
func dropContradicts(r []foragers.Forager, arrows []string) {
	for _, arrow := range arrows {
		from, to, _ := strings.Cut(arrow, "→")
		dropDeclared(r, from, to, foragers.BondContradicts)
	}
}

// flipContradicts removes every in-preset contradicts bond of b, or adds
// one between two members when there is none.
func flipContradicts(b []foragers.Forager, rng *rand.Rand, members []string) error {
	if a := inPresetContradicts(b); len(a) > 0 {
		dropContradicts(b, a)
		return nil
	}
	cand := edges(b, members, members, foragers.BondContradicts)
	if len(cand) == 0 {
		return fmt.Errorf("every minimal forager already declares a bond to every other")
	}
	e := pick(rng, cand)
	addBond(b, e.from, e.to, e.kind)
	return nil
}

// dropDeclared removes the bonds of kind that from declares on to.
func dropDeclared(r []foragers.Forager, from, to, kind string) {
	dropBondsTo(find(r, from), to, kind)
}

// ── F8: every minimal forager declares an MSS axis ─────────────────────

var (
	mssAxes = []string{"def", "gua", "asm", "unk"}
	cdeAxes = []string{"detect", "decompose", "execute"}
)

// minimalWithoutMSS lists the minimal foragers with no MSS axis, as @name
// tokens, sorted.
func minimalWithoutMSS(r []foragers.Forager) []string {
	var out []string
	for _, f := range foragers.Minimal(r) {
		if f.Coverage.Mss == "" {
			out = append(out, "@"+f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// mssAnswer is F8's answer: the minimal foragers without an MSS axis.
func mssAnswer(x []foragers.Forager) Answer {
	l := minimalWithoutMSS(x)
	return Answer{Verdict: verdict(len(l) == 0), Tokens: l}
}

func buildMSS(r []foragers.Forager, rng *rand.Rand) (built, error) {
	elig := names(eligible(r))
	if len(elig) == 0 {
		return built{}, fmt.Errorf("no deliberation-eligible forager")
	}
	_, ineligible := split(r, memberSet(eligible(r)))

	// Keep the answer, vary the item: give some lacking foragers an MSS
	// axis but not all, move one owner to another MSS axis, and give an
	// ineligible forager a CDE axis with no MSS, which does not count.
	giveMSSAxes(r, rng, someButNotAll(rng, minimalWithoutMSS(r)))
	moveOneMSSAxis(r, rng)
	moveOneBareCDEAxis(r, rng, ineligible)

	b := cloneRoster(r)
	flipMSS(b, rng, elig)
	q := minimalRule + "Does every forager in the minimal preset declare an mss coverage axis? Emit support if every one does and oppose if at least one does not." +
		" Name each forager in the minimal preset without one as `@name`, and write no other forager in that form."
	return built{question: q, a: r, b: b, answer: mssAnswer}, nil
}

// giveMSSAxes gives each @name forager an MSS axis drawn at random.
func giveMSSAxes(r []foragers.Forager, rng *rand.Rand, tokens []string) {
	for _, n := range tokens {
		find(r, strings.TrimPrefix(n, "@")).Coverage.Mss = pick(rng, mssAxes)
	}
}

// moveOneMSSAxis moves one minimal forager that has an MSS axis to
// another.
func moveOneMSSAxis(r []foragers.Forager, rng *rand.Rand) {
	var owners []string
	for _, f := range foragers.Minimal(r) {
		if f.Coverage.Mss != "" {
			owners = append(owners, f.Name)
		}
	}
	if len(owners) > 0 {
		f := find(r, pick(rng, owners))
		f.Coverage.Mss = pick(rng, without(mssAxes, f.Coverage.Mss))
	}
}

// moveOneBareCDEAxis moves one ineligible forager with no MSS axis to
// another CDE axis.
func moveOneBareCDEAxis(r []foragers.Forager, rng *rand.Rand, ineligible []string) {
	var bare []string
	for _, n := range ineligible {
		if find(r, n).Coverage.Mss == "" {
			bare = append(bare, n)
		}
	}
	if len(bare) > 0 {
		f := find(r, pick(rng, bare))
		f.Coverage.Cde = pick(rng, without(cdeAxes, f.Coverage.Cde))
	}
}

// flipMSS gives every minimal forager without an MSS axis one, or takes
// the axis from one when none lacks it.
func flipMSS(b []foragers.Forager, rng *rand.Rand, elig []string) {
	if l := minimalWithoutMSS(b); len(l) > 0 {
		giveMSSAxes(b, rng, l)
		return
	}
	takeMSSAxis(b, rng, elig)
}

// takeMSSAxis takes the MSS axis from one forager in minimal — or from any
// eligible forager when minimal is empty — and makes sure it keeps another
// axis, so it stays in minimal without one.
func takeMSSAxis(b []foragers.Forager, rng *rand.Rand, elig []string) {
	pool := names(foragers.Minimal(b))
	if len(pool) == 0 {
		pool = elig
	}
	f := find(b, pick(rng, pool))
	f.Coverage.Mss = ""
	if f.Coverage.Wasp == "" && f.Coverage.Cde == "" {
		f.Coverage.Cde = pick(rng, cdeAxes)
	}
}

// ── AB: the evidence is missing from the pack ──────────────────────────

func buildMissing(r []foragers.Forager, rng *rand.Rand) (built, error) {
	all := names(r)
	declared, undeclared := resonatesOrNot(r, all)
	pool := missingPool(rng, declared, undeclared)
	if len(pool) == 0 {
		return built{}, fmt.Errorf("the roster has fewer than two foragers")
	}
	q := pick(rng, pool)
	// Vary the rest of the roster with one resonates bond that touches
	// neither forager the question names, so repeated questions still
	// differ in their pack.
	addOneOf(r, rng, resonatesWithin(r, without(without(all, q.from), q.to)))

	text := fmt.Sprintf("Does the roster entry for %s list a resonates bond to %s? Emit support if it does and oppose if it does not.", q.from, q.to)
	return built{question: text, a: r, b: withoutEntry(cloneRoster(r), q.from), answer: q.resonatesAnswer}, nil
}

// resonatesOrNot splits every ordered pair of distinct names into those
// whose first declares a resonates bond to the second, and the rest.
func resonatesOrNot(r []foragers.Forager, all []string) (declared, undeclared []edge) {
	for _, x := range all {
		d, u := resonatesFrom(find(r, x), all)
		declared, undeclared = append(declared, d...), append(undeclared, u...)
	}
	return declared, undeclared
}

// resonatesFrom splits the other names into those f declares a resonates
// bond to, and the rest.
func resonatesFrom(f *foragers.Forager, all []string) (declared, undeclared []edge) {
	for _, y := range all {
		if f.Name == y {
			continue
		}
		if hasBond(f, y, foragers.BondResonates) {
			declared = append(declared, edge{f.Name, y, foragers.BondResonates})
		} else {
			undeclared = append(undeclared, edge{f.Name, y, foragers.BondResonates})
		}
	}
	return declared, undeclared
}

// missingPool is the bonds AB asks about: the declared ones half the time
// when there are any, and always when every pair is declared; else the
// undeclared ones. It draws from rng whatever the lists hold.
func missingPool(rng *rand.Rand, declared, undeclared []edge) []edge {
	if rng.IntN(2) == 0 && len(declared) > 0 || len(undeclared) == 0 {
		return declared
	}
	return undeclared
}

// resonatesAnswer is AB's answer to the question about q: abstain when the
// roster has no entry for q.from, else whether that entry lists a
// resonates bond to q.to.
func (q edge) resonatesAnswer(x []foragers.Forager) Answer {
	f := find(x, q.from)
	if f == nil {
		return Answer{Verdict: Abstain}
	}
	return Answer{Verdict: verdict(hasBond(f, q.to, foragers.BondResonates))}
}

// withoutEntry is the roster less the entry named name.
func withoutEntry(r []foragers.Forager, name string) []foragers.Forager {
	var out []foragers.Forager
	for _, f := range r {
		if f.Name != name {
			out = append(out, f)
		}
	}
	return out
}
