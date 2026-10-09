package cli

import (
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/spf13/cobra"
)

// buildAgentRunCmdForTest returns a freshly-constructed agent-run cobra command
// so we can test flag parsing in isolation without a real store or workflow.
func buildAgentRunCmdForTest() *cobra.Command {
	return newAgentRunCmd()
}

// TestAgentRunFlag_Deterministic verifies --deterministic is registered and
// sets the flag to true.
func TestAgentRunFlag_Deterministic(t *testing.T) {
	cmd := buildAgentRunCmdForTest()
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return nil }
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"/dev/null", "--deterministic"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	f := cmd.Flags().Lookup("deterministic")
	if f == nil {
		t.Fatal("--deterministic flag not registered on agent-run command")
	}
	if f.Value.String() != "true" {
		t.Errorf("--deterministic value = %q after --deterministic; want true", f.Value.String())
	}
}

// TestAgentRunFlag_SeedImpliesDeterministic verifies --seed is registered and
// that the Changed("seed") method correctly reports it was set.
func TestAgentRunFlag_SeedImpliesDeterministic(t *testing.T) {
	cmd := buildAgentRunCmdForTest()
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return nil }
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"/dev/null", "--seed", "42"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	seedF := cmd.Flags().Lookup("seed")
	if seedF == nil {
		t.Fatal("--seed flag not registered on agent-run command")
	}
	if !cmd.Flags().Changed("seed") {
		t.Error("Flags().Changed(\"seed\") should be true after --seed 42")
	}
	if seedF.Value.String() != "42" {
		t.Errorf("--seed value = %q; want 42", seedF.Value.String())
	}
}

// TestAgentRunFlag_SeedZeroDistinguishable verifies that passing --seed 0 is
// distinguishable from not passing --seed at all (Changed() semantics).
func TestAgentRunFlag_SeedZeroDistinguishable(t *testing.T) {
	cmdNoSeed := buildAgentRunCmdForTest()
	cmdNoSeed.RunE = func(cmd *cobra.Command, args []string) error { return nil }
	cmdNoSeed.SilenceErrors = true
	cmdNoSeed.SilenceUsage = true
	cmdNoSeed.SetArgs([]string{"/dev/null"})
	if err := cmdNoSeed.Execute(); err != nil {
		t.Fatalf("Execute (no seed): %v", err)
	}
	if cmdNoSeed.Flags().Changed("seed") {
		t.Error("Changed(\"seed\") should be false when --seed not passed")
	}

	cmdWithSeed := buildAgentRunCmdForTest()
	cmdWithSeed.RunE = func(cmd *cobra.Command, args []string) error { return nil }
	cmdWithSeed.SilenceErrors = true
	cmdWithSeed.SilenceUsage = true
	cmdWithSeed.SetArgs([]string{"/dev/null", "--seed", "0"})
	if err := cmdWithSeed.Execute(); err != nil {
		t.Fatalf("Execute (seed=0): %v", err)
	}
	if !cmdWithSeed.Flags().Changed("seed") {
		t.Error("Changed(\"seed\") should be true when --seed 0 is passed explicitly")
	}
}

// TestAgentRunFlag_DeterministicOnly verifies --deterministic alone does NOT
// set --seed (Changed stays false).
func TestAgentRunFlag_DeterministicOnly(t *testing.T) {
	cmd := buildAgentRunCmdForTest()
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return nil }
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"/dev/null", "--deterministic"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if cmd.Flags().Changed("seed") {
		t.Error("Changed(\"seed\") should be false when only --deterministic is passed")
	}
	f := cmd.Flags().Lookup("deterministic")
	if f == nil || f.Value.String() != "true" {
		t.Errorf("--deterministic should be true, got %v", f)
	}
}

// TestRunnerConfig_DeterministicFields is a lightweight unit test confirming
// that runner.Config accepts the new fields without compilation issues.
func TestRunnerConfig_DeterministicFields(t *testing.T) {
	seed := int64(12345)
	cfg := runner.Config{
		Deterministic: true,
		Seed:          &seed,
	}
	if !cfg.Deterministic {
		t.Error("Deterministic should be true")
	}
	if cfg.Seed == nil || *cfg.Seed != 12345 {
		t.Errorf("Seed = %v; want &12345", cfg.Seed)
	}

	// Verify nil seed is the default (no seed set).
	cfg2 := runner.Config{Deterministic: true}
	if cfg2.Seed != nil {
		t.Errorf("Seed should be nil when not set; got %v", cfg2.Seed)
	}
}
