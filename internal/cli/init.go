package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// ─── chb init (project-init.py) ──────────────────────────────────

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init <project-name>",
		Short: "Initialize a new HIVE project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return initProject(args[0])
		},
	}
	return cmd
}

// initProject creates workspace/<projectName> and a database in it, unless
// the project exists, and prints how to use it.
func initProject(projectName string) error {
	projectDir := filepath.Join("workspace", projectName)
	dbFile := filepath.Join(projectDir, "hive.db")

	if _, err := os.Stat(dbFile); err == nil {
		fmt.Printf("Project '%s' already exists at %s\n", projectName, projectDir)
		fmt.Printf("To use it: %s\n", dbPathEnvHint(dbFile))
		return nil
	}
	if err := createProjectDB(projectDir, dbFile); err != nil {
		return err
	}

	fmt.Printf("\nProject '%s' initialized at %s\n", projectName, projectDir)
	fmt.Printf("\nTo use this project, set the environment variable:\n")
	fmt.Printf("  %s\n", dbPathEnvHint(dbFile))
	return nil
}

// createProjectDB creates the project directory, and the database file in
// it with every table.
func createProjectDB(projectDir, dbFile string) error {
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return fmt.Errorf("create project dir: %w", err)
	}

	// Open a new store at the project path and init schema
	projStore, err := db.NewStore(dbFile)
	if err != nil {
		return err
	}
	defer projStore.Close()
	return projStore.Init()
}

// dbPathEnvHint is the command that points HIVE_DB_PATH at dbFile, in
// the platform's own shell, with the path made absolute so it holds from any
// directory.
func dbPathEnvHint(dbFile string) string {
	if abs, err := filepath.Abs(dbFile); err == nil {
		dbFile = abs
	}
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`setx HIVE_DB_PATH "%s"   (applies to shells opened afterwards)`, dbFile)
	}
	return fmt.Sprintf("export HIVE_DB_PATH=%q", dbFile)
}
