package bench

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is the decision rule, fixed before the first run. Its defaults are
// the plan's pre-registered values; a rule file overrides any of them.
type Rule struct {
	Reference string `yaml:"reference" json:"reference"`
	Negative  string `yaml:"negative" json:"negative"`
	// Delta is the non-inferiority margin; Alpha the one-sided error rate.
	Delta float64 `yaml:"delta" json:"delta"`
	Alpha float64 `yaml:"alpha" json:"alpha"`
	// CompletionLowerBound is the floor on the Wilson 95% lower bound of
	// the share of runs that completed.
	CompletionLowerBound float64 `yaml:"completion_lower_bound" json:"completion_lower_bound"`
	ClassFloor           float64 `yaml:"class_floor" json:"class_floor"`
	FullVerdictMin       float64 `yaml:"full_verdict_min" json:"full_verdict_min"`
	SchemaValidEnforced  float64 `yaml:"schema_valid_enforced" json:"schema_valid_enforced"`
	SchemaValidRepaired  float64 `yaml:"schema_valid_repaired" json:"schema_valid_repaired"`
	AbstainMargin        float64 `yaml:"abstain_margin" json:"abstain_margin"`
	WallTie              float64 `yaml:"wall_tie" json:"wall_tie"`
	SelectionSeeds       []int64 `yaml:"selection_seeds" json:"selection_seeds"`
	ConfirmationSeeds    []int64 `yaml:"confirmation_seeds" json:"confirmation_seeds"`
	// Configs are the configurations a machine class may choose from.
	Configs map[string]ConfigSpec `yaml:"configs" json:"configs,omitempty"`
	Classes []MachineClass        `yaml:"classes" json:"classes,omitempty"`
}

// ConfigSpec is what the rule knows about a configuration that the runs
// do not measure: the memory its models need, and whether its outputs are
// schema-enforced during generation.
type ConfigSpec struct {
	MemoryGB float64 `yaml:"memory_gb" json:"memory_gb"`
	Enforced bool    `yaml:"enforced" json:"enforced"`
}

// MachineClass is a memory budget. Zero means no limit.
type MachineClass struct {
	Name     string  `yaml:"name" json:"name"`
	MemoryGB float64 `yaml:"memory_gb" json:"memory_gb"`
}

// DefaultRule returns the pre-registered values with no configurations named.
func DefaultRule() Rule {
	return Rule{
		Delta: 0.15, Alpha: 0.05,
		CompletionLowerBound: 0.85, ClassFloor: 0.70, FullVerdictMin: 0.90,
		SchemaValidEnforced: 1.0, SchemaValidRepaired: 0.95,
		AbstainMargin: 0.10, WallTie: 0.10,
		SelectionSeeds:    []int64{1, 2, 3, 4},
		ConfirmationSeeds: []int64{101, 102, 103, 104},
	}
}

// LoadRule reads a rule file over the defaults and validates it.
func LoadRule(path string) (Rule, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Rule{}, err
	}
	r := DefaultRule()
	if err := yaml.Unmarshal(raw, &r); err != nil {
		return Rule{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return r, r.validate()
}

// validate refuses a rule that cannot be applied.
func (r Rule) validate() error {
	if err := r.validateControls(); err != nil {
		return err
	}
	if err := r.validateLevels(); err != nil {
		return err
	}
	return r.validateSeeds()
}

// validateControls refuses a rule that does not name a reference and a
// negative control, or that names one configuration as both.
func (r Rule) validateControls() error {
	if r.Reference == "" || r.Negative == "" {
		return fmt.Errorf("the rule must name a reference and a negative control")
	}
	if r.Reference == r.Negative {
		return fmt.Errorf("the reference and the negative control are both %q", r.Reference)
	}
	return nil
}

// validateLevels refuses a delta or an alpha outside (0, 0.5).
func (r Rule) validateLevels() error {
	if outsideOpenHalf(r.Delta) {
		return fmt.Errorf("delta must lie in (0, 0.5)")
	}
	if outsideOpenHalf(r.Alpha) {
		return fmt.Errorf("alpha must lie in (0, 0.5)")
	}
	return nil
}

// outsideOpenHalf: x is at most 0 or at least 0.5.
func outsideOpenHalf(x float64) bool { return x <= 0 || x >= 0.5 }

// validateSeeds refuses a rule without both seed lists, or with a seed on
// both.
func (r Rule) validateSeeds() error {
	if len(r.SelectionSeeds) == 0 || len(r.ConfirmationSeeds) == 0 {
		return fmt.Errorf("the rule needs selection and confirmation seeds")
	}
	if s, ok := firstSharedSeed(r.SelectionSeeds, r.ConfirmationSeeds); ok {
		return fmt.Errorf("seed %d is both a selection and a confirmation seed", s)
	}
	return nil
}

// Statuses of a guard or a margin.
const (
	Pass         = "pass"
	Fail         = "fail"
	Unmeasured   = "unmeasured"
	Inconclusive = "inconclusive"
)

// Outcomes of a configuration.
const (
	OutcomeAccept       = "accept"        // passed selection and confirmation; chosen for a class
	OutcomePending      = "pending"       // chosen for a class; its confirmation runs are missing
	OutcomePassed       = "passed"        // passed selection; not chosen for any class
	OutcomeRejectGuard  = "reject-guard"  // a hard guard failed
	OutcomeRejectMargin = "reject-margin" // worse than the reference by more than delta
	OutcomeInconclusive = "inconclusive"  // the data cannot decide, or confirmation failed
	OutcomeOneCall      = "one-call"      // non-inferior, but the swarm did not beat one call of its model
	OutcomeRejected     = "rejected"      // the negative control, as it must be
	OutcomeNotRejected  = "not-rejected"  // the negative control passed: the run is void
	OutcomeReference    = "reference"     // the reference, valid
	OutcomeInvalid      = "invalid"       // the reference failed a guard: the run is void
)

// Check is one hard guard.
type Check struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Detail    string  `json:"detail,omitempty"`
}

// Margin is a paired non-inferiority test against the reference, over the
// twin pairs whose twins expect support and oppose.
type Margin struct {
	Metric string  `json:"metric"`
	Pairs  int     `json:"pairs"`
	Diff   float64 `json:"diff"`
	Lower  float64 `json:"lower"`
	Upper  float64 `json:"upper"`
	Status string  `json:"status"`
	Detail string  `json:"detail,omitempty"`
}

// Stage is the guards and margins on one split of the seeds.
type Stage struct {
	Guards  []Check  `json:"guards"`
	Margins []Margin `json:"margins"`
}

// ConfigResult is one configuration's evaluation.
type ConfigResult struct {
	Config       string   `json:"config"`
	Role         string   `json:"role"`
	Outcome      string   `json:"outcome"`
	Reasons      []string `json:"reasons,omitempty"`
	Selection    Stage    `json:"selection"`
	Confirmation *Stage   `json:"confirmation,omitempty"`
	SwarmCBA     *float64 `json:"swarm_class_balanced,omitempty"`
	SoloCBA      *float64 `json:"solo_class_balanced,omitempty"`
	Lift         *float64 `json:"lift,omitempty"`
	MedianWall   float64  `json:"median_wall_seconds"`
	MemoryGB     float64  `json:"memory_gb,omitempty"`
}

// Choice is the configuration a machine class gets.
type Choice struct {
	Class   string   `json:"class"`
	Config  string   `json:"config,omitempty"`
	Outcome string   `json:"outcome"` // accept | pending | none
	Tried   []string `json:"tried,omitempty"`
}

// Decision is the rule applied to a set of rows.
type Decision struct {
	// Verdict: decided, inconclusive (the reference's guards could not be
	// measured) or void (the benchmark failed its own validity check).
	Verdict string         `json:"verdict"`
	Reasons []string       `json:"reasons,omitempty"`
	Configs []ConfigResult `json:"configs"`
	Choices []Choice       `json:"choices,omitempty"`
	// Ignored counts rows whose seed is on neither list.
	Ignored int `json:"ignored_rows,omitempty"`
	// Overlap lists items that appear under both seed lists.
	Overlap []string `json:"overlap,omitempty"`
}

// Decide applies the rule. In order:
//
//  1. Validity. The reference must pass every guard and the negative
//     control must be rejected (a class floor, abstain fabrication or the
//     accuracy margin fails), else the run is void. Confirmation items must
//     not repeat selection items.
//  2. Guards, per configuration, on its swarm runs.
//  3. Non-inferiority against the reference, paired by item over the
//     support/oppose twin pairs, on class-balanced accuracy and on token F1.
//  4. HIVE lift: swarm minus solo class-balanced accuracy. At or below
//     zero the configuration gets one call, not a swarm.
//  5. Per machine class, the passing configuration that fits with the
//     lowest median wall-clock; within WallTie of it, the least memory.
//  6. Confirmation of that choice on the confirmation seeds; a failure is
//     inconclusive and the next configuration is tried.
func Decide(rows []Row, rule Rule) Decision {
	d := Decision{Verdict: "decided"}
	sel, conf := d.splitBySeed(rows, rule)
	refRan := len(arm(sel[rule.Reference], ArmSwarm)) > 0
	negRan := len(arm(sel[rule.Negative], ArmSwarm)) > 0
	d.checkRuns(rule, refRan, negRan)
	order := orderedKeys(configSet(sel, conf))
	results := make(map[string]*ConfigResult, len(order))
	for _, name := range order {
		results[name] = selectionResult(name, sel, rule, refRan)
	}
	d.judgeControls(rule, results, refRan, negRan)
	judgeCandidates(order, results, rule, refRan)
	if d.Verdict == "decided" {
		d.Choices = choose(rule, results, conf)
	}
	for _, name := range order {
		d.Configs = append(d.Configs, *results[name])
	}
	return d
}

// splitBySeed sorts rows into selection and confirmation rows, by
// configuration, counts in d.Ignored the rows whose seed is on neither list,
// and lists in d.Overlap the items both lists hold.
func (d *Decision) splitBySeed(rows []Row, rule Rule) (sel, conf map[string][]Row) {
	selSeeds, confSeeds := seedSet(rule.SelectionSeeds), seedSet(rule.ConfirmationSeeds)
	sel, conf = map[string][]Row{}, map[string][]Row{}
	selItems, confItems := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		switch {
		case selSeeds[r.Seed]:
			sel[r.Config] = append(sel[r.Config], r)
			selItems[r.ItemHash] = true
		case confSeeds[r.Seed]:
			conf[r.Config] = append(conf[r.Config], r)
			confItems[r.ItemHash] = true
		default:
			d.Ignored++
		}
	}
	d.Overlap = sharedItems(selItems, confItems)
	return sel, conf
}

// sharedItems is the items of conf that sel also holds, sorted; nil when
// there is none.
func sharedItems(sel, conf map[string]bool) []string {
	var out []string
	for h := range conf {
		if sel[h] {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// void makes the decision void, for reason.
func (d *Decision) void(reason string) {
	d.Verdict = "void"
	d.Reasons = append(d.Reasons, reason)
}

// checkRuns voids the decision when confirmation items repeat selection
// items, or when the reference or the negative control has no selection
// swarm runs.
func (d *Decision) checkRuns(rule Rule, refRan, negRan bool) {
	if len(d.Overlap) > 0 {
		d.void(fmt.Sprintf("%d confirmation item(s) repeat selection items; generate the confirmation case with exclude_seeds: [%s]",
			len(d.Overlap), joinSeeds(rule.SelectionSeeds)))
	}
	if !refRan {
		d.void(fmt.Sprintf("the reference %q has no selection swarm runs", rule.Reference))
	}
	if !negRan {
		d.void(fmt.Sprintf("the negative control %q has no selection swarm runs", rule.Negative))
	}
}

// configSet is every configuration with selection or confirmation rows.
func configSet(sel, conf map[string][]Row) map[string]bool {
	names := map[string]bool{}
	for n := range sel {
		names[n] = true
	}
	for n := range conf {
		names[n] = true
	}
	return names
}

// selectionResult is one configuration's role, its selection stage against
// the reference when the reference ran, and its arms' class-balanced
// accuracies, lift and median swarm wall-clock.
func selectionResult(name string, sel map[string][]Row, rule Rule, refRan bool) *ConfigResult {
	res := &ConfigResult{Config: name, Role: configRole(name, rule), MemoryGB: rule.Configs[name].MemoryGB}
	if refRan {
		res.Selection = evaluate(sel[name], sel[rule.Reference], rule, name)
	}
	swarm := arm(sel[name], ArmSwarm)
	res.SwarmCBA, res.SoloCBA = classBalancedOf(swarm), classBalancedOf(arm(sel[name], ArmSolo))
	res.MedianWall = medianWall(swarm)
	res.Lift = diffOf(res.SwarmCBA, res.SoloCBA)
	return res
}

// configRole is the configuration's role under the rule.
func configRole(name string, rule Rule) string {
	switch name {
	case rule.Reference:
		return "reference"
	case rule.Negative:
		return "negative"
	}
	return "candidate"
}

// classBalancedOf is the rows' class-balanced accuracy, nil when there are
// none.
func classBalancedOf(rows []Row) *float64 {
	if len(rows) == 0 {
		return nil
	}
	v := Summarize(rows).ClassBalanced
	return &v
}

// diffOf is *a − *b, nil when either is nil.
func diffOf(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a - *b
	return &v
}

// judgeControls sets the reference's and the negative control's outcomes
// when the reference ran. The decision is void when the reference failed a
// guard or the negative control was not rejected, and otherwise
// inconclusive when a guard of the reference could not be measured.
func (d *Decision) judgeControls(rule Rule, results map[string]*ConfigResult, refRan, negRan bool) {
	if !refRan {
		return
	}
	unmeasured := d.judgeReference(results[rule.Reference])
	if negRan {
		d.judgeNegative(results[rule.Negative])
	}
	d.inconclusiveOn(unmeasured)
}

// judgeReference sets the reference's outcome, voiding the decision when it
// failed a guard, and returns the guards it could not measure.
func (d *Decision) judgeReference(r *ConfigResult) []string {
	if failed := guardNames(r.Selection, Fail); len(failed) > 0 {
		r.Outcome = OutcomeInvalid
		d.void("the reference failed guard(s): " + strings.Join(failed, ", "))
	} else {
		r.Outcome = OutcomeReference
	}
	return guardNames(r.Selection, Unmeasured)
}

// judgeNegative sets the negative control's outcome, voiding the decision
// when it was not rejected.
func (d *Decision) judgeNegative(n *ConfigResult) {
	if rejected(n.Selection) {
		n.Outcome = OutcomeRejected
		return
	}
	n.Outcome = OutcomeNotRejected
	d.void("the negative control was not rejected: no class floor, abstain fabrication guard or accuracy margin failed")
}

// inconclusiveOn makes a decided decision inconclusive when the reference
// has unmeasured guards.
func (d *Decision) inconclusiveOn(unmeasured []string) {
	if d.Verdict == "decided" && len(unmeasured) > 0 {
		d.Verdict = "inconclusive"
		d.Reasons = append(d.Reasons, "the reference's guard(s) could not be measured: "+strings.Join(unmeasured, ", "))
	}
}

// judgeCandidates sets each candidate's outcome from its selection stage
// and its lift, when the reference ran.
func judgeCandidates(order []string, results map[string]*ConfigResult, rule Rule, refRan bool) {
	if !refRan {
		return
	}
	for _, name := range order {
		res := results[name]
		if !isCandidate(res, rule) {
			continue
		}
		var reasons []string
		res.Outcome, reasons = candidateOutcome(res.Selection, res.Lift)
		res.Reasons = append(res.Reasons, reasons...)
	}
}

// isCandidate: every configuration but the negative control is judged as a
// candidate. The reference is one only when the rule lists it among the
// configurations a class may choose, and it passed its guards.
func isCandidate(res *ConfigResult, rule Rule) bool {
	switch res.Role {
	case "negative":
		return false
	case "reference":
		_, listed := rule.Configs[res.Config]
		return listed && res.Outcome != OutcomeInvalid
	}
	return true
}

// choose picks a configuration for each machine class, or for one class
// with no memory limit when the rule names none.
func choose(rule Rule, results map[string]*ConfigResult, conf map[string][]Row) []Choice {
	classes := rule.Classes
	if len(classes) == 0 {
		classes = []MachineClass{{Name: "any"}}
	}
	var out []Choice
	for _, class := range classes {
		out = append(out, chooseFor(class, rule, results, conf))
	}
	return out
}

// chooseFor tries the configurations that fit class, best first, until one
// is accepted or waits on its confirmation runs.
func chooseFor(class MachineClass, rule Rule, results map[string]*ConfigResult, conf map[string][]Row) Choice {
	cands := classCandidates(class, rule, results)
	ch := Choice{Class: class.Name, Outcome: "none"}
	for len(cands) > 0 {
		best := rank(cands, rule.WallTie)
		ch.Tried = append(ch.Tried, best.Config)
		if outcome := confirm(best, rule, conf); outcome != "" {
			ch.Config, ch.Outcome = best.Config, outcome
			break
		}
		cands = drop(cands, best)
	}
	return ch
}

// classCandidates are the configurations the rule lists, the negative
// control aside, that class may choose.
func classCandidates(class MachineClass, rule Rule, results map[string]*ConfigResult) []*ConfigResult {
	var cands []*ConfigResult
	for name, spec := range rule.Configs {
		if r := results[name]; name != rule.Negative && choosable(r, spec, class) {
			cands = append(cands, r)
		}
	}
	return cands
}

// choosable: the configuration was evaluated, passed selection (or was
// already chosen for a class) and fits the class's memory.
func choosable(r *ConfigResult, spec ConfigSpec, class MachineClass) bool {
	return r != nil && passedSelection(r.Outcome) && !overMemory(spec, class)
}

// passedSelection: the outcome of a configuration that passed selection.
func passedSelection(outcome string) bool {
	return outcome == OutcomePassed || outcome == OutcomeAccept || outcome == OutcomePending
}

// overMemory: the class has a memory limit and the configuration needs more.
func overMemory(spec ConfigSpec, class MachineClass) bool {
	return class.MemoryGB > 0 && spec.MemoryGB > class.MemoryGB
}

// confirm reads best's confirmation stage, evaluating it on first use:
// OutcomePending when its or the reference's confirmation runs are missing,
// OutcomeAccept when it passed, and "" when it failed, which leaves best
// inconclusive.
func confirm(best *ConfigResult, rule Rule, conf map[string][]Row) string {
	if best.Confirmation == nil {
		if !confirmationRan(best.Config, rule, conf) {
			noteOutcome(best, OutcomePending, "confirmation runs missing: run it and the reference on seeds "+joinSeeds(rule.ConfirmationSeeds))
			return OutcomePending
		}
		st := evaluate(conf[best.Config], conf[rule.Reference], rule, best.Config)
		best.Confirmation = &st
	}
	outcome, reasons := stageOutcome(*best.Confirmation)
	if outcome == OutcomePassed {
		best.Outcome = OutcomeAccept
		return OutcomeAccept
	}
	noteOutcome(best, OutcomeInconclusive, "confirmation: "+strings.Join(reasons, "; "))
	return ""
}

// confirmationRan: both the configuration and the reference have
// confirmation swarm runs.
func confirmationRan(config string, rule Rule, conf map[string][]Row) bool {
	return len(arm(conf[config], ArmSwarm)) > 0 && len(arm(conf[rule.Reference], ArmSwarm)) > 0
}

// noteOutcome gives c outcome and records reason, unless c already has
// that outcome.
func noteOutcome(c *ConfigResult, outcome, reason string) {
	if c.Outcome != outcome {
		c.Outcome = outcome
		c.Reasons = append(c.Reasons, reason)
	}
}

// rank picks the fastest configuration by median wall-clock and, among
// those within tie of it, the one needing the least memory.
func rank(cands []*ConfigResult, tie float64) *ConfigResult {
	sorted := append([]*ConfigResult(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool { return fasterThan(sorted[i], sorted[j]) })
	best := sorted[0]
	for _, c := range sorted[1:] {
		if c.MedianWall > sorted[0].MedianWall*(1+tie) {
			break
		}
		if c.MemoryGB < best.MemoryGB {
			best = c
		}
	}
	return best
}

// fasterThan orders configurations by median wall-clock, then by name.
func fasterThan(a, b *ConfigResult) bool {
	if a.MedianWall != b.MedianWall {
		return a.MedianWall < b.MedianWall
	}
	return a.Config < b.Config
}

// drop is cs without c.
func drop(cs []*ConfigResult, c *ConfigResult) []*ConfigResult {
	var out []*ConfigResult
	for _, x := range cs {
		if x != c {
			out = append(out, x)
		}
	}
	return out
}

// candidateOutcome is stageOutcome, then the lift.
func candidateOutcome(st Stage, lift *float64) (string, []string) {
	if o, reasons := stageOutcome(st); o != OutcomePassed {
		return o, reasons
	}
	if lift == nil {
		return OutcomeInconclusive, []string{"no solo control: HIVE lift is unmeasured"}
	}
	if *lift <= 0 {
		return OutcomeOneCall, []string{fmt.Sprintf("HIVE lift %+.3f: the swarm did not beat one call of its model", *lift)}
	}
	return OutcomePassed, nil
}

// stageOutcome reads a stage into an outcome: a failed guard, then a
// failed margin, then anything unmeasured or inconclusive.
func stageOutcome(st Stage) (string, []string) {
	if failed := guardNames(st, Fail); len(failed) > 0 {
		return OutcomeRejectGuard, []string{"guard(s) failed: " + strings.Join(failed, ", ")}
	}
	failedM, openM := marginReasons(st.Margins)
	if len(failedM) > 0 {
		return OutcomeRejectMargin, []string{"worse than the reference by more than delta: " + strings.Join(failedM, "; ")}
	}
	if open := openReasons(st, openM); len(open) > 0 {
		return OutcomeInconclusive, open
	}
	return OutcomePassed, nil
}

// marginReasons describes the margins that failed and those left open,
// inconclusive or unmeasured.
func marginReasons(margins []Margin) (failed, open []string) {
	for _, m := range margins {
		switch m.Status {
		case Fail:
			failed = append(failed, fmt.Sprintf("%s %+.3f, upper bound %+.3f", m.Metric, m.Diff, m.Upper))
		case Inconclusive, Unmeasured:
			open = append(open, fmt.Sprintf("%s %s (%s)", m.Metric, m.Status, marginDetail(m)))
		}
	}
	return failed, open
}

// openReasons is what leaves a stage open: its unmeasured guards, then
// openMargins.
func openReasons(st Stage, openMargins []string) []string {
	var open []string
	if u := guardNames(st, Unmeasured); len(u) > 0 {
		open = append(open, "unmeasured guard(s): "+strings.Join(u, ", "))
	}
	return append(open, openMargins...)
}

// marginDetail is the margin's detail, else its difference and bounds.
func marginDetail(m Margin) string {
	if m.Detail != "" {
		return m.Detail
	}
	return fmt.Sprintf("diff %+.3f, bounds [%+.3f, %+.3f]", m.Diff, m.Lower, m.Upper)
}

// rejected: the negative control fails a check of its answers — a class
// floor or abstain fabrication — or is worse than the reference by more
// than delta on accuracy. A process guard (completion, finish reasons,
// schema, full verdicts, tree) does not count: failing one says nothing
// about whether the items tell a bad model from the reference.
func rejected(st Stage) bool {
	for _, name := range guardNames(st, Fail) {
		if isAnswerGuard(name) {
			return true
		}
	}
	return accuracyMarginFailed(st.Margins)
}

// isAnswerGuard: the guard checks the answers — a class floor or abstain
// fabrication — rather than the process.
func isAnswerGuard(name string) bool {
	return name == GuardSupport || name == GuardOppose || name == GuardFabrication
}

// accuracyMarginFailed: the class-balanced accuracy margin failed.
func accuracyMarginFailed(margins []Margin) bool {
	for _, m := range margins {
		if m.Metric == MetricAccuracy && m.Status == Fail {
			return true
		}
	}
	return false
}

// guardNames lists the stage's guards with status, in order.
func guardNames(st Stage, status string) []string {
	var out []string
	for _, g := range st.Guards {
		if g.Status == status {
			out = append(out, g.Name)
		}
	}
	return out
}

// Guard and margin names.
const (
	GuardCompletion  = "completion"
	GuardFinish      = "no output cut off"
	GuardSchema      = "schema-valid outputs"
	GuardFullVerdict = "queen read full verdicts"
	GuardSupport     = "support accuracy"
	GuardOppose      = "oppose accuracy"
	GuardFabrication = "abstain fabrication"
	GuardTree        = "tree unchanged"

	MetricAccuracy = "class-balanced accuracy"
	MetricTokenF1  = "token F1"
)

// evaluate computes the guards and margins of rows against refRows.
func evaluate(rows, refRows []Row, rule Rule, name string) Stage {
	swarm, refSwarm := arm(rows, ArmSwarm), arm(refRows, ArmSwarm)
	t := tallySwarm(swarm)
	sum, refSum := Summarize(swarm), Summarize(refSwarm)
	st := Stage{Guards: []Check{
		completionGuard(t, rule.CompletionLowerBound),
		finishGuard(t),
		ratioGuard(GuardSchema, t.valid, t.outputs, schemaFloor(rule, name)),
		ratioGuard(GuardFullVerdict, t.full, t.lenses, rule.FullVerdictMin),
		classGuard(GuardSupport, sum.Classes[Support], rule.ClassFloor),
		classGuard(GuardOppose, sum.Classes[Oppose], rule.ClassFloor),
		fabricationGuard(sum, refSum, rule.AbstainMargin),
		treeGuard(t),
	}}
	acc, f1 := pairedDiffs(swarm, refSwarm)
	st.Margins = append(st.Margins, margin(MetricAccuracy, acc, rule.Delta, rule.Alpha), margin(MetricTokenF1, f1, rule.Delta, rule.Alpha))
	return st
}

// swarmTally is what the process guards count over a configuration's
// swarm runs.
type swarmTally struct {
	runs, completed int
	// fin counts calls cut off at the output cap over the runs that
	// recorded finish reasons; finMissing the runs that did not.
	fin, finMissing              int
	valid, outputs, full, lenses int
	treeBad                      int
}

// tallySwarm counts the swarm runs for the process guards.
func tallySwarm(swarm []Row) swarmTally {
	t := swarmTally{runs: len(swarm)}
	for _, r := range swarm {
		t.add(r)
	}
	return t
}

// add counts one swarm run.
func (t *swarmTally) add(r Row) {
	if r.Completed {
		t.completed++
	}
	if r.FinishLength == nil {
		t.finMissing++
	} else {
		t.fin += *r.FinishLength
	}
	t.valid += r.SchemaValid
	t.outputs += r.Outputs
	t.full += r.FullVerdicts
	t.lenses += r.Lenses
	if !r.TreeOK {
		t.treeBad++
	}
}

// completionGuard holds the Wilson 95% lower bound of the share of runs
// that completed to floor.
func completionGuard(t swarmTally, floor float64) Check {
	if t.runs == 0 {
		return Check{Name: GuardCompletion, Status: Unmeasured, Threshold: floor, Detail: "no swarm runs"}
	}
	lb := WilsonLower(t.completed, t.runs, NormalQuantile(0.975))
	return Check{Name: GuardCompletion, Status: status(lb >= floor), Value: lb, Threshold: floor,
		Detail: fmt.Sprintf("%d/%d completed; Wilson 95%% lower bound", t.completed, t.runs)}
}

// finishGuard passes when no call stopped at the output cap; it is
// unmeasured when a run did not record finish reasons.
func finishGuard(t swarmTally) Check {
	switch {
	case t.runs == 0:
		return Check{Name: GuardFinish, Status: Unmeasured, Detail: "no swarm runs"}
	case t.finMissing > 0:
		return Check{Name: GuardFinish, Status: Unmeasured, Detail: fmt.Sprintf("%d of %d runs did not record finish reasons", t.finMissing, t.runs)}
	}
	return Check{Name: GuardFinish, Status: status(t.fin == 0), Value: float64(t.fin), Detail: fmt.Sprintf("%d call(s) stopped at the output cap", t.fin)}
}

// ratioGuard holds num/den to floor, unmeasured when den is 0.
func ratioGuard(name string, num, den int, floor float64) Check {
	if den == 0 {
		return Check{Name: name, Status: Unmeasured, Threshold: floor, Detail: "nothing to measure"}
	}
	v := float64(num) / float64(den)
	return Check{Name: name, Status: status(v >= floor), Value: v, Threshold: floor, Detail: fmt.Sprintf("%d/%d", num, den)}
}

// classGuard holds one class's accuracy to floor.
func classGuard(name string, cs ClassScore, floor float64) Check {
	return ratioGuard(name, cs.Correct, cs.N, floor)
}

// schemaFloor is the schema-valid floor for the configuration: higher when
// its outputs are schema-enforced during generation.
func schemaFloor(rule Rule, name string) float64 {
	if rule.Configs[name].Enforced {
		return rule.SchemaValidEnforced
	}
	return rule.SchemaValidRepaired
}

// fabricationGuard holds the share of abstain items answered support or
// oppose to the reference's share plus abstainMargin.
func fabricationGuard(sum, refSum Summary, abstainMargin float64) Check {
	ab, refAB := sum.Classes[Abstain], refSum.Classes[Abstain]
	if ab.N == 0 || refAB.N == 0 {
		return Check{Name: GuardFabrication, Status: Unmeasured, Detail: "no abstain items"}
	}
	v, limit := float64(sum.Fabricated)/float64(ab.N), float64(refSum.Fabricated)/float64(refAB.N)+abstainMargin
	return Check{Name: GuardFabrication, Status: status(v <= limit), Value: v, Threshold: limit,
		Detail: fmt.Sprintf("%d/%d abstain items answered support or oppose", sum.Fabricated, ab.N)}
}

// treeGuard passes when no run changed its private tree.
func treeGuard(t swarmTally) Check {
	if t.runs == 0 {
		return Check{Name: GuardTree, Status: Unmeasured, Detail: "no swarm runs"}
	}
	return Check{Name: GuardTree, Status: status(t.treeBad == 0), Value: float64(t.treeBad), Detail: fmt.Sprintf("%d run(s) changed their tree", t.treeBad)}
}

// status is Pass when ok, else Fail.
func status(ok bool) string {
	if ok {
		return Pass
	}
	return Fail
}

// twinDiff is one twin pair's difference from the reference, candidate
// minus reference, on its support twin and on its oppose twin.
type twinDiff struct{ support, oppose float64 }

// pairedDiffs pairs rows with the reference's by item and returns, for
// every twin pair whose two twins expect support and oppose and both ran on
// the reference, the differences in mean accuracy and, where both configs
// scored tokens on both twins, in mean token F1. Repetitions are averaged
// first. A pair holding more than one item of either verdict is skipped.
func pairedDiffs(rows, ref []Row) (acc, f1 []twinDiff) {
	c, rf := itemScores(rows), itemScores(ref)
	twins := twinItems(c, rf)
	for _, p := range orderedKeys(twins) {
		s, o, ok := oneTwinEach(twins[p])
		if !ok {
			continue
		}
		cs, co, rs, ro := c[s], c[o], rf[s], rf[o]
		acc = append(acc, twinDiff{cs.correct/cs.n - rs.correct/rs.n, co.correct/co.n - ro.correct/ro.n})
		if scoredTokens(cs, co, rs, ro) {
			f1 = append(f1, twinDiff{cs.f1/cs.f1n - rs.f1/rs.f1n, co.f1/co.f1n - ro.f1/ro.f1n})
		}
	}
	return acc, f1
}

// itemScore is one item's runs: its pair and expected verdict, the runs and
// those answered right, and the runs that scored tokens and their F1 sum.
type itemScore struct {
	pair, want string
	n, correct float64
	f1n, f1    float64
}

// itemScores sums the rows by item.
func itemScores(rows []Row) map[string]*itemScore {
	m := map[string]*itemScore{}
	for _, r := range rows {
		a := m[r.ItemHash]
		if a == nil {
			a = &itemScore{pair: r.Pair, want: r.Want}
			m[r.ItemHash] = a
		}
		a.add(r)
	}
	return m
}

// add counts one run of the item.
func (a *itemScore) add(r Row) {
	a.n++
	if r.Correct {
		a.correct++
	}
	if r.TokenF1 != nil {
		a.f1n++
		a.f1 += *r.TokenF1
	}
}

// twinItems groups the support and oppose items of c that rf also ran, by
// pair: pair → expected verdict → item hashes.
func twinItems(c, rf map[string]*itemScore) map[string]map[string][]string {
	twins := map[string]map[string][]string{}
	for h, a := range c {
		if rf[h] == nil || !binaryVerdict(a.want) {
			continue
		}
		addTwin(twins, a.pair, a.want, h)
	}
	return twins
}

// binaryVerdict: the verdict is support or oppose.
func binaryVerdict(want string) bool { return want == Support || want == Oppose }

// addTwin files item h under its pair and expected verdict.
func addTwin(twins map[string]map[string][]string, pair, want, h string) {
	if twins[pair] == nil {
		twins[pair] = map[string][]string{}
	}
	twins[pair][want] = append(twins[pair][want], h)
}

// oneTwinEach is a pair's support item and oppose item, when it holds
// exactly one of each.
func oneTwinEach(byVerdict map[string][]string) (support, oppose string, ok bool) {
	s, o := byVerdict[Support], byVerdict[Oppose]
	if len(s) != 1 || len(o) != 1 {
		return "", "", false
	}
	return s[0], o[0], true
}

// scoredTokens: every one of the items scored tokens on some run.
func scoredTokens(items ...*itemScore) bool {
	for _, a := range items {
		if a.f1n <= 0 {
			return false
		}
	}
	return true
}

// margin tests non-inferiority over m twin pairs, x their support twins'
// differences and y their oppose twins'. The difference is (mean x +
// mean y)/2, the class-balanced difference on those items, since each
// pair holds one of each. The pair is the unit: SE² = Var(x/2 + y/2)/m,
// so errors a model makes on both twins of a pair are not counted as two
// independent observations. Two floors keep a sample whose differences
// happen to be equal from collapsing the bound onto the mean:
//
//   - each twin's variance is floored at delta(1−delta), its variance when
//     the reference never errs and the candidate errs on that twin at
//     rate delta; the covariance is the pairs' sample correlation (0 when
//     either twin's sample variance is 0) times the two floored standard
//     deviations;
//   - Var(x/2 + y/2) is floored at delta(1−2·delta)/2, the least variance
//     a mean of two −1/0/1 differences can have when its mean is −delta.
//
// The bounds are the difference ∓ t·SE, where t is the 1−alpha quantile of
// Student's t on m−1 degrees of freedom. Pass when the lower bound is above
// −delta, fail when the upper bound is below it, inconclusive otherwise.
func margin(metric string, pairs []twinDiff, delta, alpha float64) Margin {
	m := Margin{Metric: metric, Pairs: len(pairs)}
	if len(pairs) < 2 {
		m.Status, m.Detail = Unmeasured, fmt.Sprintf("%d support/oppose twin pair(s) paired with the reference; the margin needs 2", len(pairs))
		return m
	}
	mx, my := twinMeans(pairs)
	se := pairSE(pairs, mx, my, delta)
	t := studentTQuantile(1-alpha, len(pairs)-1)
	m.Diff = (mx + my) / 2
	m.Lower, m.Upper = m.Diff-t*se, m.Diff+t*se
	m.Status = marginStatus(m.Lower, m.Upper, delta)
	return m
}

// twinMeans is the mean of the pairs' support differences and of their
// oppose differences.
func twinMeans(pairs []twinDiff) (mx, my float64) {
	n := float64(len(pairs))
	for _, p := range pairs {
		mx += p.support
		my += p.oppose
	}
	return mx / n, my / n
}

// twinCovariance is the sample variances of the pairs' support and oppose
// differences about their means mx and my, and their sample covariance.
func twinCovariance(pairs []twinDiff, mx, my float64) (sxx, syy, sxy float64) {
	for _, p := range pairs {
		dx, dy := p.support-mx, p.oppose-my
		sxx += dx * dx
		syy += dy * dy
		sxy += dx * dy
	}
	n := float64(len(pairs))
	return sxx / (n - 1), syy / (n - 1), sxy / (n - 1)
}

// pairSE is the standard error of the pairs' class-balanced difference,
// with the two variance floors margin describes.
func pairSE(pairs []twinDiff, mx, my, delta float64) float64 {
	sxx, syy, sxy := twinCovariance(pairs, mx, my)
	rho := 0.0
	if sxx > 0 && syy > 0 {
		rho = sxy / math.Sqrt(sxx*syy)
	}
	vx, vy := math.Max(sxx, delta*(1-delta)), math.Max(syy, delta*(1-delta))
	pairVar := math.Max((vx+vy+2*rho*math.Sqrt(vx*vy))/4, delta*(1-2*delta)/2)
	return math.Sqrt(pairVar / float64(len(pairs)))
}

// marginStatus passes when the lower bound is above −delta, fails when the
// upper bound is below it, and is inconclusive otherwise.
func marginStatus(lower, upper, delta float64) string {
	switch {
	case lower > -delta:
		return Pass
	case upper < -delta:
		return Fail
	}
	return Inconclusive
}

// WilsonLower is the lower bound of the Wilson score interval for k
// successes in n trials at quantile z.
func WilsonLower(k, n int, z float64) float64 {
	if n == 0 {
		return 0
	}
	p, nf := float64(k)/float64(n), float64(n)
	den := 1 + z*z/nf
	centre := p + z*z/(2*nf)
	spread := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	return (centre - spread) / den
}

// NormalQuantile is the standard normal quantile function.
func NormalQuantile(p float64) float64 {
	return math.Sqrt2 * math.Erfinv(2*p-1)
}

// studentTQuantile is the p quantile of Student's t on df degrees of
// freedom, found by bisection on studentTCDF.
func studentTQuantile(p float64, df int) float64 {
	lo, hi := tBracket(p, df)
	for i := 0; i < 200; i++ {
		mid := (lo + hi) / 2
		if midpointStalled(lo, mid, hi) {
			break
		}
		if studentTCDF(mid, df) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// tBracket doubles [−1, 1] outward until it brackets the p quantile of
// Student's t on df degrees of freedom.
func tBracket(p float64, df int) (lo, hi float64) {
	lo, hi = -1.0, 1.0
	for studentTCDF(lo, df) > p {
		lo *= 2
	}
	for studentTCDF(hi, df) < p {
		hi *= 2
	}
	return lo, hi
}

// midpointStalled: the midpoint equals an end, so bisection can narrow no
// further.
func midpointStalled(lo, mid, hi float64) bool { return mid == lo || mid == hi }

// studentTCDF is P(T ≤ x) for Student's t on df ≥ 1 degrees of freedom, by
// the finite series for integer df (Abramowitz and Stegun 26.7.3, 26.7.4).
func studentTCDF(x float64, df int) float64 {
	theta := math.Atan(x / math.Sqrt(float64(df)))
	s, c := math.Sin(theta), math.Cos(theta)
	var a float64 // P(|T| < |x|), with the sign of x
	if df%2 == 1 {
		sum, term := 0.0, c
		for k := 1; 2*k+1 <= df; k++ {
			sum += term
			term *= c * c * float64(2*k) / float64(2*k+1)
		}
		a = 2 / math.Pi * (theta + s*sum)
	} else {
		sum, term := 0.0, 1.0
		for k := 1; 2*k <= df; k++ {
			sum += term
			term *= c * c * float64(2*k-1) / float64(2*k)
		}
		a = s * sum
	}
	return (1 + a) / 2
}

// arm is the rows run on arm a.
func arm(rows []Row, a string) []Row {
	var out []Row
	for _, r := range rows {
		if r.Arm == a {
			out = append(out, r)
		}
	}
	return out
}

// medianWall is the rows' median wall-clock, 0 when there are none.
func medianWall(rows []Row) float64 {
	if len(rows) == 0 {
		return 0
	}
	w := make([]float64, len(rows))
	for i, r := range rows {
		w[i] = r.WallSeconds
	}
	sort.Float64s(w)
	if len(w)%2 == 1 {
		return w[len(w)/2]
	}
	return (w[len(w)/2-1] + w[len(w)/2]) / 2
}

// seedSet is the seeds as a set.
func seedSet(seeds []int64) map[int64]bool {
	m := make(map[int64]bool, len(seeds))
	for _, s := range seeds {
		m[s] = true
	}
	return m
}

// firstSharedSeed is the first of seeds that among also holds.
func firstSharedSeed(among, seeds []int64) (int64, bool) {
	in := seedSet(among)
	for _, s := range seeds {
		if in[s] {
			return s, true
		}
	}
	return 0, false
}

// joinSeeds writes the seeds comma-separated.
func joinSeeds(seeds []int64) string {
	parts := make([]string, len(seeds))
	for i, s := range seeds {
		parts[i] = fmt.Sprint(s)
	}
	return strings.Join(parts, ", ")
}

// orderedKeys is m's keys in order, empty but not nil when m is empty.
func orderedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
