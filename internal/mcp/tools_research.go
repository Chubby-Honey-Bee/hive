package mcp

// chb_research — start a multi-wave research run, in this process.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// researchRun runs a chb_research workflow: runner.Run, indirected so a
// test can stand in a run that panics.
var researchRun = runner.Run

func researchSpec() map[string]any {
	return map[string]any{
		"name":        "chb_research",
		"title":       "Start a research run",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
		"description": "Start a multi-wave research-deep run on a topic in the server process. Returns the project, the workflow and the workspace database it writes; poll chb_status with the project to follow progress.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"topic":    map[string]any{"type": "string", "description": "Research question or goal"},
				"project":  map[string]any{"type": "string", "description": "Workspace name (derived from topic if omitted)"},
				"workflow": map[string]any{"type": "string", "description": "Name of a workflow in workflows/ (without .yaml), e.g. research-deep (the default), hive or research-quick"},
			},
			"required": []string{"topic"},
		},
	}
}

// ─── chb_research ────────────────────────────────────────────

func (s *mcpServer) handleResearch(req rpcRequest, args map[string]any) {
	r, err := s.admitResearch(args)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.startResearch(req, r)
}

// researchRequest is a chb_research call the server admitted: its topic,
// the project it writes, the workflow it runs and the budget mode it runs
// under.
type researchRequest struct {
	topic, project, workflow string
	budget                   runner.BudgetMode
}

// admitResearch validates a chb_research call (researchArgs) and admits its
// run. The session's budget mode reaches this run as it reaches every run a
// tool starts, so the research-deep workflow, whose every node names a tier,
// runs at the mode set. A research run calls a paid model like every other
// spawning tool, and one run per project does not bound it: each topic is
// its own project, so the run takes a place under the spawn rate limit too.
func (s *mcpServer) admitResearch(args map[string]any) (researchRequest, error) {
	r, err := s.researchArgs(args)
	if err != nil {
		return r, err
	}
	if r.budget, err = runner.ParseBudgetMode(s.activeBudgetMode()); err != nil {
		return r, err
	}
	if ok, why := s.limiter().allow(time.Now()); !ok {
		return r, errors.New(why)
	}
	return r, nil
}

// researchArgs reads a chb_research call: the server needs a store, the call
// a topic and a workflow by plain name, a name under workflows/ and nothing
// else: joined unchecked, "../../x" would run any *.yaml on disk.
func (s *mcpServer) researchArgs(args map[string]any) (researchRequest, error) {
	if s.store == nil {
		return researchRequest{}, errors.New("HIVE_DB_PATH is not set")
	}
	topic := stringArg(args, "topic")
	if topic == "" {
		return researchRequest{}, errors.New("topic is required")
	}
	r := researchRequest{
		topic:    topic,
		project:  sanitizeProject(topic, args["project"]),
		workflow: cmp.Or(stringArg(args, "workflow"), "research-deep"),
	}
	if !plainWorkflowName(r.workflow) {
		return researchRequest{}, errors.New("workflow must be a plain file name under workflows/ (no path separators or '..')")
	}
	return r, nil
}

// plainWorkflowName reports whether a workflow name is a plain file name:
// no path separators, and no leading dot.
func plainWorkflowName(name string) bool {
	return name == filepath.Base(name) && !strings.HasPrefix(name, ".")
}

// startResearch starts an admitted research run on its own goroutine and
// answers with what it started. The workflow is the one under workflows/
// beside the working directory or the binary, else the copy the binary
// carries (workflow.md § Workflow file). The run reads the file, so its
// cleanup waits for the run.
func (s *mcpServer) startResearch(req rpcRequest, r researchRequest) {
	wfFile, _, wfCleanup, err := workflow.ResolveFile(filepath.Join("workflows", r.workflow+".yaml"))
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	cfg := s.researchConfig(r, wfFile)
	ctx, cancel := context.WithCancel(context.Background())
	release := func() {
		cancel()
		wfCleanup()
	}
	if err := s.registerResearch(r.project, cancel); err != nil {
		release()
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	go s.runResearch(ctx, r.project, cfg, release)
	s.writeJSONToolResult(req.ID, map[string]any{
		"project":  r.project,
		"workflow": r.workflow,
		"db_path":  s.store.Path,
		"status":   "started",
	})
}

// researchConfig is the research run's config.
func (s *mcpServer) researchConfig(r researchRequest, wfFile string) runner.Config {
	return runner.Config{
		WorkflowYAML:  wfFile,
		ProjectName:   r.project,
		Inputs:        map[string]any{"topic": r.topic},
		MaxIterations: 30,
		BudgetMode:    r.budget,
		Log:           os.Stderr,
		// The run is in-process, so a command node's `chb` would otherwise
		// run this binary, which is not chb.
		ChbPath: s.chbBin(),
	}
}

// registerResearch registers a project's run, by its cancel, for the
// shutdown to cancel and wait for. No run starts once the shutdown has
// begun: the shutdown cancels the runs registered before it, waits for them,
// then closes the store. The check and the registration hold drainMu, which
// the shutdown takes to begin. One run per project: a second chb_research on
// the same topic is refused, since its cancel func would replace the first
// run's, leaving the first impossible to cancel at shutdown while both wrote
// the same project.
func (s *mcpServer) registerResearch(project string, cancel context.CancelFunc) error {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	if s.draining {
		return errors.New("the server is shutting down; no research run starts")
	}
	if _, running := s.runs.LoadOrStore(project, cancel); running {
		return fmt.Errorf(
			"a research run for project %q is already running; wait for it, or pass a different `project`", project)
	}
	s.runsWG.Add(1)
	return nil
}

// runResearch runs a registered research run to its end, then releases
// it: its context and workflow file, and its registration. The run's id
// is kept once its row exists, so a run that panics can be marked failed.
func (s *mcpServer) runResearch(ctx context.Context, project string, cfg runner.Config, release func()) {
	var runID int64
	cfg.RunStarted = func(id int64) { runID = id }
	defer func() {
		s.failPanickedResearch(project, runID, recover())
		release()
		s.runs.Delete(project)
		s.runsWG.Done()
	}()
	if _, err := researchRun(ctx, s.store, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "[chb-mcp] research %s: %v\n", project, err)
	}
}

// failPanickedResearch handles what recover returned in a research run,
// nil for no panic. A panic fails the run, not the server: it is logged,
// and the run, once its row exists, is marked failed.
func (s *mcpServer) failPanickedResearch(project string, runID int64, rec any) {
	if rec == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[chb-mcp] research %s: panic: %v\n%s\n", project, rec, debug.Stack())
	if runID <= 0 {
		return
	}
	if err := s.store.Workflows().MarkRunFailed(runID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		fmt.Fprintf(os.Stderr, "[chb-mcp] research %s: mark run %d failed: %v\n", project, runID, err)
	}
}
