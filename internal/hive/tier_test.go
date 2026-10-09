package hive

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// The model-tier rule, one test per property: climb, cap, hold, fall back,
// no flapping, and a local-only profile. Expected scans and rungs are
// computed from tierChangeEvery (N), tierClearScans (M), the two shares and
// the ladder.

// waveUnknowns is how many unknowns a scan's latest wave of
// unknownShakingMinFindings findings holds, by the kind of scan a letter
// names: D the fewest that dominate it (above unknownShakingShare), C the
// most a clear scan allows (at most tierClearShare), and B one more than
// that, in the band between.
func waveUnknowns(t *testing.T, kind rune) int {
	t.Helper()
	n := unknownShakingMinFindings
	dominate := int(unknownShakingShare*float64(n)) + 1
	clear := int(tierClearShare * float64(n))
	if clear+1 >= dominate {
		t.Fatalf("setup: a wave of %d has no count of unknowns between %d (clear) and %d (dominated)", n, clear, dominate)
	}
	switch kind {
	case 'D':
		return dominate
	case 'B':
		return clear + 1
	case 'C':
		return clear
	}
	t.Fatalf("setup: scan kind %q is none of D, B, C", kind)
	return 0
}

// runTier feeds decideTier one scan per letter of scans (D, B or C, as
// waveUnknowns), with the signals evalShaking raises for that wave, carrying
// the tier and the clock from each decision to the next, as --apply and
// ScanState do.
func runTier(t *testing.T, p tierPolicy, tier string, scans string) []tierDecision {
	t.Helper()
	var out []tierDecision
	var clock TierClock
	for i, kind := range scans {
		u := waveUnknowns(t, kind)
		state := &State{
			Hive:             HiveState{ModelTier: tier},
			LatestWave:       i + 1,
			LatestWaveLabels: map[string]int{"unknown": u, "assumption": unknownShakingMinFindings - u},
			TierClock:        clock,
		}
		d := decideTier(state, evalShaking(state, nil), p)
		out = append(out, d)
		tier, clock = d.To, d.Clock
	}
	return out
}

// rungLadder is a ladder of n rungs named r0 (cheapest) to r<n-1>.
func rungLadder(n int) *models.Config {
	cfg := &models.Config{}
	for i := 0; i < n; i++ {
		cfg.HiveTiers = append(cfg.HiveTiers, fmt.Sprintf("r%d", i))
	}
	return cfg
}

// Climb: while unknowns dominate, the tier rises one step on the first scan
// and then once every N scans, until the top.
func TestTierRule_Climbs(t *testing.T) {
	p := tierPolicyFrom(rungLadder(7), "", "standard")
	top := len(p.ladder) - 1
	scans := 1 + (top-p.start)*tierChangeEvery + 3
	for k, d := range runTier(t, p, p.ladder[p.start], strings.Repeat("D", scans)) {
		scan := k + 1
		want := min(p.start+1+(scan-1)/tierChangeEvery, top)
		if got := slices.Index(p.ladder, d.To); got != want {
			t.Fatalf("scan %d: tier %s (rung %d), want rung %d", scan, d.To, got, want)
		}
		rose := (scan-1)%tierChangeEvery == 0 && want > slices.Index(p.ladder, d.From)
		if (d.Outcome == "rise") != rose {
			t.Errorf("scan %d: outcome %q (%s), want a rise: %v", scan, d.Outcome, d.Reason, rose)
		}
		if d.Outcome == "rise" && !strings.Contains(d.Reason, fmt.Sprintf("wave %d", scan)) {
			t.Errorf("scan %d: a rise's reason %q does not name the wave", scan, d.Reason)
		}
	}
}

// Cap: the tier never rises above the highest rung the budget mode allows.
// Standard and premium allow the whole ladder; cheap, free and a name that
// is no mode none above the start rung. A tier above the range is brought to
// the ceiling in one scan, however many rungs above it is, and one off the
// ladder is set to the start rung.
func TestTierRule_Caps(t *testing.T) {
	cfg := rungLadder(5)
	for _, mode := range []string{"", "standard", "premium", "cheap", "free", "chaep"} {
		t.Run("mode="+mode, func(t *testing.T) {
			p := tierPolicyFrom(cfg, "", mode)
			top, ceiling := len(p.ladder)-1, len(p.ladder)-1
			if mode != "" && mode != "standard" && mode != "premium" {
				ceiling = p.start
			}
			decisions := runTier(t, p, p.ladder[p.start], strings.Repeat("D", 20))
			for k, d := range decisions {
				if i := slices.Index(p.ladder, d.To); i > ceiling {
					t.Fatalf("scan %d: tier %s is above rung %d", k+1, d.To, ceiling)
				}
			}
			last := decisions[len(decisions)-1]
			if last.To != p.ladder[ceiling] || last.Outcome != "hold" || !strings.Contains(last.Reason, p.ladder[ceiling]+" is the highest rung") {
				t.Errorf("after 20 scans of unknowns: %+v, want a hold at %s", last, p.ladder[ceiling])
			}
			if ceiling < top && (!strings.Contains(last.Reason, "budget mode ") || !strings.Contains(last.Reason, mode)) {
				t.Errorf("reason %q does not name budget mode %s", last.Reason, mode)
			}
			if mode == "chaep" && !strings.Contains(last.Reason, "is no mode") {
				t.Errorf("reason %q does not say %s is no mode", last.Reason, mode)
			}

			above := runTier(t, p, p.ladder[top], "D")[0]
			if want := p.ladder[ceiling]; above.To != want || (ceiling < top) != (above.Outcome == "clamp") {
				t.Errorf("from the top rung: %+v, want %s in one scan", above, want)
			}
			off := runTier(t, p, "no-such-rung", "D")[0]
			if want := p.ladder[p.start]; off.To != want || off.Outcome != "clamp" {
				t.Errorf("from a tier off the ladder: %+v, want a clamp to %s", off, want)
			}
		})
	}
}

// Hold: within N scans of a rise, unknowns do not raise the tier again, and
// the reason names the wait; fewer than M clear scans in a row do not lower
// it, and a scan in the band restarts the count.
func TestTierRule_Holds(t *testing.T) {
	p := tierPolicyFrom(rungLadder(7), "", "standard")
	clears := strings.Repeat("C", tierClearScans-1)
	scans := strings.Repeat("D", tierChangeEvery) + clears + "B" + clears
	decisions := runTier(t, p, p.ladder[p.start], scans)
	if decisions[0].Outcome != "rise" {
		t.Fatalf("scan 1: %+v, want a rise", decisions[0])
	}
	for k, d := range decisions[1:] {
		scan := k + 2
		if d.changes() || d.To != p.ladder[p.start+1] {
			t.Errorf("scan %d (%c): %+v, want the tier held at %s", scan, scans[scan-1], d, p.ladder[p.start+1])
		}
		if scan <= tierChangeEvery && !strings.Contains(d.Reason, fmt.Sprintf("rose fewer than %d scans ago", tierChangeEvery)) {
			t.Errorf("scan %d: reason %q, want the wait named", scan, d.Reason)
		}
		if scans[scan-1] == 'B' && (d.Clock.Clear != 0 || !strings.Contains(d.Reason, "restarts")) {
			t.Errorf("scan %d in the band: %+v, want the clear count restarted", scan, d)
		}
	}
}

// Fall back: on each M-th clear scan in a row the tier falls one step, down
// to the start rung and no further. A wave of unknowns right after a fall
// raises the tier at once: only a rise makes the next one wait.
func TestTierRule_FallsBack(t *testing.T) {
	p := tierPolicyFrom(rungLadder(7), "", "standard")
	const risen = 2
	lastRise := 1 + (risen-1)*tierChangeEvery
	clear := risen*tierClearScans + tierClearScans + 2
	decisions := runTier(t, p, p.ladder[p.start], strings.Repeat("D", lastRise)+strings.Repeat("C", clear))
	if got := decisions[lastRise-1].To; got != p.ladder[p.start+risen] {
		t.Fatalf("setup: tier %s after scan %d, want %s", got, lastRise, p.ladder[p.start+risen])
	}
	for k := lastRise; k < len(decisions); k++ {
		scan, d := k+1, decisions[k]
		falls := (scan - lastRise) / tierClearScans
		fell := (scan-lastRise)%tierClearScans == 0 && falls <= risen
		want := p.start + risen - min(falls, risen)
		if got := slices.Index(p.ladder, d.To); got != want {
			t.Fatalf("scan %d: tier %s (rung %d), want rung %d", scan, d.To, got, want)
		}
		if (d.Outcome == "fall") != fell {
			t.Errorf("scan %d: outcome %q (%s), want a fall: %v", scan, d.Outcome, d.Reason, fell)
		}
		if fell && !strings.Contains(d.Reason, fmt.Sprintf("for %d scans", tierClearScans)) {
			t.Errorf("scan %d: a fall's reason %q does not name the %d clear scans", scan, d.Reason, tierClearScans)
		}
	}

	again := runTier(t, p, p.ladder[p.start], "D"+strings.Repeat("C", tierClearScans)+"D")
	if fall, rise := again[tierClearScans], again[tierClearScans+1]; fall.Outcome != "fall" || rise.Outcome != "rise" || rise.To != p.ladder[p.start+1] {
		t.Errorf("a wave of unknowns right after a fall: %+v then %+v, want a fall and then a rise to %s", fall, rise, p.ladder[p.start+1])
	}
}

// No flapping, for every run of 9 scans of D, B and C from every rung of the
// ladder: the tier stays on the ladder and moves at most one step a scan; a
// scan in the band never moves it; a rise comes only on a scan whose wave
// unknowns dominate, and at least N scans after the rise before it; a fall
// comes only on the M-th clear scan in a row, never below the start rung. So
// the tier turns from rising to falling only after M clear scans in a row,
// and from falling to rising only on a wave that unknowns dominate: waves
// that stay inside the band, or cross only one of its edges, never turn it.
func TestTierRule_NoFlapping(t *testing.T) {
	if tierChangeEvery < 2 || tierClearScans <= tierChangeEvery {
		t.Fatalf("N=%d, M=%d: the clocks' rationale needs N of at least 2 (the stall window) and M above N", tierChangeEvery, tierClearScans)
	}
	const length = 9
	p := tierPolicyFrom(rungLadder(5), "", "standard")
	top := len(p.ladder) - 1
	kinds := []byte("DBC")
	runs := 1
	for i := 0; i < length; i++ {
		runs *= len(kinds)
	}
	scans := make([]byte, length)
	for from := 0; from <= top; from++ {
		for r := 0; r < runs; r++ {
			for i, n := 0, r; i < length; i, n = i+1, n/len(kinds) {
				scans[i] = kinds[n%len(kinds)]
			}
			lastRise, prev := -1, from
			for k, d := range runTier(t, p, p.ladder[from], string(scans)) {
				idx := slices.Index(p.ladder, d.To)
				fail := func(why string) {
					t.Fatalf("from %s, scans %s, scan %d: %s (%+v)", p.ladder[from], scans, k+1, why, d)
				}
				switch {
				case idx < 0 || idx > top:
					fail("the tier left the ladder")
				case idx-prev > 1 || prev-idx > 1:
					fail("the tier moved more than one step")
				case idx != prev && scans[k] == 'B':
					fail("a scan in the band moved the tier")
				case idx > prev && scans[k] != 'D':
					fail("rose on a scan that unknowns do not dominate")
				case idx > prev && lastRise >= 0 && k-lastRise < tierChangeEvery:
					fail(fmt.Sprintf("rose %d scans after the last rise", k-lastRise))
				case idx < prev && idx < p.start:
					fail("fell below the start rung")
				case idx < prev && (k+1 < tierClearScans || strings.ContainsAny(string(scans[k+1-tierClearScans:k+1]), "DB")):
					fail(fmt.Sprintf("fell without %d clear scans in a row", tierClearScans))
				}
				if idx > prev {
					lastRise = k
				}
				prev = idx
			}
		}
	}

	// Waves either side of one edge of the band, however long the run.
	for _, pattern := range []string{"DB", "DBB", "DBBBB", "DC", "DCB", "CB", "CCB"} {
		scans := strings.Repeat(pattern, 12)
		for k, d := range runTier(t, p, p.ladder[p.start+1], scans) {
			if d.Outcome == "fall" {
				t.Fatalf("scans %s…: fell at scan %d: %+v", pattern, k+1, d)
			}
			if d.Outcome == "rise" && !strings.Contains(pattern, "D") {
				t.Fatalf("scans %s…: rose at scan %d: %+v", pattern, k+1, d)
			}
		}
	}
}

// The tier cycles only while the waves themselves swing across the whole
// band: a wave that unknowns dominate, then clear ones. With fewer than M
// clear waves between the dominated ones it never falls. With M or more it
// rises on each dominated wave and falls on the M-th clear one after it, so
// each cycle takes at least M+1 scans.
func TestTierRule_CyclesOnlyAcrossTheBand(t *testing.T) {
	p := tierPolicyFrom(rungLadder(7), "", "standard")
	for gap := 1; gap <= 2*tierClearScans; gap++ {
		period := "D" + strings.Repeat("C", gap)
		const periods = 10
		decisions := runTier(t, p, p.ladder[p.start], strings.Repeat(period, periods))
		for k, d := range decisions {
			phase := k % len(period)
			switch {
			case gap < tierClearScans && d.Outcome == "fall":
				t.Fatalf("period %s: fell at scan %d: %+v", period, k+1, d)
			case gap >= tierClearScans:
				wantRise, wantFall := phase == 0, phase == tierClearScans
				if (d.Outcome == "rise") != wantRise || (d.Outcome == "fall") != wantFall {
					t.Fatalf("period %s, scan %d: outcome %q, want rise %v, fall %v (%+v)", period, k+1, d.Outcome, wantRise, wantFall, d)
				}
			}
		}
	}
}

// A local-only profile: its hive_tiers is its escalation for hive-research,
// and the rule climbs it only onto rungs on this machine. A rung off it (a
// model the config lists under a cloud family, by id or alias, or an Ollama
// cloud tag) is not applicable while the profile keeps hive-research local;
// a profile that routes hive-research to the cloud with because: may climb
// there. A profile that pins hive-research with no hive_tiers has no
// escalation, and the rise is not applicable.
func TestTierRule_LocalOnlyProfile(t *testing.T) {
	const opus = "claude-opus-4-8"
	research := map[string]models.Route{"hive-research": {Model: "qwen-mid"}}
	cfg := &models.Config{
		Models:  map[string]models.Model{opus: {Family: "anthropic"}},
		Aliases: map[string]string{"opus": opus},
		Profiles: map[string]models.Profile{
			"cloud-top":  {Provider: "local", HiveTiers: []string{"qwen-small", "qwen-mid", opus}, Roles: research},
			"cloud-tag":  {Provider: "local", HiveTiers: []string{"qwen-small", "qwen-mid", "gpt-oss:120b-cloud"}, Roles: research},
			"alias-top":  {Provider: "local", HiveTiers: []string{"qwen-small", "qwen-mid", "opus"}, Roles: research},
			"local-top":  {Provider: "local", HiveTiers: []string{"qwen-small", "qwen-mid", "qwen-big"}, Roles: research},
			"no-route":   {Provider: "local", HiveTiers: []string{"qwen-small", "qwen-mid", opus}},
			"pinned":     {Provider: "local", Roles: research},
			"cloud-role": {Provider: "local", HiveTiers: []string{"qwen-small", "qwen-mid", opus}, Roles: map[string]models.Route{"hive-research": {Model: "claude-sonnet-4-6", Provider: "anthropic", Because: "the test's evidence"}}},
		},
	}
	for _, tc := range []struct {
		profile, want string
		reason        string
	}{
		{"cloud-top", "qwen-mid", opus + " is off this machine"},
		{"cloud-tag", "qwen-mid", "gpt-oss:120b-cloud is off this machine"},
		{"alias-top", "qwen-mid", "opus is off this machine"},
		{"no-route", "qwen-mid", opus + " is off this machine"},
		{"pinned", "qwen-mid", "pins hive-research to qwen-mid"},
		{"local-top", "qwen-big", ""},
		{"cloud-role", opus, ""},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			p := tierPolicyFrom(cfg, tc.profile, "standard")
			if p.profile != tc.profile {
				t.Fatalf("policy profile %q, want %q", p.profile, tc.profile)
			}
			decisions := runTier(t, p, "qwen-mid", strings.Repeat("D", 3*tierChangeEvery))
			for k, d := range decisions {
				if tc.reason == "" {
					break
				}
				if d.changes() || d.Outcome != "not_applicable" || !strings.Contains(d.Reason, tc.reason) || !strings.Contains(d.Reason, "routing profile "+tc.profile) {
					t.Fatalf("scan %d: %+v, want not_applicable naming %q", k+1, d, tc.reason)
				}
			}
			if got := decisions[len(decisions)-1].To; got != tc.want {
				t.Errorf("tier %s after %d scans of unknowns, want %s", got, len(decisions), tc.want)
			}
		})
	}
}

// Under a local-only profile a rung off this machine is outside the range,
// wherever it sits on the ladder: a hive starts at the nearest rung on this
// machine when the middle is off it; a tier already on such a rung (inherited
// from another run or set by params) moves to the nearest rung on it below,
// in one scan, and a fall steps over such a rung. With no rung of the ladder
// on this machine, every scan records not_applicable and the tier holds.
func TestTierRule_LocalOnlyRange(t *testing.T) {
	const opus, sonnet, haiku = "claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"
	local := func(model string) map[string]models.Route {
		return map[string]models.Route{"hive-research": {Model: model}}
	}
	cfg := &models.Config{
		Models:    map[string]models.Model{opus: {Family: "anthropic"}, sonnet: {Family: "anthropic"}, haiku: {Family: "anthropic"}},
		Aliases:   map[string]string{"opus": opus, "sonnet": sonnet, "haiku": haiku},
		HiveTiers: []string{"haiku", "sonnet", "opus"},
		Profiles: map[string]models.Profile{
			"cloud-mid": {Provider: "local", HiveTiers: []string{"qwen-small", "gpt-oss:120b-cloud", opus}, Roles: local("qwen-small")},
			"cloud-top": {Provider: "local", HiveTiers: []string{"qwen-small", "qwen-mid", "sonnet"}, Roles: local("qwen-mid")},
			"sandwich":  {Provider: "local", HiveTiers: []string{"q0", "q1", "q2", "gpt-oss:20b-cloud", "q4"}, Roles: local("q2")},
			"all-cloud": {Provider: "local"},
		},
	}
	policy := func(profile string) tierPolicy { return tierPolicyFrom(cfg, profile, "standard") }

	if p := policy("cloud-mid"); p.ladder[p.start] != "qwen-small" {
		t.Errorf("cloud-mid starts at %s, want qwen-small: its middle rung is off this machine", p.ladder[p.start])
	}
	for _, tc := range []struct{ profile, from, to string }{
		{"cloud-mid", "gpt-oss:120b-cloud", "qwen-small"},
		{"cloud-mid", opus, "qwen-small"},
		{"cloud-top", "sonnet", "qwen-mid"},
		{"sandwich", "gpt-oss:20b-cloud", "q2"},
	} {
		d := runTier(t, policy(tc.profile), tc.from, "C")[0]
		if d.To != tc.to || d.Outcome != "clamp" || !strings.Contains(d.Reason, tc.from+" is off this machine") {
			t.Errorf("%s from %s: %+v, want a clamp to %s naming %s off this machine", tc.profile, tc.from, d, tc.to, tc.from)
		}
	}

	p := policy("sandwich")
	falls := runTier(t, p, "q4", strings.Repeat("C", 2*tierClearScans))
	if d := falls[tierClearScans-1]; d.Outcome != "fall" || d.To != "q2" {
		t.Errorf("sandwich from q4: %+v on clear scan %d, want a fall over the cloud rung to q2", d, tierClearScans)
	}
	if d := runTier(t, p, "q2", "D")[0]; d.Outcome != "not_applicable" || d.changes() {
		t.Errorf("sandwich from q2 under unknowns: %+v, want not_applicable", d)
	}

	p = policy("all-cloud")
	for _, from := range []string{"sonnet", "opus", "no-such-rung"} {
		for k, d := range runTier(t, p, from, "DCCCC") {
			if d.changes() || d.Outcome != "not_applicable" || !strings.Contains(d.Reason, "no rung of the ladder") {
				t.Fatalf("all-cloud from %s, scan %d: %+v, want not_applicable naming no rung on this machine", from, k+1, d)
			}
		}
	}
}

// scanApply scans, plans and records one --apply pass, and returns the plan
// and the hive_tier_log row it wrote.
func scanApply(t *testing.T, s *db.Store) ([]Action, tierRow) {
	t.Helper()
	state, signals, plan := scanAndPlan(t, s)
	if _, err := RecordScan(s, Scan{Project: "p", State: state, Fired: signals, Apply: true, Plan: plan}); err != nil {
		t.Fatal(err)
	}
	var r tierRow
	if err := s.ReadDB.QueryRow(
		`SELECT iteration, from_tier, to_tier, outcome, reason FROM hive_tier_log WHERE project='p' ORDER BY id DESC LIMIT 1`,
	).Scan(&r.iteration, &r.from, &r.to, &r.outcome, &r.reason); err != nil {
		t.Fatal(err)
	}
	return plan, r
}

type tierRow struct {
	iteration         int
	from, to, outcome string
	reason            string
}

// tierActions are the model_tier values the plan's adjust_params carry.
func tierActions(plan []Action) []any {
	var out []any
	for _, a := range plan {
		if v, ok := a.Params["model_tier"]; ok && a.Type == "adjust_params" {
			out = append(out, v)
		}
	}
	return out
}

// addScanWave adds a wave of unknownShakingMinFindings findings of the
// kind a letter names (waveUnknowns), the rest assumptions.
func addScanWave(t *testing.T, s *db.Store, wave int, kind rune) {
	t.Helper()
	u := waveUnknowns(t, kind)
	for i := 0; i < unknownShakingMinFindings; i++ {
		label := "assumption"
		if i < u {
			label = "unknown"
		}
		if _, err := s.WriteDB.Exec(
			`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding) VALUES (?,'test',?,?,0,0,?,'f')`,
			wave, 30+wave, i, label,
		); err != nil {
			t.Fatal(err)
		}
	}
}

// Through the database, on the shipped ladder, from its lowest rung: a wave
// of unknowns raises the tier one step on the first --apply scan, holds it
// on the next N-1 scans of the same wave for the wait, and raises it again
// on the scan after, up to the top. Scans of a wave in the band then hold
// it; scans of a clear wave lower it one step on each M-th, back to the
// start rung. Each pass writes one hive_tier_log row with its reason, and
// ScanState reads its clock back: the wait and the clear count both.
func TestTierRule_RecordedScans(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := InitProject(s, "p"); err != nil {
		t.Fatal(err)
	}
	ladder := ModelTiers()
	p := activeTierPolicy()
	top := len(ladder) - 1
	if p.start+1 > top || p.start < 1 {
		t.Fatalf("setup: ladder %v needs a rung below its start and one above", ladder)
	}
	if _, err := s.WriteDB.Exec(`UPDATE hive_state SET model_tier=? WHERE project='p'`, ladder[0]); err != nil {
		t.Fatal(err)
	}
	addScanWave(t, s, 1, 'D')

	pass, prev := 0, 0
	climb := 1 + top*tierChangeEvery
	for ; pass < climb; pass++ {
		scan := pass + 1
		want := min(1+(scan-1)/tierChangeEvery, top)
		plan, row := scanApply(t, s)
		_, _, tier := storedParams(t, s)
		if tier != ladder[want] || row.to != ladder[want] || row.iteration != scan {
			t.Fatalf("pass %d: tier %s, row %+v, want %s", scan, tier, row, ladder[want])
		}
		switch {
		case want > prev:
			if got := tierActions(plan); row.outcome != "rise" || !strings.Contains(row.reason, "wave 1") || len(got) != 1 || got[0] != ladder[want] {
				t.Fatalf("pass %d: row %+v, plan changes %v, want a rise to %s for wave 1", scan, row, got, ladder[want])
			}
		case (scan-1)%tierChangeEvery != 0:
			if row.outcome != "hold" || !strings.Contains(row.reason, fmt.Sprintf("rose fewer than %d scans ago", tierChangeEvery)) || len(tierActions(plan)) != 0 {
				t.Fatalf("pass %d: row %+v, want a hold that names the wait", scan, row)
			}
		default:
			if row.outcome != "hold" || !strings.Contains(row.reason, ladder[top]+" is the highest rung") {
				t.Fatalf("pass %d: row %+v, want a hold at the top", scan, row)
			}
		}
		prev = want
	}

	addScanWave(t, s, 2, 'B')
	for i := 0; i < tierClearScans+1; i++ {
		pass++
		if _, row := scanApply(t, s); row.outcome != "hold" || row.to != ladder[top] || !strings.Contains(row.reason, "restarts") {
			t.Fatalf("pass %d on a wave in the band: row %+v, want %s held", pass, row, ladder[top])
		}
	}

	addScanWave(t, s, 3, 'C')
	tier := top
	for clear := 1; clear <= (top-p.start+1)*tierClearScans; clear++ {
		pass++
		plan, row := scanApply(t, s)
		outcome := "hold"
		if clear%tierClearScans == 0 && tier > p.start {
			tier, outcome = tier-1, "fall"
		}
		if _, _, got := storedParams(t, s); got != ladder[tier] || row.outcome != outcome || row.to != ladder[tier] {
			t.Fatalf("clear scan %d: tier %s, row %+v, want %s (%s)", clear, got, row, ladder[tier], outcome)
		}
		if outcome == "fall" {
			if got := tierActions(plan); len(got) != 1 || got[0] != ladder[tier] || !strings.Contains(row.reason, fmt.Sprintf("for %d scans", tierClearScans)) {
				t.Errorf("the fall's plan changes %v and reason %q", got, row.reason)
			}
		}
	}
	if tier != p.start {
		t.Errorf("after the clear scans the tier is %s, want the start rung %s", ladder[tier], ladder[p.start])
	}

	var rows int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM hive_tier_log WHERE project='p'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != pass {
		t.Errorf("hive_tier_log holds %d rows, want one per --apply pass: %d", rows, pass)
	}
}

// Under the shipped local-fast profile, which pins hive-research and defines
// no hive_tiers, a wave of unknowns leaves the tier on local-fast's model and
// records the rise as not applicable.
func TestTierRule_ShippedLocalProfileRecordsNotApplicable(t *testing.T) {
	const profile = "local-fast"
	route, ok := models.Load().Profiles[profile].Roles["hive-research"]
	if !ok || len(models.Load().Profiles[profile].HiveTiers) != 0 {
		t.Fatalf("setup: %s must route hive-research and define no hive_tiers", profile)
	}
	t.Setenv("HIVE_PROFILE", profile)
	s := newTestStore(t)
	if _, _, err := InitProject(s, "p"); err != nil {
		t.Fatal(err)
	}
	addFindingsInWave(t, s, 1, "unknown", unknownShakingMinFindings)
	plan, row := scanApply(t, s)
	if _, _, tier := storedParams(t, s); tier != route.Model || len(tierActions(plan)) != 0 {
		t.Fatalf("tier %s, plan changes %v, want %s unchanged", tier, tierActions(plan), route.Model)
	}
	if row.outcome != "not_applicable" || row.to != route.Model || !strings.Contains(row.reason, "routing profile "+profile) {
		t.Errorf("row %+v, want not_applicable naming %s", row, profile)
	}
}
