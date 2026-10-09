package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/spf13/cobra"
)

// `chb ask`'s help names the preset it runs by default, balanced, and its
// size, read from the shipped roster, so a roster change that makes the
// number stale fails here.
func TestAskHelpNamesItsDefaultPreset(t *testing.T) {
	all, err := foragers.Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	size := len(foragers.Balanced(all))
	words := map[int]string{7: "seven", 8: "eight", 9: "nine", 10: "ten", 11: "eleven"}
	if words[size] == "" {
		t.Fatalf("the balanced preset has %d foragers; add the word for %d here", size, size)
	}
	ask := newSwarmAskCmd()
	if def := ask.Flags().Lookup("foragers").DefValue; def != "[balanced]" {
		t.Fatalf("ask --foragers defaults to %s, want [balanced]", def)
	}
	if want := "the balanced " + words[size]; !strings.Contains(ask.Short, want) {
		t.Errorf("ask help %q does not name its default, %q", ask.Short, want)
	}
}

// Lowercase `hive` names autonomous mode (`chb hive`), so the forager-swarm
// commands never call a swarm run the hive, in help, in flag help or in `chb
// list`'s usage lines, and `chb review` does not call its self-reviewer
// agents foragers.
func TestSwarmCommandHelpSaysSwarm(t *testing.T) {
	hive := regexp.MustCompile(`\bhive\b`)
	for _, cmd := range []*cobra.Command{
		newSwarmAskCmd(), newSwarmGenerateCmd(), newSwarmListCmd(), newSwarmRecallCmd(),
		newSwarmReplicateCmd(), newSwarmVerifyArtifactCmd(),
	} {
		if hive.MatchString(cmd.Short) || hive.MatchString(cmd.Long) {
			t.Errorf("chb %s help calls a swarm the hive: %q", cmd.Name(), cmd.Short)
		}
		for _, line := range strings.Split(cmd.Flags().FlagUsages(), "\n") {
			if hive.MatchString(line) {
				t.Errorf("chb %s flag help calls a swarm the hive: %q", cmd.Name(), strings.TrimSpace(line))
			}
		}
	}
	if review := newReviewCmd(); strings.Contains(review.Short, "forager") {
		t.Errorf("chb review help %q calls its self-reviewer agents foragers", review.Short)
	}

	// These personas' descriptions name no hive, so a hive in the listing
	// is the command's own text.
	dir := t.TempDir()
	for _, p := range []struct{ name, archetype string }{{"alpha", "lens"}, {"queen", "synthesizer"}} {
		body := fmt.Sprintf("---\nname: %s\narchetype: %s\ndescription: the %s persona\n---\nbody\n", p.name, p.archetype, p.name)
		if err := os.WriteFile(filepath.Join(dir, p.name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HIVE_FORAGERS_DIR", dir)
	var out bytes.Buffer
	list := newSwarmListCmd()
	list.SetOut(&out)
	list.SetArgs([]string{"--color", "never"})
	if err := list.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if hive.MatchString(line) {
			t.Errorf("chb list calls a swarm the hive: %q", line)
		}
	}
}

// pflag prints the first `backticked` span of a flag's help as the flag's
// value name and drops the backticks, so a backticked span in flag help must
// be a bare value name, such as `run_id`, never a command.
func TestFlagHelpBackticksOnlyNameAValue(t *testing.T) {
	flagName := regexp.MustCompile(`(?m)^\s*(?:-\w, )?--([\w-]+)`)
	quoted := regexp.MustCompile("`([^`]*)`")
	valueName := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, cmd := range []*cobra.Command{
		newSwarmAskCmd(), newSwarmGenerateCmd(), newAgentRunCmd(), newCombEmbedCmd(),
	} {
		names := flagName.FindAllStringSubmatch(cmd.Flags().FlagUsages(), -1)
		if len(names) == 0 {
			t.Fatalf("chb %s lists no flags", cmd.Name())
		}
		for _, m := range names {
			usage := cmd.Flags().Lookup(m[1]).Usage
			for _, q := range quoted.FindAllStringSubmatch(usage, -1) {
				if !valueName.MatchString(q[1]) {
					t.Errorf("chb %s --%s help backticks %q, which --help prints as the flag's value name", cmd.Name(), m[1], q[1])
				}
			}
		}
	}
}
