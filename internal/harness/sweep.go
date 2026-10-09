package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// What the bench and design cases share: the routing their runs resolve
// under, the model preflight before the first run, and the sweep over the
// runs with its outage honesty. A run that reached no model, or after which
// the endpoint fails the preflight it passed before the first run, stops
// the case, and the rows are then written as not a measurement.

// harnessUnknownArm is the first of arms that is none of known.
func harnessUnknownArm(arms []string, known ...string) (string, bool) {
	for _, a := range arms {
		if !slices.Contains(known, a) {
			return a, true
		}
	}
	return "", false
}

// harnessRouting is the config a case's runs resolve under, as their
// agent-run will: the budget mode, --budget-mode else HIVE_BUDGET_MODE,
// and under a profile the profile's provider. It also returns the profile,
// nil when there is none.
func (h *agentHarness) harnessRouting() (runner.Config, *runner.Profile, error) {
	bm, err := runner.ResolveBudgetMode(h.BudgetMode)
	if err != nil {
		return runner.Config{}, nil, err
	}
	cfg := runner.Config{Provider: h.Provider, BudgetMode: bm}
	if h.Profile == "" {
		return cfg, nil, nil
	}
	profile, err := runner.ResolveProfile(h.Profile)
	if err != nil {
		return cfg, nil, err
	}
	cfg.Provider = string(profile.Provider)
	return cfg, profile, nil
}

// routeHarnessWorkflows loads each workflow text, routed by profile when
// there is one, as its agent-run will.
func routeHarnessWorkflows(texts []string, profile *runner.Profile) ([]map[string]any, error) {
	var defns []map[string]any
	for _, text := range texts {
		defn, err := routeHarnessWorkflow(text, profile)
		if err != nil {
			return nil, err
		}
		defns = append(defns, defn)
	}
	return defns, nil
}

// routeHarnessWorkflow loads one workflow text, routed by profile when
// there is one.
func routeHarnessWorkflow(text string, profile *runner.Profile) (map[string]any, error) {
	if profile != nil {
		var err error
		if text, err = runner.ApplyProfile(text, profile); err != nil {
			return nil, err
		}
	}
	return workflow.LoadYAMLString(text)
}

// preflightEndpoint asks the endpoint, before the first run, whether it
// serves every model the case's workflows send; false when it does not.
// An endpoint that is down, or does not serve a model a run sends, fails
// every run at its model preflight: that is an outage, not a result.
func (h *agentHarness) preflightEndpoint(ctx context.Context, cfg runner.Config, defns []map[string]any, check func(string, bool, string)) bool {
	served, err := checkEndpoints(ctx, cfg, defns)
	switch {
	case err != nil:
		check("the endpoint serves every model the case sends (model preflight)", false, endpointCause(err))
		return false
	case served == "":
		h.logf("  model preflight: nothing asked; it asks only the endpoint at OPENAI_BASE_URL, when a node sends to it and OPENAI_API_KEY is set, and the local provider's, when a node sends to it")
	default:
		check("the endpoint serves every model the case sends (model preflight)", true, served)
	}
	return true
}

// resetHarnessWorkspace empties the case's workspace. Only a case that
// will run clears it, so an endpoint that is down does not cost the
// results of an earlier run into it.
func resetHarnessWorkspace(ws string, check func(string, bool, string)) bool {
	_ = os.RemoveAll(ws)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		check("workspace", false, err.Error())
		return false
	}
	return true
}

// harnessSweep tracks a case's runs: how many there are to run, how many
// ran and completed, which changed their tree, why the others did not
// complete, and why the case stopped, when it did.
type harnessSweep struct {
	cfg      runner.Config
	defns    []map[string]any
	fallback string // the cause of a run that did not complete and says nothing

	total, runs, completed int
	changed                []string
	causes                 map[string]int
	why                    []string // the causes in the order first seen
	stopped                string
}

// newHarnessSweep starts a sweep of total runs against the endpoint the
// workflows defns send to under cfg.
func newHarnessSweep(total int, cfg runner.Config, defns []map[string]any, fallback string) harnessSweep {
	return harnessSweep{cfg: cfg, defns: defns, fallback: fallback, total: total, causes: map[string]int{}, why: []string{}}
}

// count tallies one run: whether it completed, and, when it changed its
// tree, its name.
func (s *harnessSweep) count(completed, treeOK bool, name string) {
	s.runs++
	if completed {
		s.completed++
	}
	if !treeOK {
		s.changed = append(s.changed, name)
	}
}

// stopsAt tallies why a run did not complete and says whether that stops
// the case (harnessOutage), recording why when it does. where names the
// run in the message.
func (s *harnessSweep) stopsAt(ctx context.Context, reached bool, failed, runErr, where string) bool {
	s.noteIncomplete(failed, runErr)
	which, cause, stop := harnessOutage(ctx, s.cfg, s.defns, reached, runErr)
	if stop {
		s.stopped = fmt.Sprintf("stopped at run %d of %d (%s), %s: %s", s.runs, s.total, where, which, cause)
	}
	return stop
}

// noteIncomplete counts a run that did not complete under its cause: the
// error of its first failed node, else the last line of its error, else
// the sweep's fallback.
func (s *harnessSweep) noteIncomplete(failed, runErr string) {
	cause := firstLine(failed)
	if cause == "" {
		cause = lastLine(runErr)
	}
	if cause == "" {
		cause = s.fallback
	}
	if s.causes[cause] == 0 {
		s.why = append(s.why, cause)
	}
	s.causes[cause]++
}

// whyCounts counts the runs that did not complete by cause, in the order
// first seen.
func (s *harnessSweep) whyCounts() []string {
	out := make([]string, len(s.why))
	for i, cause := range s.why {
		out[i] = fmt.Sprintf("%d× %s", s.causes[cause], cause)
	}
	return out
}

// checkMeasured fails the case when it stopped, and says why its rows
// measured nothing the configuration can be judged on: it stopped, or no
// run completed; "" when they did. Such rows are kept to read, under a line
// that makes the reader refuse them.
func (s *harnessSweep) checkMeasured(check func(string, bool, string)) string {
	if s.stopped != "" {
		check("every run reached a model, and the endpoint stayed up", false, s.stopped)
		return s.stopped
	}
	if s.completed == 0 {
		return "no run completed: " + strings.Join(s.whyCounts(), "; ")
	}
	return ""
}

// finishSweep logs how the runs ended and checks that one completed;
// false when the case stopped. Whatever stopped them, runs none of which
// completed measured nothing the case can pass on; the detail counts them
// by cause.
func (h *agentHarness) finishSweep(s *harnessSweep, check func(string, bool, string)) bool {
	if s.stopped != "" {
		h.logf("  stopped after %d of %d runs; results.jsonl marks them as not a measurement", s.runs, s.total)
		return false
	}
	h.logf("  completed %d/%d runs; see the report for scores", s.completed, s.runs)
	detail := ""
	if s.completed == 0 {
		detail = strings.Join(s.whyCounts(), "; ")
	}
	check(fmt.Sprintf("a run completed (%d of %d)", s.completed, s.runs), s.completed > 0, detail)
	return true
}

// harnessOutage says whether a run that did not complete stops the case,
// and how to say so. A run that did not complete is data, unless it
// reached no model or the endpoint now fails the preflight it passed
// before the first run. A run that left no run row was refused at its
// setup, its model preflight among it, before any model call. An endpoint
// that fails the preflight now is down or lost a model, and every run
// after this one would fail the same way.
func harnessOutage(ctx context.Context, cfg runner.Config, defns []map[string]any, reached bool, runErr string) (which, cause string, stop bool) {
	_, perr := checkEndpoints(ctx, cfg, defns)
	if reached && perr == nil {
		return "", "", false
	}
	which = "which did not complete"
	if !reached {
		which = "which reached no model"
	}
	return which, harnessOutageCause(lastLine(runErr), perr), true
}

// harnessOutageCause is why the case stopped: the run's own error, or,
// when the endpoint fails the preflight, the preflight's error first, since
// the console shows only the start, then the run's own.
func harnessOutageCause(own string, perr error) string {
	if perr == nil {
		return orDash(own)
	}
	cause := endpointCause(perr) + harnessOwnErrorNote(own, perr)
	// No answer at all: the server may be down because of this
	// configuration, not beside it.
	if errors.As(perr, new(*url.Error)) {
		cause += ". If this configuration took the server down (out of memory, say), that is a finding: read the server's log before you rerun it"
	}
	return cause
}

// harnessOwnErrorNote adds the run's own error to the preflight's, unless
// it is empty or already says it.
func harnessOwnErrorNote(own string, perr error) string {
	if own == "" || strings.Contains(own, perr.Error()) {
		return ""
	}
	return "; the run's own error: " + own
}

// writeHarnessResults writes the results file at path with write.
func writeHarnessResults(path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkHarnessResults checks that results.jsonl was written: as a
// measurement, or marked as not one, which refuser refuses.
func checkHarnessResults(check func(string, bool, string), err error, notMeasured, refuser string) {
	if notMeasured != "" {
		check("results.jsonl written, marked as not a measurement, which "+refuser+" refuses", err == nil, errString(err))
		return
	}
	check("results written to results.jsonl", err == nil, errString(err))
}
