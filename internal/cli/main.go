// Package cli is HIVE's command line: the cobra command tree behind chb, and
// Main, which runs it. cmd/chb is a thin main that calls Main, so
// `go install .../cmd/chb@latest` names the binary chb (docs/naming.md
// rule 5).
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	dbPath  string
	// dbPinned says --db named dbPath for this one command. A hive command
	// leaves a pinned database that hosts another project's hive refused,
	// not for the project's workspace database (hive.Resolve).
	dbPinned bool
	store    *db.Store
)

// Main runs chb with os.Args and exits the process on failure. v is the
// version the binary reports, which the build injects into its main.
func Main(v string) {
	version = v
	useragent.Version = version
	root := &cobra.Command{
		Use:               "chb",
		Short:             "HIVE (chb) — multi-agent reasoning with an MSS integrity gate",
		Version:           version,
		PersistentPreRunE: openCommandStore,
		PersistentPostRun: closeCommandStore,
	}

	root.PersistentFlags().StringVar(&dbPath, "db", "", "database path (default: $HIVE_DB_PATH or workspace/hive.db)")

	// The data layer: the database, outcomes and calibration.
	root.AddCommand(
		newDBInitCmd(),
		newDBWriteCmd(),
		newDBReadCmd(),
		newOutcomeRecordCmd(),
		newOutcomeImportCmd(),
		newCalibrateCmd(),
		newCalibrationExportCmd(),
		newCalibrationMergeCmd(),
	)

	// Autonomous mode, and the wave's merge, conflicts and gate.
	root.AddCommand(
		newHiveCmd(),
		newSwarmMergeCmd(),
		newDetectConflictsCmd(),
		newGuardCmd(),
	)

	// Projects, agents, sources, ingestion, graph export and the workflow engine.
	root.AddCommand(
		newInitCmd(),
		newCheckAgentsCmd(),
		newValidateSourcesCmd(),
		newIngestCmd(),
		newIngestFindingsCmd(),
		newExportGraphCmd(),
		newWorkflowCmd(),
	)

	// The Lean 4 bridge.
	root.AddCommand(
		newLean4ExtractCmd(),
	)

	// The unattended runner and the commands that read its runs.
	root.AddCommand(
		newAgentRunCmd(),
		newPreflightCmd(),
		newExtractFindingsCmd(),
		newRenderReviewCmd(),
		newRunTotalsCmd(),
		newGenImplementWorkflowCmd(),
		newModelsCmd(), // unified models / pricing / tiers config
	)

	// Forager-swarm subcommands — promoted directly to root (a `swarm`
	// parent group would just be a wasted dimension in the CLI's
	// coordinate space). WASP-correct: every dimension earns its keep.
	root.AddCommand(
		newSwarmAskCmd(),
		newSwarmListCmd(),
		newSwarmGenerateCmd(),
		newSwarmPaletteCmd(),
		newSwarmGapsCmd(),
		newSwarmRecallCmd(),
		newRipenCmd(),
		newSwarmVerifyArtifactCmd(),
		newSwarmReplicateCmd(),
		newSwarmValidatePersonasCmd(),
	)

	// Comb is the hive's shared belief surface. The Dreamer forager's
	// ripening loop is reached via `chb ripen`.
	root.AddCommand(
		newCombCmd(),
		newDBRepairCmd(), // SQLite recovery for corrupted workspaces
		newReviewCmd(),
		newImplementCmd(),
		newProofCmd(),
		newGenBehaviorCmd(),
		newReplayBehaviorCmd(),
		newMCPSmokeCmd(),
		newVerifyCitationsCmd(), // open-access DOI verification (Unpaywall)
		newValidateCmd(),
		newAgentHarnessCmd(),   // prompts + personas through the local claude CLI, contract-checked
		newBenchCmd(),          // the pre-registered decision rule over bench results
		newDesignCmd(),         // the executability benchmark's report and rule
		newWASPScanReportCmd(), // WASP runtime scan-detector evidence log
		newCDESuggestAxisCmd(), // CDE axis-candidate suggester (framer evidence)
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// openCommandStore runs before every command. It resolves the database path
// and, unless the verb keeps away from the database, opens the store the
// command uses.
func openCommandStore(cmd *cobra.Command, _ []string) error {
	// Arguments and flags are validated before this hook, so a usage mistake
	// still prints usage. An error from the command itself — a refused
	// write, a hash mismatch — prints alone, not buried under the usage
	// text.
	cmd.SilenceUsage = true
	if storeFreePath(strings.Fields(cmd.CommandPath())[1:]) {
		return nil
	}
	resolved := resolveCommandDBPath()
	if err := ensureDBParentDir(resolved); err != nil {
		return err
	}
	// A verb that inspects or repairs the raw file opens its own
	// recovery connection and must not have the schema created
	// underneath it — a --dry-run that writes to the database it was
	// asked only to look at is not a dry run.
	if storeUntouched(cmd.Name()) {
		return nil
	}
	return openSharedStore(resolved)
}

// closeCommandStore runs after every command and closes the store
// openCommandStore opened, if it opened one.
func closeCommandStore(*cobra.Command, []string) {
	if store != nil {
		store.Close()
	}
}

// resolveCommandDBPath settles dbPath for the command: --db, else
// $HIVE_DB_PATH, else workspace/hive.db. It records in dbPinned
// whether --db named it.
func resolveCommandDBPath() string {
	resolved := dbPath
	dbPinned = resolved != ""
	if resolved == "" {
		resolved = os.Getenv("HIVE_DB_PATH")
	}
	if resolved == "" {
		resolved = filepath.Join("workspace", "hive.db")
	}
	dbPath = resolved
	return resolved
}

// ensureDBParentDir creates the directory that holds the database file.
// Standalone binaries get extracted to a directory that has no
// `workspace/` yet — SQLite then fails to create the file with the cryptic
// "unable to open database file (14)". Creating the parent on demand lets a
// fresh user just work.
func ensureDBParentDir(path string) error {
	parent := filepath.Dir(path)
	if parent == "" || parent == "." {
		return nil
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create db parent dir %q: %w", parent, err)
	}
	return nil
}

// openSharedStore opens the store at path as the package's store. db-init
// creates the file, so the store is opened regardless. Init() then ensures
// every table exists. It is CREATE TABLE IF NOT EXISTS and nothing else —
// there are no migrations — so it is idempotent and cheap on fresh and
// existing databases alike.
func openSharedStore(path string) error {
	var err error
	store, err = db.NewStore(path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err := store.Init(); err != nil {
		return fmt.Errorf("init database schema: %w", err)
	}
	return nil
}

// storeFree are the commands that never read or write the default database,
// by their path under chb; a group named here covers every command under it.
// They read files only, or run on a database of their own: one under their
// --workspace, a temporary one, or chb init's project database. A path, not a
// name, since names repeat: hive report reads the database and design report
// does not.
var storeFree = map[string]bool{
	"help": true, "completion": true, cobra.ShellCompRequestCmd: true, cobra.ShellCompNoDescRequestCmd: true,
	"agent-harness": true, "bench": true, "calibration-merge": true, "design": true,
	"gen-behavior": true, "gen-implement-workflow": true, "generate": true,
	"hive write-synthesis": true, "implement": true, "init": true, "list": true,
	"mcp-smoke": true, "models": true, "palette": true, "preflight": true, "proof": true,
	"render-review": true, "replay-behavior": true, "replicate": true, "review": true,
	"validate": true, "validate-personas": true, "verify-artifact": true,
	"workflow list": true, "workflow validate": true,
}

// storeFreePath reports whether the command words name, the words after chb,
// or a group it is under, is storeFree, so nothing is opened for it.
func storeFreePath(words []string) bool {
	for i := range words {
		if storeFree[strings.Join(words[:i+1], " ")] {
			return true
		}
	}
	return false
}

// storeUntouched names the verbs that open the database themselves and must
// see it exactly as it is on disk: the schema is not created before they run.
func storeUntouched(name string) bool {
	return name == "db-repair"
}
