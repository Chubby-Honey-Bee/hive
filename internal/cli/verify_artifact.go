package cli

import (
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/spf13/cobra"
)

// newSwarmVerifyArtifactCmd implements `chb verify-artifact <path>`.
//
// Reads the artifact at <path>, re-hashes its canonical encoding, and
// reports whether the stored SHA256 matches. Exit 0 on match; exit 1 on
// mismatch (corruption, tampering, or schema drift) or IO/decode error.
//
//	chb verify-artifact /tmp/swarm-run-abc123.json
func newSwarmVerifyArtifactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify-artifact <path>",
		Short: "Verify a swarm artifact's SHA256 hash (tamper detection)",
		Long: `verify-artifact re-reads the artifact at <path>, checks that its bytes
are the canonical encoding (sorted keys, no timestamps) of what they decode
to, re-hashes the bytes with the sha256 value emptied, and compares against
the stored sha256 field. A changed byte anywhere fails, whitespace included.

Exit 0  — hash matches; artifact is intact.
Exit 1  — hash mismatch (file corrupted or tampered) or decode error.

Use this after transferring an artifact between machines, committing it
to a repo, or to confirm two deterministic runs produced the same content.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			matched, expected, actual, err := artifact.VerifyArtifact(path)
			if err != nil {
				return fmt.Errorf("verify-artifact: %w", err)
			}
			if matched {
				fmt.Fprintf(cmd.OutOrStdout(), "OK  %s\n    sha256=%s\n", path, expected)
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"MISMATCH  %s\n  stored:     %s\n  recomputed: %s\n",
				path, expected, actual,
			)
			// Return a non-nil error so cobra exits with code 1.
			return fmt.Errorf("artifact hash mismatch: stored=%s recomputed=%s", expected, actual)
		},
	}
	return cmd
}
