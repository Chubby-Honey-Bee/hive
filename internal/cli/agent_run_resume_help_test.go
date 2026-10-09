package cli

import (
	"strings"
	"testing"
)

// pflag takes the first back-quoted word of a usage string as the value's
// placeholder. The --resume help names its value run_id, and names chb
// workflow resume without back quotes.
func TestAgentRunResumeHelp_PlaceholderIsTheRunID(t *testing.T) {
	var line string
	for _, l := range strings.Split(newAgentRunCmd().Flags().FlagUsages(), "\n") {
		if strings.Contains(l, "--resume ") {
			line = strings.TrimSpace(l)
		}
	}
	if !strings.HasPrefix(line, "--resume run_id ") {
		t.Errorf("help line = %q, want it to start with \"--resume run_id \"", line)
	}
	if !strings.Contains(line, "chb workflow resume") {
		t.Errorf("help line = %q, want it to name chb workflow resume too", line)
	}
}
