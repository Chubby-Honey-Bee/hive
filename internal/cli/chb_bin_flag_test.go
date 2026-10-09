package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// --chb-bin names the chb binary that validate and mcp-smoke drive, beside
// --mcp-bin, and their usage lists it.
func TestChbBinFlag(t *testing.T) {
	for name, build := range map[string]func() *cobra.Command{
		"validate":  newValidateCmd,
		"mcp-smoke": newMCPSmokeCmd,
	} {
		t.Run(name, func(t *testing.T) {
			cmd := build()
			const want = "/opt/chb-bin/chb"
			if err := cmd.ParseFlags([]string{"--chb-bin", want}); err != nil {
				t.Fatal(err)
			}
			if got := cmd.Flags().Lookup("chb-bin").Value.String(); got != want {
				t.Errorf("--chb-bin %s set the chb binary to %q, want %q", want, got, want)
			}
			if usage := cmd.UsageString(); !strings.Contains(usage, "--chb-bin") {
				t.Errorf("usage does not list --chb-bin:\n%s", usage)
			}
		})
	}
}

// CI uploads the validate log from the default workspace, so the path in
// ci.yml is the one chb validate writes the log to.
func TestCIUploadsTheValidateLogFromTheDefaultWorkspace(t *testing.T) {
	ci, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := "path: " + selfValidationWorkspace + "/self-validate.log"
	if !strings.Contains(string(ci), want) {
		t.Errorf("ci.yml does not upload %q", want)
	}
}
