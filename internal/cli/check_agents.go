package cli

import (
	"fmt"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// ─── chb check-agents (check-agents.py) ─────────────────────

func newCheckAgentsCmd() *cobra.Command {
	var wave int
	cmd := &cobra.Command{
		Use:   "check-agents",
		Short: "Report agent completion status for a wave",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckAgents(wave)
		},
	}
	cmd.Flags().IntVar(&wave, "wave", 0, "wave number to check")
	cmd.MarkFlagRequired("wave")
	return cmd
}

// runCheckAgents reports the wave's agent runs by status. A read that fails
// is the command's error, not a wave with no runs.
func runCheckAgents(wave int) error {
	rows, err := db.QueryToMaps(store.ReadDB, "SELECT * FROM agent_runs WHERE wave = ? ORDER BY started_at", wave)
	if err != nil {
		return fmt.Errorf("read agent runs: %w", err)
	}
	if len(rows) == 0 {
		fmt.Printf("No agent runs found for wave %d.\n", wave)
		return nil
	}
	printWaveAgents(wave, rows)
	return nil
}

// printWaveAgents prints the wave's runs: the counts by status, each
// status's runs, the stale ones, and whether every agent completed.
func printWaveAgents(wave int, rows []map[string]any) {
	g := groupWaveAgents(rows, time.Now().UTC())

	fmt.Printf("=== Agent Status: Wave %d ===\n\n", wave)
	fmt.Printf("Total runs:  %d\n", len(rows))
	fmt.Printf("  Running:   %d\n", len(g.running))
	fmt.Printf("  Completed: %d\n", len(g.completed))
	fmt.Printf("  Failed:    %d\n", len(g.failed))

	printCompletedWaveAgents(g.completed)
	printRunningWaveAgents(g.running)
	printFailedWaveAgents(g.failed)
	printStaleWaveAgents(g.stale)

	if len(g.running) == 0 && len(g.failed) == 0 {
		fmt.Println("\nAll agents completed successfully.")
	}
}

// waveAgentGroups are a wave's agent runs by status; stale are the running
// ones that started more than ten minutes ago.
type waveAgentGroups struct {
	running, completed, failed, stale []map[string]any
}

// groupWaveAgents sorts the runs by status, in their order.
func groupWaveAgents(rows []map[string]any, now time.Time) waveAgentGroups {
	var g waveAgentGroups
	for _, run := range rows {
		g.add(run, now)
	}
	return g
}

// add files one run under its status; a run of any other status is left
// out.
func (g *waveAgentGroups) add(run map[string]any, now time.Time) {
	status, _ := run["status"].(string)
	switch status {
	case "completed":
		g.completed = append(g.completed, run)
	case "failed":
		g.failed = append(g.failed, run)
	case "running":
		g.addRunning(run, now)
	}
}

// addRunning files a running run, and again as stale when it is.
func (g *waveAgentGroups) addRunning(run map[string]any, now time.Time) {
	g.running = append(g.running, run)
	if waveAgentStale(run, now) {
		g.stale = append(g.stale, run)
	}
}

// waveAgentStale reports whether a run's started_at is more than ten minutes
// before now. started_at is a TIMESTAMP column, which the driver returns as
// a time.Time.
func waveAgentStale(run map[string]any, now time.Time) bool {
	started, ok := run["started_at"].(time.Time)
	return ok && now.Sub(started) > 10*time.Minute
}

// printCompletedWaveAgents lists the completed runs, nothing when there are
// none.
func printCompletedWaveAgents(runs []map[string]any) {
	if len(runs) == 0 {
		return
	}
	fmt.Println("\n--- Completed Agents ---")
	for _, run := range runs {
		fmt.Printf("  %-20s  %-12s  duration=%s  tokens=%s\n",
			anyStr(run["agent_name"]), strDefault(anyStr(run["agent_type"]), "n/a"),
			waveAgentField(run, "duration_ms", "%vms"), waveAgentField(run, "total_tokens", "%v tokens"))
	}
}

// waveAgentField formats run[key] with format, or is "n/a" when the run has
// no value there.
func waveAgentField(run map[string]any, key, format string) string {
	v := run[key]
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf(format, v)
}

// printRunningWaveAgents lists the running runs, nothing when there are
// none.
func printRunningWaveAgents(runs []map[string]any) {
	if len(runs) == 0 {
		return
	}
	fmt.Println("\n--- Running Agents ---")
	for _, run := range runs {
		fmt.Printf("  %-20s  %-12s  started=%v\n",
			anyStr(run["agent_name"]), anyStr(run["agent_type"]), run["started_at"])
	}
}

// printFailedWaveAgents lists the failed runs with the head of each one's
// summary, nothing when there are none.
func printFailedWaveAgents(runs []map[string]any) {
	if len(runs) == 0 {
		return
	}
	fmt.Println("\n--- Failed Agents ---")
	for _, run := range runs {
		fmt.Printf("  %-20s  %-12s  reason: %s\n",
			anyStr(run["agent_name"]), anyStr(run["agent_type"]), failedWaveAgentReason(run))
	}
}

// failedWaveAgentReason is the first 80 bytes of a failed run's summary, or
// "no summary".
func failedWaveAgentReason(run map[string]any) string {
	return strDefault(clip(anyStr(run["summary"]), 80), "no summary")
}

// printStaleWaveAgents warns of the stale runs, nothing when there are none.
func printStaleWaveAgents(runs []map[string]any) {
	if len(runs) == 0 {
		return
	}
	fmt.Printf("\n!!! STALE AGENTS (running > 10 min) !!!\n")
	for _, run := range runs {
		fmt.Printf("  %-20s  started=%v\n", anyStr(run["agent_name"]), run["started_at"])
	}
}
