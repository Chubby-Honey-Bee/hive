package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// An outage is not a result: a bench case asks the endpoint before its
// first run, and stops at a run that reached no model or after which the
// endpoint cannot be asked; a case that stopped, or in which no run
// completed, writes its rows marked as not a measurement; and a model that
// answers wrongly, or a server that refuses Queen but still lists its
// models, is recorded as data.
func TestBenchCase_AnOutageIsNotAResult(t *testing.T) {
	chb := buildChb(t, t.TempDir())
	t.Run("an endpoint down from the start", func(t *testing.T) { endpointDownFromTheStart(t, chb) })
	t.Run("a rerun with the endpoint down keeps the earlier results", func(t *testing.T) { rerunKeepsResults(t, chb) })
	t.Run("an endpoint that goes down after N runs", func(t *testing.T) { endpointDownAfterRuns(t, chb, false) })
	t.Run("an endpoint that goes down inside run N+1", func(t *testing.T) { endpointDownAfterRuns(t, chb, true) })
	t.Run("a run refused at its setup stops the case", func(t *testing.T) { refusedAtSetup(t, chb) })
	t.Run("a workflow that cannot be written is not an endpoint failure", func(t *testing.T) { workflowsNotWritten(t, chb) })
	t.Run("an endpoint missing a model", func(t *testing.T) { endpointMissingAModel(t, chb) })
	t.Run("wrong answers and a refused Queen are data", func(t *testing.T) { wrongAnswersAreData(t, chb) })
	t.Run("no completed run fails the case", func(t *testing.T) { noCompletedRun(t, chb) })
}

var oracle = func(it *bench.Item) (string, []string) { return it.Want.Verdict, it.Want.Tokens }

// The checks' names, up to their first " (".
const (
	endpointCheck = "the endpoint serves every model the case sends"
	stopCheck     = "every run reached a model, and the endpoint stayed up"
)

// caseChecks are the bench case's checks from report.json, by name up to
// its first " (".
func caseChecks(t *testing.T, workspace string) map[string]harnessCheck {
	t.Helper()
	var report struct {
		Results []harnessResult `json:"results"`
	}
	js, err := os.ReadFile(filepath.Join(workspace, "report.json"))
	if err != nil || json.Unmarshal(js, &report) != nil || len(report.Results) != 1 {
		t.Fatalf("report.json: %v", err)
	}
	checks := map[string]harnessCheck{}
	for _, ck := range report.Results[0].Checks {
		checks[strings.SplitN(ck.Name, " (", 2)[0]] = ck
	}
	return checks
}

// benchRun is one run of a bench case, in the order the harness runs them.
type benchRun struct {
	item, arm string
	rep       int
}

func (r benchRun) String() string { return fmt.Sprintf("%s %s r%d", r.item, r.arm, r.rep) }

// benchRuns lists a case's runs in the harness's order: each item, each
// repetition, each arm.
func benchRuns(t *testing.T, families []string, seeds []int64, arms []string, reps int) []benchRun {
	t.Helper()
	items, err := benchItems(liveRoster(t), harnessCase{Families: families, Seeds: seeds})
	if err != nil {
		t.Fatal(err)
	}
	return runsOf(items, arms, reps)
}

// runsOf lists the runs of items in the harness's order.
func runsOf(items []bench.Item, arms []string, reps int) []benchRun {
	var runs []benchRun
	for _, it := range items {
		for rep := 1; rep <= reps; rep++ {
			for _, a := range arms {
				runs = append(runs, benchRun{it.ID, a, rep})
			}
		}
	}
	return runs
}

// ran reports whether a run's directory is in the case's workspace.
func ran(h *agentHarness, r benchRun) bool {
	_, err := os.Stat(filepath.Join(h.Workspace, "bench", r.item, fmt.Sprintf("%s-r%d", r.arm, r.rep)))
	return err == nil
}

// closedURL is an OpenAI-compatible base URL on a port nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr + "/v1"
}

// markedRows reads a results file the harness marked as not a measurement:
// ReadRows, which chb bench decide reads with, must refuse it, and its
// first line says why. It returns why and the rows after that line.
func markedRows(t *testing.T, path string) (string, []bench.Row) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("results.jsonl: %v", err)
	}
	if _, err := bench.ReadRows(bytes.NewReader(raw)); !errors.Is(err, bench.ErrNotMeasured) {
		t.Fatalf("ReadRows(results.jsonl) = %v; want it refused as not a measurement", err)
	}
	first, rest, _ := bytes.Cut(raw, []byte("\n"))
	var mark struct {
		NotMeasured string `json:"not_measured"`
	}
	if err := json.Unmarshal(first, &mark); err != nil || mark.NotMeasured == "" {
		t.Fatalf("first line %s; want {\"not_measured\": why}", first)
	}
	rows, err := bench.ReadRows(bytes.NewReader(rest))
	if err != nil {
		t.Fatal(err)
	}
	return mark.NotMeasured, rows
}

// useLocal sends the local provider to the fake at OPENAI_BASE_URL and
// unsets OPENAI_BASE_URL.
func useLocal(t *testing.T) {
	t.Setenv("HIVE_LOCAL_BASE_URL", os.Getenv("OPENAI_BASE_URL"))
	t.Setenv("OPENAI_BASE_URL", "")
}

// userModels writes a models config holding the given profiles text and
// returns its path.
func userModels(t *testing.T, profiles string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte("profiles:\n"+profiles), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// setCase rewrites the harness's one-case suite with edit applied.
func setCase(t *testing.T, h *agentHarness, edit func(*harnessCase)) {
	t.Helper()
	raw, err := os.ReadFile(h.Suite)
	if err != nil {
		t.Fatal(err)
	}
	var suite harnessSuite
	if err := yaml.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	edit(&suite.Cases[0])
	if raw, err = yaml.Marshal(suite); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Suite, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// With the server down before the case starts, the case fails with the
// model preflight's message, which names the endpoint and the refused
// connection within what the console shows, runs no item and writes no
// results.
func endpointDownFromTheStart(t *testing.T, chb string) {
	seeds, families, arms := []int64{1}, []string{"F1"}, []string{bench.ArmSwarm, bench.ArmSolo}
	runs := benchRuns(t, families, seeds, arms, 1)
	model := &fakeModel{items: liveItems(t, families, seeds), policy: oracle}
	h, results := benchHarness(t, chb, model, arms, families, seeds)
	base := closedURL(t)
	t.Setenv("OPENAI_BASE_URL", base)
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the case passed with the endpoint down")
	}
	pre := caseChecks(t, h.Workspace)[endpointCheck]
	if shown := detailSuffix(pre.Detail); pre.OK || !strings.Contains(shown, base) || !strings.Contains(shown, "connection refused") {
		t.Errorf("endpoint check %+v; want it failed, naming %s and the refused connection in what the console shows: %q", pre, base, shown)
	}
	for _, r := range runs {
		if ran(h, r) {
			t.Errorf("%s ran with the endpoint down", r)
		}
	}
	if _, err := os.Stat(results); err == nil {
		t.Error("results.jsonl was written with the endpoint down")
	}
}

// A rerun into the workspace of an earlier run, with the endpoint down,
// fails before it runs anything and keeps the earlier results.
func rerunKeepsResults(t *testing.T, chb string) {
	seeds, families, arms := []int64{1}, []string{"F1"}, []string{bench.ArmSolo}
	model := &fakeModel{items: liveItems(t, families, seeds), policy: oracle}
	h, results := benchHarness(t, chb, model, arms, families, seeds)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before, err := os.ReadFile(results)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_BASE_URL", closedURL(t))
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the rerun passed with the endpoint down")
	}
	if pre := caseChecks(t, h.Workspace)[endpointCheck]; pre.OK {
		t.Fatalf("endpoint check passed with the endpoint down: %s", pre.Detail)
	}
	if after, err := os.ReadFile(results); err != nil || !bytes.Equal(after, before) {
		t.Errorf("results.jsonl after the rerun: %v, %d bytes; want the earlier %d bytes kept", err, len(after), len(before))
	}
}

// A server that goes down after it has answered n runs stops the case at
// run n+1, whether that run then fails its own model preflight and reaches
// no model, or, with midRun, passes it and loses the endpoint inside the
// run. The run is named with the refused connection within what the
// console shows, no later run starts, and the rows so far are written
// marked as not a measurement.
func endpointDownAfterRuns(t *testing.T, chb string, midRun bool) {
	seeds, families, arms := []int64{1}, []string{"F1", "F4"}, []string{bench.ArmSwarm, bench.ArmSolo}
	runs := benchRuns(t, families, seeds, arms, 1)
	n := len(runs) / 2
	model := &fakeModel{items: liveItems(t, families, seeds), policy: oracle, downAfter: n, downOnChat: midRun}
	h, results := benchHarness(t, chb, model, arms, families, seeds)
	console := filepath.Join(t.TempDir(), "console.log")
	out, err := os.Create(console)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	h.Stdout = out
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the case passed with the endpoint down after its first runs")
	}
	checks := caseChecks(t, h.Workspace)
	if pre := checks[endpointCheck]; !pre.OK {
		t.Fatalf("endpoint check before the first run failed: %s", pre.Detail)
	}
	which := "which reached no model"
	if midRun {
		which = "which did not complete"
	}
	head := fmt.Sprintf("stopped at run %d of %d (%s), %s", n+1, len(runs), runs[n], which)
	stop := checks[stopCheck]
	if shown := detailSuffix(stop.Detail); stop.OK || !strings.HasPrefix(stop.Detail, head) || !strings.Contains(shown, "connection refused") {
		t.Errorf("stop check %+v; want it failed, starting %q, with the refused connection in what the console shows: %q", stop, head, shown)
	}
	for i, r := range runs {
		if ran(h, r) != (i <= n) {
			t.Errorf("%s (run %d) ran %v; want only the first %d runs", r, i+1, ran(h, r), n+1)
		}
	}
	why, rows := markedRows(t, results)
	if !strings.HasPrefix(why, head) {
		t.Errorf("results.jsonl marks its rows %q; want the stop, %q", why, head)
	}
	if len(rows) != n+1 {
		t.Fatalf("%d rows; want the %d runs up to the stop", len(rows), n+1)
	}
	for i, r := range rows {
		if got := (benchRun{r.Item, r.Arm, r.Rep}); got != runs[i] {
			t.Errorf("row %d is %s; want %s", i+1, got, runs[i])
		}
	}
	log, err := os.ReadFile(console)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), fmt.Sprintf("stopped after %d of %d runs", n+1, len(runs))) || strings.Contains(string(log), "see the report for scores") {
		t.Errorf("console does not say the case stopped after %d of %d runs, or offers scores:\n%s", n+1, len(runs), log)
	}
}

// A run refused at its setup, before it wrote its run row, reached no
// model, so the case stops there whatever the endpoint says after it. With
// OPENAI_API_KEY empty the model preflight has nothing to ask with, before
// the first run or after it, and no run's backend can be built.
func refusedAtSetup(t *testing.T, chb string) {
	seeds, families, arms := []int64{1}, []string{"F1"}, []string{bench.ArmSwarm, bench.ArmSolo}
	runs := benchRuns(t, families, seeds, arms, 1)
	model := &fakeModel{items: liveItems(t, families, seeds), policy: oracle}
	h, results := benchHarness(t, chb, model, arms, families, seeds)
	t.Setenv("OPENAI_API_KEY", "")
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the case passed with no run's backend built")
	}
	checks := caseChecks(t, h.Workspace)
	if pre, ok := checks[endpointCheck]; ok {
		t.Errorf("endpoint check %+v, though nothing could be asked; want none", pre)
	}
	head := fmt.Sprintf("stopped at run 1 of %d (%s), which reached no model", len(runs), runs[0])
	if stop := checks[stopCheck]; stop.OK || !strings.HasPrefix(stop.Detail, head) || !strings.Contains(stop.Detail, "OPENAI_API_KEY") {
		t.Errorf("stop check %+v; want it failed, starting %q and naming OPENAI_API_KEY", stop, head)
	}
	for i, r := range runs {
		if ran(h, r) != (i == 0) {
			t.Errorf("%s (run %d) ran %v; want only the first", r, i+1, ran(h, r))
		}
	}
	if _, rows := markedRows(t, results); len(rows) != 1 {
		t.Errorf("%d rows; want the one run up to the stop", len(rows))
	}
	if calls := model.effortCounts(); len(calls) > 0 {
		t.Errorf("the server was sent model calls %v", calls)
	}
}

// A case whose workflows cannot be written, here for a forager preset
// that does not exist, fails under its own check, not the endpoint's, and
// runs nothing.
func workflowsNotWritten(t *testing.T, chb string) {
	seeds, families, arms := []int64{1}, []string{"F1"}, []string{bench.ArmSwarm, bench.ArmSolo}
	runs := benchRuns(t, families, seeds, arms, 1)
	model := &fakeModel{items: liveItems(t, families, seeds), policy: oracle}
	h, _ := benchHarness(t, chb, model, arms, families, seeds)
	preset := "no-such-forager"
	if _, err := foragers.Filter(liveRoster(t), []string{preset}); err == nil {
		t.Fatalf("preset %s selects foragers", preset)
	}
	setCase(t, h, func(c *harnessCase) { c.Foragers = preset })
	if err := h.run(context.Background()); err == nil {
		t.Fatalf("the case passed with preset %s", preset)
	}
	checks := caseChecks(t, h.Workspace)
	if wf := checks["the workflows the runs dispatch are written"]; wf.OK || !strings.Contains(wf.Detail, preset) {
		t.Errorf("workflow check %+v; want it failed, naming %s", wf, preset)
	}
	if pre, ok := checks[endpointCheck]; ok {
		t.Errorf("endpoint check %+v; want none, since no workflow was written to ask about", pre)
	}
	for _, r := range runs {
		if ran(h, r) {
			t.Errorf("%s ran", r)
		}
	}
}

// A model the endpoint does not serve fails the case before its first run,
// naming that model and no served one: Queen's, a lens model that is not
// first in the list, one only a later run's rotation sends, the solo
// control's, a routing profile's Queen, the evaluator's when the case sets
// eval, and the model of a node the profile does not route, which runs on
// the profile's provider.
func endpointMissingAModel(t *testing.T, chb string) {
	seeds, families := []int64{1}, []string{"F1"}
	both := []string{bench.ArmSwarm, bench.ArmSolo}
	// A custom profile lives in a models config this process has already
	// read (models.Load reads once), so its cases run the harness as chb.
	type setup struct {
		served []string // the models the endpoint serves
		want   string   // what the check must name
		cli    []string // when set, run chb agent-harness with these flags
	}
	cases := []struct {
		name  string
		arms  []string
		setup func(t *testing.T, h *agentHarness, items []bench.Item, lenses int) setup
	}{
		{"queen", []string{bench.ArmSwarm}, func(t *testing.T, h *agentHarness, _ []bench.Item, _ int) setup {
			return setup{served: []string{h.LensModel}, want: strconv.Quote(h.QueenModel)}
		}},
		{"the second model of a lens list", both, func(t *testing.T, h *agentHarness, _ []bench.Item, _ int) setup {
			h.LensModel = "fake-first,fake-second"
			return setup{served: []string{"fake-first", h.QueenModel}, want: strconv.Quote("fake-second")}
		}},
		{"a lens model only a later rotation sends", []string{bench.ArmSwarm}, func(t *testing.T, h *agentHarness, items []bench.Item, lenses int) setup {
			// One model more than the lenses: each rotation leaves one out.
			var models []string
			for i := 0; i <= lenses; i++ {
				models = append(models, fmt.Sprintf("fake-lens-%d", i))
			}
			h.LensModel, h.Reps = strings.Join(models, ","), 2
			if err := h.checkModelFlags(); err != nil {
				t.Fatal(err)
			}
			first := h.benchLensModels(items[0], 1)[:lenses]
			missing := ""
			for _, it := range items {
				for rep := 1; rep <= h.Reps; rep++ {
					for _, m := range h.benchLensModels(it, rep)[:lenses] {
						if missing == "" && !slices.Contains(first, m) {
							missing = m
						}
					}
				}
			}
			if missing == "" {
				t.Fatal("every run sends only the first run's lens models")
			}
			served := []string{h.QueenModel}
			for _, m := range models {
				if m != missing {
					served = append(served, m)
				}
			}
			return setup{served: served, want: strconv.Quote(missing)}
		}},
		{"the solo control", []string{bench.ArmSolo}, func(t *testing.T, h *agentHarness, _ []bench.Item, _ int) setup {
			return setup{served: []string{h.QueenModel}, want: strconv.Quote(h.LensModel)}
		}},
		{"a routing profile's queen", both, func(t *testing.T, h *agentHarness, _ []bench.Item, _ int) setup {
			p, err := runner.ResolveProfile("local-small")
			if err != nil {
				t.Fatal(err)
			}
			lens, queen := p.Routes["lens"].Model, p.Routes["queen"].Model
			if lens == queen {
				t.Fatalf("profile %s routes lens and queen to one model, %s", p.Name, lens)
			}
			useLocal(t)
			h.Profile, h.Provider, h.LensModel, h.QueenModel = p.Name, string(p.Provider), "", ""
			return setup{served: []string{lens}, want: strconv.Quote(queen)}
		}},
		{"the evaluator, when the case sets eval", both, func(t *testing.T, h *agentHarness, _ []bench.Item, _ int) setup {
			lens, queen, evaluator := "fake-lens", "fake-queen", "fake-evaluator"
			t.Setenv("HIVE_MODELS_PATH", userModels(t, fmt.Sprintf(`  bench-eval:
    quality: test
    provider: local
    roles:
      lens: {model: %s}
      followup: {model: %s}
      queen: {model: %s}
      evaluate: {model: %s}
`, lens, lens, queen, evaluator)))
			useLocal(t)
			setCase(t, h, func(c *harnessCase) { c.Eval = true })
			return setup{served: []string{lens, queen}, want: strconv.Quote(evaluator), cli: []string{"--profile", "bench-eval"}}
		}},
		{"a node the profile does not route", both, func(t *testing.T, h *agentHarness, _ []bench.Item, _ int) setup {
			// The profile routes only the lenses, and the solo control as
			// one, so Queen is sent where the run default sends her: to the
			// profile's provider, local, though --provider says openai.
			// OPENAI_API_KEY is empty, so nothing could reach OpenAI.
			lens := "fake-lens"
			t.Setenv("HIVE_MODELS_PATH", userModels(t, fmt.Sprintf(`  bench-lens-only:
    quality: test
    provider: local
    roles:
      lens: {model: %s}
`, lens)))
			useLocal(t)
			t.Setenv("OPENAI_API_KEY", "")
			return setup{served: []string{lens}, want: "(node queen", cli: []string{"--profile", "bench-lens-only", "--provider", "openai"}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HIVE_MODELS_PATH", filepath.Join(t.TempDir(), "no-user-models.yaml"))
			t.Setenv("HIVE_PROFILE", "")
			// Before benchHarness moves to the repository root, where the
			// roster's relative path no longer holds.
			items := liveItems(t, families, seeds)
			swarm, err := foragers.Filter(liveRoster(t), []string{"minimal"})
			if err != nil {
				t.Fatal(err)
			}
			lenses, _ := foragers.SplitByArchetype(swarm)
			model := &fakeModel{items: items, policy: oracle}
			h, results := benchHarness(t, chb, model, c.arms, families, seeds)
			s := c.setup(t, h, items, len(lenses))
			// Under the lock: the server's goroutines read it, and a request
			// from a chb process is no synchronization the race detector sees.
			model.mu.Lock()
			model.served = s.served
			model.mu.Unlock()
			runs := runsOf(items, c.arms, h.Reps)
			if s.cli == nil {
				err = h.run(context.Background())
			} else {
				args := append([]string{"agent-harness", "--suite", h.Suite, "--workspace", h.Workspace,
					"--budget-mode", h.BudgetMode, "--reps", strconv.Itoa(h.Reps), "--config", h.Config}, s.cli...)
				var out []byte
				out, err = exec.Command(chb, args...).CombinedOutput()
				t.Logf("chb agent-harness:\n%s", out)
			}
			if err == nil {
				t.Fatalf("the case passed with %s not served", s.want)
			}
			pre := caseChecks(t, h.Workspace)[endpointCheck]
			if pre.OK || !strings.Contains(pre.Detail, s.want) {
				t.Errorf("endpoint check %+v; want it failed, naming %s", pre, s.want)
			}
			for _, m := range s.served {
				if strings.Contains(pre.Detail, strconv.Quote(m)) {
					t.Errorf("endpoint check names %q, which the endpoint serves: %s", m, pre.Detail)
				}
			}
			for _, r := range runs {
				if ran(h, r) {
					t.Errorf("%s ran with %s not served", r, s.want)
				}
			}
			if _, err := os.Stat(results); err == nil {
				t.Error("results.jsonl was written")
			}
		})
	}
}

// A model that answers every item wrongly, and a server that refuses every
// Queen call while it still lists its models, are data: every run is
// recorded, the case is not stopped, and it passes, since the solo runs
// completed.
func wrongAnswersAreData(t *testing.T, chb string) {
	seeds, families, arms := []int64{1}, []string{"F1"}, []string{bench.ArmSwarm, bench.ArmSolo}
	items := liveItems(t, families, seeds)
	flip := map[string]string{bench.Support: bench.Oppose, bench.Oppose: bench.Support}
	wrong := func(it *bench.Item) (string, []string) { return flip[it.Want.Verdict], nil }
	model := &fakeModel{items: items, queenStatus: http.StatusBadRequest, policy: wrong}
	h, results := benchHarness(t, chb, model, arms, families, seeds)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("harness: %v", err)
	}
	rows := readResults(t, results)
	if len(rows) != len(items)*len(arms) {
		t.Fatalf("%d rows, want every run: %d", len(rows), len(items)*len(arms))
	}
	byID := map[string]*bench.Item{}
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	completed := 0
	for _, r := range rows {
		want, _ := wrong(byID[r.Item])
		switch r.Arm {
		case bench.ArmSolo:
			if !r.Completed || r.Got != want || r.Correct {
				t.Errorf("%s solo: completed %v, got %q, correct %v; want a completed run answering %q, graded wrong", r.Item, r.Completed, r.Got, r.Correct, want)
			}
			completed++
		case bench.ArmSwarm:
			if r.Completed || r.Error == "" {
				t.Errorf("%s swarm: completed %v, error %q; want a run Queen's refusal stopped", r.Item, r.Completed, r.Error)
			}
		}
	}
	done := caseChecks(t, h.Workspace)["a run completed"]
	if !done.OK || !strings.Contains(done.Name, fmt.Sprintf("(%d of %d)", completed, len(rows))) {
		t.Errorf("completion check %+v; want %d of %d runs completed", done, completed, len(rows))
	}
}

// A case none of whose runs completed fails, though the model answered
// every lens call right. The rows are written marked as not a measurement,
// and the check counts the runs by why: here every run's Queen, whom the
// server refused.
func noCompletedRun(t *testing.T, chb string) {
	seeds, families, arms := []int64{1}, []string{"F1"}, []string{bench.ArmSwarm}
	items := liveItems(t, families, seeds)
	model := &fakeModel{items: items, queenStatus: http.StatusBadRequest, policy: oracle}
	h, results := benchHarness(t, chb, model, arms, families, seeds)
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the case passed with no run completed")
	}
	why, rows := markedRows(t, results)
	if len(rows) != len(items) {
		t.Fatalf("%d rows, want every run: %d", len(rows), len(items))
	}
	for _, r := range rows {
		if r.Completed {
			t.Errorf("%s completed with every Queen refused", r.Item)
		}
	}
	done := caseChecks(t, h.Workspace)["a run completed"]
	if done.OK || !strings.Contains(done.Name, fmt.Sprintf("(0 of %d)", len(rows))) {
		t.Errorf("completion check %+v; want it failed with 0 of %d runs completed", done, len(rows))
	}
	if !strings.Contains(done.Detail, fmt.Sprintf("%d× ", len(rows))) || !strings.Contains(done.Detail, queenRefusal) {
		t.Errorf("completion detail %q does not count all %d runs under Queen's refusal, %q", done.Detail, len(rows), queenRefusal)
	}
	if !strings.Contains(why, done.Detail) {
		t.Errorf("results.jsonl marks its rows %q; want the completion check's why, %q", why, done.Detail)
	}
}
