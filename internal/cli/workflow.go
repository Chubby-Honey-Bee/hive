package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Graph-based workflow engine",
	}

	cmd.AddCommand(
		newWorkflowValidateCmd(),
		newWorkflowListCmd(),
		newWorkflowInitCmd(),
		newWorkflowNextCmd(),
		newWorkflowCompleteCmd(),
		newWorkflowFailCmd(),
		newWorkflowStatusCmd(),
		newWorkflowRunsCmd(),
		newWorkflowResumeCmd(),
	)
	return cmd
}

func newWorkflowValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file.yaml>",
		Short: "Validate a workflow definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return validateWorkflowFile(args[0])
		},
	}
}

// validateWorkflowFile validates the workflow name names, on disk or else
// the copy the binary carries, and prints VALID or INVALID with every error
// and warning. An invalid workflow exits 1.
func validateWorkflowFile(name string) error {
	wfPath, _, cleanup, err := workflow.ResolveFile(name)
	if err != nil {
		return err
	}
	defer cleanup()
	errs, warnings, err := workflow.Validate(wfPath)
	if err != nil {
		return err
	}

	if len(errs) > 0 {
		printWorkflowInvalid(errs, warnings)
		os.Exit(1)
	}

	fmt.Println("VALID")
	printWorkflowWarnings(warnings)
	return nil
}

// printWorkflowInvalid prints INVALID with every error and warning.
func printWorkflowInvalid(errs, warnings []string) {
	fmt.Println("INVALID")
	for _, e := range errs {
		fmt.Printf("  ERROR: %s\n", e)
	}
	printWorkflowWarnings(warnings)
}

// printWorkflowWarnings prints every warning.
func printWorkflowWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Printf("  WARNING: %s\n", w)
	}
}

func newWorkflowListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available workflow definitions",
		RunE: func(cmd *cobra.Command, args []string) error {
			listWorkflows()
			return nil
		},
	}
}

// listWorkflows lists the workflow definitions under workflows/.
func listWorkflows() {
	wfs, err := workflow.ListWorkflows("workflows")
	if err != nil {
		fmt.Println("No workflows directory found")
		return
	}
	if len(wfs) == 0 {
		fmt.Println("No workflow definitions found")
		return
	}
	fmt.Printf("Available workflows (%d):\n\n", len(wfs))
	for _, wf := range wfs {
		printWorkflowEntry(wf)
	}
}

// printWorkflowEntry prints one workflow definition, or the error reading
// it.
func printWorkflowEntry(wf map[string]any) {
	if e, ok := wf["error"]; ok {
		fmt.Printf("  %s: ERROR — %s\n", wf["file"], e)
		return
	}
	fmt.Printf("  %s (v%v)\n", wf["name"], wf["version"])
	fmt.Printf("    Nodes: %v, File: %s\n\n", wf["nodes"], wf["file"])
}

func newWorkflowInitCmd() *cobra.Command {
	var inputs string

	cmd := &cobra.Command{
		Use:   "init <file.yaml>",
		Short: "Initialize a workflow run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputMap, err := parseWorkflowInputs(inputs)
			if err != nil {
				return err
			}
			return initWorkflowRun(args[0], inputMap)
		},
	}
	cmd.Flags().StringVar(&inputs, "inputs", "", "JSON object of input parameters")
	return cmd
}

// parseWorkflowInputs reads --inputs, a JSON object; empty is an empty
// object.
func parseWorkflowInputs(inputs string) (map[string]any, error) {
	if inputs == "" {
		return map[string]any{}, nil
	}
	var inputMap map[string]any
	if err := json.Unmarshal([]byte(inputs), &inputMap); err != nil {
		return nil, fmt.Errorf("invalid inputs JSON: %w", err)
	}
	return inputMap, nil
}

// initWorkflowRun starts a run of the workflow name names, on disk or else
// the copy the binary carries.
func initWorkflowRun(name string, inputMap map[string]any) error {
	wfPath, _, cleanup, err := workflow.ResolveFile(name)
	if err != nil {
		return err
	}
	defer cleanup()

	runID, err := workflow.InitWorkflow(store.Workflows(), wfPath, inputMap)
	if err != nil {
		return err
	}
	fmt.Printf("Workflow run initialized: ID=%d\n", runID)
	fmt.Printf("Next: chb workflow next %d\n", runID)
	return nil
}

func newWorkflowNextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "next <run_id>",
		Short: "Get next dispatchable nodes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var runID int64
			fmt.Sscanf(args[0], "%d", &runID)

			dispatch, err := workflow.GetNextNodesManual(store.Workflows(), runID)
			if err != nil {
				return err
			}
			if len(dispatch) == 0 {
				fmt.Println("No nodes ready — workflow may be complete or waiting")
				return nil
			}
			b, _ := json.MarshalIndent(dispatch, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

func newWorkflowCompleteCmd() *cobra.Command {
	var outputs string

	cmd := &cobra.Command{
		Use:   "complete <run_id> <node>",
		Short: "Mark a node as completed with outputs",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var runID int64
			fmt.Sscanf(args[0], "%d", &runID)
			nodeName := args[1]

			var outputMap map[string]any
			if outputs != "" {
				if err := json.Unmarshal([]byte(outputs), &outputMap); err != nil {
					return fmt.Errorf("invalid outputs JSON: %w", err)
				}
			} else {
				outputMap = map[string]any{}
			}

			if err := workflow.CompleteNode(store.Workflows(), runID, nodeName, outputMap); err != nil {
				return err
			}
			fmt.Printf("Node %q completed. State updated with: %v\n", nodeName, outputMap)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputs, "outputs", "", "JSON object of output values")
	return cmd
}

func newWorkflowFailCmd() *cobra.Command {
	var errMsg string

	cmd := &cobra.Command{
		Use:   "fail <run_id> <node>",
		Short: "Mark a node as failed",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var runID int64
			fmt.Sscanf(args[0], "%d", &runID)
			nodeName := args[1]

			if errMsg == "" {
				errMsg = "Unknown error"
			}

			return workflow.FailNode(store.Workflows(), runID, nodeName, errMsg)
		},
	}
	cmd.Flags().StringVar(&errMsg, "error", "", "error description")
	return cmd
}

// newWorkflowResumeCmd is `chb workflow resume`: the operator's answer to a
// `human_review` node that paused the run (workflow.ResumeHumanReview).
func newWorkflowResumeCmd() *cobra.Command {
	var feedback string
	cmd := &cobra.Command{
		Use:   "resume <run_id> <approve|reject|redirect>",
		Short: "Answer a human_review checkpoint and resume a paused run",
		Long: `Completes the node a paused run is waiting on and returns the run to
running, so the next ` + "`chb agent-run --resume`" + ` (or the dispatcher still
watching it) carries on.

  approve    the work stands — the node completes and the run continues
  reject     the node fails; the run follows its failure path
  redirect   the node completes and --feedback lands in the run state as
             {human_feedback}, so downstream prompts can read the new direction`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, decision, err := parseResumeArgs(args)
			if err != nil {
				return err
			}
			if err := workflow.ResumeHumanReview(store.Workflows(), runID, decision, feedback); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run %d resumed (%s)\n", runID, decision)
			return nil
		},
	}
	cmd.Flags().StringVar(&feedback, "feedback", "", "note carried into run state as {human_feedback}")
	return cmd
}

// parseResumeArgs reads resume's arguments: the run id, a positive integer,
// and the decision, approve, reject or redirect.
func parseResumeArgs(args []string) (int64, string, error) {
	var runID int64
	if _, err := fmt.Sscanf(args[0], "%d", &runID); err != nil || runID <= 0 {
		return 0, "", fmt.Errorf("run_id must be a positive integer, got %q", args[0])
	}
	switch decision := args[1]; decision {
	case "approve", "reject", "redirect":
		return runID, decision, nil
	}
	return 0, "", fmt.Errorf("decision must be approve, reject or redirect, got %q", args[1])
}

func newWorkflowStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <run_id>",
		Short: "Show workflow run status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var runID int64
			fmt.Sscanf(args[0], "%d", &runID)
			return printWorkflowStatus(runID)
		},
	}
}

// printWorkflowStatus prints the run and the state of each of its nodes. It
// reads both before it prints, so a read that fails prints nothing.
func printWorkflowStatus(runID int64) error {
	rows, err := db.QueryToMaps(store.ReadDB, "SELECT * FROM workflow_runs WHERE id=?", runID)
	if err != nil {
		return fmt.Errorf("read workflow run %d: %w", runID, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("no workflow run %d", runID)
	}
	nodeRows, err := db.QueryToMaps(store.ReadDB,
		"SELECT node_name, node_type, status, error FROM workflow_node_states WHERE run_id=? ORDER BY id", runID)
	if err != nil {
		return fmt.Errorf("read the nodes of workflow run %d: %w", runID, err)
	}
	printWorkflowRun(rows[0])
	printWorkflowNodes(nodeRows)
	return nil
}

// printWorkflowNodes lists each node's line under "Nodes:".
func printWorkflowNodes(nodeRows []map[string]any) {
	fmt.Println("Nodes:")
	for _, ns := range nodeRows {
		fmt.Println(workflowNodeLine(ns))
	}
}

// printWorkflowRun prints a run's name, id, status and times.
func printWorkflowRun(run map[string]any) {
	fmt.Printf("Workflow: %v v%v\n", run["workflow_name"], run["workflow_version"])
	fmt.Printf("Run ID: %v\n", run["id"])
	fmt.Printf("Status: %v\n", run["status"])
	fmt.Printf("Started: %v\n", run["started_at"])
	if run["completed_at"] != nil {
		fmt.Printf("Completed: %v\n", run["completed_at"])
	}
	fmt.Println()
}

// workflowNodeIcons marks each node status in the status listing.
var workflowNodeIcons = map[string]string{
	"pending": "  ", "running": ">>", "completed": "OK",
	"failed": "XX", "skipped": "--", "waiting_human": "??",
}

// workflowNodeLine is one node's line in the status listing.
func workflowNodeLine(ns map[string]any) string {
	status := anyStr(ns["status"])
	icon := workflowNodeIcons[status]
	if icon == "" {
		icon = "??"
	}
	line := fmt.Sprintf("  [%s] %s (%s) — %s", icon, anyStr(ns["node_name"]), anyStr(ns["node_type"]), status)
	if e := anyStr(ns["error"]); e != "" {
		line += " — " + e
	}
	return line
}

func newWorkflowRunsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "runs",
		Short: "List all workflow runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printWorkflowRuns()
		},
	}
}

// printWorkflowRuns lists every workflow run, newest first. A read that
// fails is the command's error, not an empty list.
func printWorkflowRuns() error {
	rows, err := db.QueryToMaps(store.ReadDB,
		"SELECT id, workflow_name, workflow_version, status, started_at, completed_at FROM workflow_runs ORDER BY id DESC")
	if err != nil {
		return fmt.Errorf("read workflow runs: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("No workflow runs found")
		return nil
	}
	fmt.Printf("Workflow runs (%d):\n\n", len(rows))
	for _, r := range rows {
		fmt.Println(workflowRunLine(r))
	}
	return nil
}

// workflowRunLine is one run's line in the runs listing.
func workflowRunLine(r map[string]any) string {
	line := fmt.Sprintf("  #%v %v v%v — %v  (started: %v)",
		r["id"], r["workflow_name"], r["workflow_version"], r["status"], r["started_at"])
	if r["completed_at"] != nil {
		line += fmt.Sprintf("  (completed: %v)", r["completed_at"])
	}
	return line
}
