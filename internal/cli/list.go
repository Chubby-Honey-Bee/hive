package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/spf13/cobra"
)

// newSwarmListCmd lists every forager the registry can find. Each
// forager renders with its sigil + accent-colored name when stdout is
// a TTY (or when --color=always / FORCE_COLOR is set); piped output
// stays plain ASCII so scripts and the behavior-spec replay keep
// working. Colors are suppressed entirely when NO_COLOR is set,
// honoring the https://no-color.org/ convention.
func newSwarmListCmd() *cobra.Command {
	var colorMode string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available foragers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listForagers(cmd.OutOrStdout(), colorMode)
		},
	}
	cmd.Flags().StringVar(&colorMode, "color", "auto",
		"colorize output: auto (TTY-detect) | always | never. Honors NO_COLOR and FORCE_COLOR env.")
	return cmd
}

// listForagers prints every forager, then the synthesizers, then usage,
// colored as the --color mode resolves.
func listForagers(out io.Writer, colorMode string) error {
	all, err := loadForagers()
	if err != nil {
		return err
	}
	l := &foragerLister{out: out, useColor: resolveColorMode(colorMode)}
	// ★ marks what `chb ask` actually dispatches. It marked the
	// `default: true` foragers, a set of ten that differs from the
	// balanced nine `chb ask` runs.
	if l.inBalanced, err = balancedForagerNames(all); err != nil {
		return err
	}
	l.print(splitSynthesizers(all))
	return nil
}

// balancedForagerNames is the set of the foragers in the balanced preset.
func balancedForagerNames(all []foragers.Forager) (map[string]bool, error) {
	balanced, err := foragers.Filter(all, []string{"balanced"})
	if err != nil {
		return nil, err
	}
	inBalanced := make(map[string]bool, len(balanced))
	for _, w := range balanced {
		inBalanced[w.Name] = true
	}
	return inBalanced, nil
}

// splitSynthesizers separates the synthesizers from the foragers. A
// synthesizer (the Queen) is not a forager: she holds no lens and writes
// the foragers' verdicts up as one. She is listed on her own line, outside
// the forager count.
func splitSynthesizers(all []foragers.Forager) (roster, synths []foragers.Forager) {
	for _, w := range all {
		if w.Archetype == foragers.ArchetypeSynthesizer {
			synths = append(synths, w)
		} else {
			roster = append(roster, w)
		}
	}
	return roster, synths
}

// foragerLister prints the forager listing.
type foragerLister struct {
	out        io.Writer
	useColor   bool
	inBalanced map[string]bool
}

// print prints the header, a row per forager, the synthesizers and the
// usage.
func (l *foragerLister) print(roster, synths []foragers.Forager) {
	fmt.Fprintf(l.out, "HIVE — %d foragers available (★ = the balanced preset `chb ask` dispatches)\n\n", len(roster))
	for _, w := range roster {
		l.row(w)
	}
	l.printSynthesizers(synths)
	fmt.Fprintln(l.out, "")
	fmt.Fprintln(l.out, "Usage:")
	fmt.Fprintln(l.out, "  chb ask \"<question>\"                # default 9 foragers")
	fmt.Fprintln(l.out, "  chb ask \"<q>\" --foragers default,steward")
	fmt.Fprintln(l.out, "  chb ask \"<q>\" --foragers all          # every deliberation-eligible forager")
}

// printSynthesizers prints a row per synthesizer under their own heading,
// when there are any.
func (l *foragerLister) printSynthesizers(synths []foragers.Forager) {
	if len(synths) == 0 {
		return
	}
	fmt.Fprintln(l.out, "")
	fmt.Fprintln(l.out, "Synthesizer (not a forager; writes the foragers' verdicts up as one):")
	for _, w := range synths {
		l.row(w)
	}
}

// row prints one forager: ★ when `chb ask` dispatches it, its sigil and
// name, and its description.
func (l *foragerLister) row(w foragers.Forager) {
	star := " "
	if l.inBalanced[w.Name] {
		star = "★"
	}
	name, sigil := l.colored(w)
	// Padding the colored name with %-15s would count escape
	// codes against the width. Print sigil + name first,
	// then pad the rest with the forager's plain-text width.
	pad := max(15-len(w.Name), 1)
	fmt.Fprintf(l.out, "  %s  %s %s%s  %s\n",
		star, sigil, name, strings.Repeat(" ", pad), w.Description)
}

// colored is the forager's name and sigil, in its accent color when color
// is on and it has one.
func (l *foragerLister) colored(w foragers.Forager) (name, sigil string) {
	if !l.useColor {
		return w.Name, w.Sigil
	}
	prefix := w.AnsiPrefix()
	if prefix == "" {
		return w.Name, w.Sigil
	}
	return prefix + w.Name + foragers.AnsiReset, prefix + w.Sigil + foragers.AnsiReset
}

// resolveColorMode applies the standard precedence for terminal-color
// gating. Highest priority: an explicit --color flag value. Below that:
// the NO_COLOR convention (https://no-color.org/) disables colors;
// FORCE_COLOR forces them. Final fallback: TTY auto-detection.
//
// This combination handles the awkward middle cases where ModeCharDevice
// returns false even though the destination *can* render ANSI — IDE
// integrated terminals, CI environments with TERM set, certain pty
// wrappers — without false-positives in real pipes (file >, | grep, …).
func resolveColorMode(flag string) bool {
	if on, explicit := explicitColorMode(flag); explicit {
		return on
	}
	return colorModeFromEnv()
}

// explicitColorMode reads a --color value that turns color on or off, and
// reports false for any other value.
func explicitColorMode(flag string) (on, explicit bool) {
	switch strings.ToLower(strings.TrimSpace(flag)) {
	case "always", "force", "yes", "true":
		return true, true
	case "never", "off", "no", "false":
		return false, true
	}
	return false, false
}

// colorModeFromEnv applies NO_COLOR, then FORCE_COLOR, then TTY
// auto-detection.
func colorModeFromEnv() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return stdoutIsTTY()
}

// stdoutIsTTY reports whether os.Stdout points at a real terminal.
// Used as the auto-detection fallback inside resolveColorMode.
// Falls back to "no color" on any error reading the file mode.
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
