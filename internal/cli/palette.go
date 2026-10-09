package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/spf13/cobra"
)

// newSwarmPaletteCmd writes the swarm's current sigil + accent
// theme as JSON. Default output target is foragers/palette.json so
// non-Go consumers (the HTML page, future visualizers, downstream
// tools) can read the palette without parsing every persona.
func newSwarmPaletteCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "palette",
		Short: "Export the forager sigil + accent palette as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return exportForagerPalette(cmd.OutOrStdout(), cmd.ErrOrStderr(), out)
		},
	}
	cmd.Flags().StringVar(&out, "out", "-", "output path; use '-' for stdout")
	return cmd
}

// exportForagerPalette encodes every forager's sigil and accent as JSON and
// writes it to out.
func exportForagerPalette(stdout, stderr io.Writer, out string) error {
	all, err := loadForagers()
	if err != nil {
		return err
	}
	data, err := foragers.PaletteJSON(all)
	if err != nil {
		return fmt.Errorf("encode palette: %w", err)
	}
	return writeForagerPalette(stdout, stderr, out, data, len(all))
}

// writeForagerPalette writes the palette of count foragers to the file out,
// or to stdout when out is "-" or empty.
func writeForagerPalette(stdout, stderr io.Writer, out string, data []byte, count int) error {
	if out == "-" || out == "" {
		_, err := stdout.Write(append(data, '\n'))
		return err
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Wrote %d foragers to %s\n", count, out)
	return nil
}
