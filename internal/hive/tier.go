package hive

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// The model-tier rule's clocks and its clear threshold. All three are
// Definitions (chosen). A scan is one recorded `chb hive next --apply` pass.
const (
	// tierChangeEvery (N) is how many scans apart two rises must be: the
	// scan after a rise may not raise the tier again. Two is the stall
	// rule's window: the hive loop stops after two passes that change
	// nothing, so in a running loop some pass between two rises changed the
	// research state. One would allow a rise on every scan.
	tierChangeEvery = 2

	// tierClearScans (M) is how many clear scans in a row lower the tier one
	// step. It exceeds tierChangeEvery, so the tier falls more slowly than
	// it rises. A scan that is not clear restarts the count, and so does a
	// fall.
	tierClearScans = 3

	// tierClearShare is the largest share of the latest wave unknowns may be
	// for a scan to be clear: half the share above which they dominate it
	// (unknownShakingShare). A scan between the two neither raises the tier
	// nor counts toward a fall. One finding is at most a fifth of a wave the
	// signal judges (unknownShakingMinFindings), less than the quarter
	// between the two shares, so relabelling one finding cannot take such a
	// wave from clear to dominated.
	tierClearShare = unknownShakingShare / 2
)

// TierClock is the tier rule's memory between scans: the latest
// hive_tier_log row's.
type TierClock struct {
	// Wait is how many more scans may not raise the tier.
	Wait int
	// Clear is how many clear scans there have been in a row.
	Clear int
}

// next is the clock one scan on: the wait counts down to zero, and a clear
// scan adds to the clear count while any other restarts it.
func (c TierClock) next(clear bool) TierClock {
	n := TierClock{Wait: max(c.Wait-1, 0)}
	if clear {
		n.Clear = c.Clear + 1
	}
	return n
}

// tierPolicy is the range the tier may move in: the ladder, the rung a hive
// starts at, the highest rung the budget mode allows, and what the routing
// profile forbids.
type tierPolicy struct {
	ladder []string
	// start is the rung a new hive starts at, and the lowest a fall reaches.
	start   int
	ceiling int
	mode    string
	// profile is the routing profile HIVE_PROFILE names, when the
	// models config holds it.
	profile string
	// pinned is the hive-research model the profile routes when it defines
	// no hive_tiers: the ladder is that model alone, and a rise does not
	// apply.
	pinned string
	// keepsLocal is true when the profile gives hive-research no cloud
	// route, so the hive may not sit on a model off this machine.
	keepsLocal bool
	cfg        *models.Config
}

// activeTierPolicy is the policy under this process's models config,
// HIVE_PROFILE and HIVE_BUDGET_MODE.
func activeTierPolicy() tierPolicy {
	return tierPolicyFrom(models.Load(), os.Getenv("HIVE_PROFILE"), os.Getenv("HIVE_BUDGET_MODE"))
}

// tierPolicyFrom builds the policy. The ladder is cfg.HiveLadder(profile).
// Under a profile the config holds, hive-research leaves this machine only
// on a cloud route (provider other than local, or an Ollama cloud tag), as a
// profile lets only a routed role leave (runner.md § Routing profiles). The
// start rung is the ladder's middle, rounding down; when that rung is off
// this machine where the profile keeps the hive on it, it is the nearest
// rung on this machine below the middle, else above, else the middle.
// Budget modes premium and standard allow the whole ladder; cheap, free and
// a name that is no mode allow no rung above the start.
func tierPolicyFrom(cfg *models.Config, profile, mode string) tierPolicy {
	route := routeOf(cfg, profile)
	p := tierPolicy{
		ladder:     cfg.HiveLadder(profile),
		cfg:        cfg,
		mode:       budgetMode(mode),
		profile:    route.profile,
		pinned:     route.pinned,
		keepsLocal: route.keepsLocal,
	}
	p.start = p.startRung()
	p.ceiling = p.ceilingRung()
	return p
}

// budgetMode is the budget mode a value names, lower-cased; standard when
// it is empty.
func budgetMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return "standard"
	}
	return m
}

// profileRoute is what the routing profile says of hive-research: the
// profile's name, the model it pins, and whether it keeps the hive on this
// machine. It is zero when the config holds no such profile.
type profileRoute struct {
	profile    string
	pinned     string
	keepsLocal bool
}

// routeOf reads hive-research's route under the named profile.
func routeOf(cfg *models.Config, profile string) profileRoute {
	prof, ok := cfg.Profiles[profile]
	if !ok || profile == "" {
		return profileRoute{}
	}
	route := prof.Roles["hive-research"]
	return profileRoute{
		profile:    profile,
		pinned:     pinnedModel(prof, route),
		keepsLocal: !cloudRoute(prof, route),
	}
}

// pinnedModel is the model a profile with no hive_tiers routes
// hive-research to, or "".
func pinnedModel(prof models.Profile, route models.Route) string {
	if len(prof.HiveTiers) == 0 && route.Model != "" {
		return route.Model
	}
	return ""
}

// cloudRoute reports whether the route sends hive-research off this
// machine: a provider other than local, or an Ollama cloud tag.
func cloudRoute(prof models.Profile, route models.Route) bool {
	return route.Model != "" && (offMachineProvider(routeProvider(prof, route)) || models.OllamaCloudModel(route.Model))
}

// routeProvider is the route's provider, else the profile's.
func routeProvider(prof models.Profile, route models.Route) string {
	if provider := strings.TrimSpace(route.Provider); provider != "" {
		return provider
	}
	return strings.TrimSpace(prof.Provider)
}

// offMachineProvider reports whether a provider is named and is not local.
func offMachineProvider(provider string) bool {
	return provider != "" && !strings.EqualFold(provider, "local")
}

// startRung is the ladder's middle, rounding down, or, when that rung is
// off this machine where the profile keeps the hive on it, the nearest rung
// on this machine below it, else above, else the middle.
func (p tierPolicy) startRung() int {
	start := (len(p.ladder) - 1) / 2
	if !p.leaves(p.ladder[start]) {
		return start
	}
	if i := nearestRung(len(p.ladder), start, p.onMachine); i >= 0 {
		return i
	}
	return start
}

// ceilingRung is the highest rung the budget mode allows: the ladder's top
// under premium and standard, else the start rung.
func (p tierPolicy) ceilingRung() int {
	if p.mode != "premium" && p.mode != "standard" {
		return p.start
	}
	return len(p.ladder) - 1
}

// leaves reports whether rung is off this machine where the profile keeps
// the hive on it.
func (p tierPolicy) leaves(rung string) bool {
	return p.keepsLocal && p.cfg.OffMachineModel(rung)
}

// onMachine reports whether rung i of the ladder may host the hive under
// the profile.
func (p tierPolicy) onMachine(i int) bool {
	return !p.leaves(p.ladder[i])
}

// inRange reports whether rung i of the ladder is one the tier may sit on:
// at or below the ceiling, and not off this machine where the profile keeps
// the hive on it.
func (p tierPolicy) inRange(i int) bool {
	return i >= 0 && i <= p.ceiling && !p.leaves(p.ladder[i])
}

// topReason says why the ceiling is the highest rung: the ladder's top, or
// the budget mode's limit.
func (p tierPolicy) topReason() string {
	top := fmt.Sprintf("%s is the highest rung", p.ladder[p.ceiling])
	switch {
	case p.ceiling == len(p.ladder)-1:
		return top + " of the ladder"
	case p.mode == "cheap" || p.mode == "free":
		return top + fmt.Sprintf(" budget mode %s allows", p.mode)
	}
	return top + fmt.Sprintf(" budget mode %q, which is no mode, allows", p.mode)
}

// offMachine says that rung is off this machine and the profile keeps
// hive-research on it.
func (p tierPolicy) offMachine(rung string) string {
	return fmt.Sprintf("%s is off this machine, and routing profile %s keeps hive-research on it", rung, p.profile)
}

// fallRung is the next rung in the range below idx.
func (p tierPolicy) fallRung(idx int) int {
	to := idx - 1
	for !p.inRange(to) {
		to--
	}
	return to
}

// nearestRung is the rung of a ladder of n rungs nearest below i that ok
// accepts, else the nearest above it, or -1 when ok accepts none.
func nearestRung(n, i int, ok func(int) bool) int {
	if j := nearestBelow(i, ok); j >= 0 {
		return j
	}
	return nearestAbove(n, i, ok)
}

// nearestBelow is the highest rung below i that ok accepts, or -1.
func nearestBelow(i int, ok func(int) bool) int {
	for j := i - 1; j >= 0; j-- {
		if ok(j) {
			return j
		}
	}
	return -1
}

// nearestAbove is the lowest rung above i, of a ladder of n rungs, that ok
// accepts, or -1.
func nearestAbove(n, i int, ok func(int) bool) int {
	for j := i + 1; j < n; j++ {
		if ok(j) {
			return j
		}
	}
	return -1
}

// tierDecision is what the tier rule decides at one scan.
type tierDecision struct {
	// Holds is true when unknowns dominate the latest wave; Clear is true
	// when they are at most tierClearShare of it.
	Holds, Clear bool
	From, To     string
	// Outcome is hold, rise, fall, clamp or not_applicable.
	Outcome string
	Reason  string
	// Clock is the rule's clock after this scan.
	Clock TierClock
}

// changes reports whether the decision moves the tier.
func (d tierDecision) changes() bool { return d.To != d.From }

// unknownsDominate reports whether the scan raised the shaking signal for a
// latest wave that unknowns dominate.
func unknownsDominate(signals []Signal) bool {
	for _, s := range filterSignals(signals, "shaking_signal") {
		if s.Payload["adjust"] == "upgrade_model_tier" {
			return true
		}
	}
	return false
}

// decideTier is the model-tier rule. It:
//   - moves a tier outside the range into it, however many rungs away: one
//     off the ladder to the start rung, any other to the nearest rung in
//     the range below it, else above it;
//   - raises it one rung when unknowns dominate the latest wave, unless it
//     rose fewer than tierChangeEvery scans ago or is at the ceiling;
//   - lowers it to the next rung in the range below it, down to the start
//     rung, on the tierClearScans-th clear scan in a row;
//   - records a move as not applicable, and holds, when the profile pins
//     hive-research with no hive_tiers, when a rise would take the hive off
//     this machine where the profile keeps it on, or when no rung of the
//     ladder is in the range.
func decideTier(state *State, signals []Signal, p tierPolicy) tierDecision {
	s := newTierScan(state, signals, p)
	switch {
	case !p.inRange(s.idx):
		s.clamp()
	case s.d.Holds:
		s.rise()
	case s.d.Clear:
		s.clearScan()
	default:
		s.d.Reason = fmt.Sprintf("unknowns are %d of %d findings in wave %d, more than a quarter: the count of clear scans restarts", s.unknowns, s.inWave, s.wave)
	}
	return s.d
}

// tierScan is one application of the tier rule: its inputs and the
// decision it builds.
type tierScan struct {
	p    tierPolicy
	tier string
	wave int
	// clock is the rule's clock before this scan.
	clock TierClock
	// idx is the tier's rung on the ladder, -1 when it is not on it.
	idx      int
	unknowns int
	inWave   int
	d        tierDecision
}

// newTierScan reads the rule's inputs and starts a decision that holds.
func newTierScan(state *State, signals []Signal, p tierPolicy) *tierScan {
	tier := state.Hive.ModelTier
	s := &tierScan{
		p:        p,
		tier:     tier,
		wave:     state.LatestWave,
		clock:    state.TierClock,
		idx:      slices.Index(p.ladder, tier),
		unknowns: state.LatestWaveLabels["unknown"],
		inWave:   waveSize(state.LatestWaveLabels),
	}
	s.d = tierDecision{Holds: unknownsDominate(signals), From: tier, To: tier, Outcome: "hold"}
	s.d.Clear = !s.d.Holds && float64(s.unknowns) <= tierClearShare*float64(s.inWave)
	s.d.Clock = s.clock.next(s.d.Clear)
	return s
}

// clamp moves a tier outside the range into it, or records the move as not
// applicable when no rung is in the range.
func (s *tierScan) clamp() {
	why, to := s.outOfRange()
	if !s.p.inRange(to) {
		s.d.Outcome = "not_applicable"
		s.d.Reason = fmt.Sprintf("%s; not applicable: no rung of the ladder %v is on this machine, and routing profile %s keeps hive-research on it", why, s.p.ladder, s.p.profile)
		return
	}
	s.d.To, s.d.Outcome, s.d.Reason = s.p.ladder[to], "clamp", why
}

// outOfRange is why the tier is outside the range and the rung it moves to:
// the start rung for a tier off the ladder, else the nearest rung in the
// range below it, else above it.
func (s *tierScan) outOfRange() (string, int) {
	switch {
	case s.idx < 0:
		return fmt.Sprintf("%s is not on the ladder %v", s.tier, s.p.ladder), s.p.start
	case s.idx > s.p.ceiling:
		return fmt.Sprintf("%s is above the range: %s", s.tier, s.p.topReason()), nearestRung(len(s.p.ladder), s.idx, s.p.inRange)
	}
	return s.p.offMachine(s.tier), nearestRung(len(s.p.ladder), s.idx, s.p.inRange)
}

// rise raises the tier one rung for a wave that unknowns dominate, unless a
// rule holds it.
func (s *tierScan) rise() {
	outcome, why := s.riseBlocked()
	if outcome != "" {
		s.d.Outcome = outcome
		s.d.Reason = fmt.Sprintf("unknowns dominate wave %d; %s", s.wave, why)
		return
	}
	s.d.To, s.d.Outcome, s.d.Reason = s.p.ladder[s.idx+1], "rise", fmt.Sprintf("unknowns dominate wave %d", s.wave)
	s.d.Clock.Wait = tierChangeEvery - 1
}

// riseBlocked is the outcome and reason of the first rule that stops a rise,
// in the order the rule checks them, or "" when the rise applies.
func (s *tierScan) riseBlocked() (outcome, why string) {
	switch {
	case s.p.pinned != "":
		return "not_applicable", fmt.Sprintf("not applicable: routing profile %s pins hive-research to %s and defines no hive_tiers", s.p.profile, s.p.pinned)
	case s.clock.Wait > 0:
		return "hold", fmt.Sprintf("the tier rose fewer than %d scans ago", tierChangeEvery)
	case s.idx >= s.p.ceiling:
		return "hold", s.p.topReason()
	case s.p.leaves(s.p.ladder[s.idx+1]):
		return "not_applicable", "not applicable: " + s.p.offMachine(s.p.ladder[s.idx+1])
	}
	return "", ""
}

// clearScan records a clear scan and, on the tierClearScans-th in a row,
// lowers the tier to the next rung in the range below it, down to the start
// rung.
func (s *tierScan) clearScan() {
	s.d.Reason = fmt.Sprintf("unknowns have been at most a quarter of the latest wave for %d scans", s.d.Clock.Clear)
	if s.d.Clock.Clear < tierClearScans || s.idx <= s.p.start {
		return
	}
	s.d.To, s.d.Outcome = s.p.ladder[s.p.fallRung(s.idx)], "fall"
	s.d.Clock.Clear = 0
}

// waveSize is the number of findings in a wave's label counts.
func waveSize(labels map[string]int) int {
	n := 0
	for _, c := range labels {
		n += c
	}
	return n
}
