package cli

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// ── db-read ──────────────────────────────────────────────────

func newDBReadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db-read <command> [json]",
		Short: "Query the CDE-encoded database",
	}

	cmd.AddCommand(
		newReadSummaryCmd(),
		newReadProbeCmd(),
		newReadDimensionsCmd(),
		newReadGapsCmd(),
		newReadConflictsCmd(),
		newReadMSSAuditCmd(),
		newReadByLabelCmd("assumptions", "assumption"),
		newReadByLabelCmd("guarantees", "guarantee"),
		newReadByLabelCmd("definitions", "definition"),
		newReadByLabelCmd("unknowns", "unknown"),
		newReadCalibrationCmd(),
	)

	return cmd
}

func newReadSummaryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "summary",
		Short: "Summary: CDE coverage + MSS integrity at a glance",
		RunE: func(cmd *cobra.Command, args []string) error {
			sum, err := store.GetSummary()
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(sum, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

// probeInt reads a probe filter that must be a whole number. JSON numbers
// arrive as float64; a string or a fraction is the caller's mistake, and
// answering it with a dropped filter would turn a coordinate lookup into a
// full scan reported as a success.
func probeInt(key string, v any) (int, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("%s must be a number, got %v", key, v)
	}
	if f != float64(int(f)) {
		return 0, fmt.Errorf("%s must be a whole number, got %v", key, v)
	}
	return int(f), nil
}

func newReadProbeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "probe <json>",
		Short: "CDE coordinate lookup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dbReadProbe(args[0])
		},
	}
}

// dbReadProbe runs the coordinate lookup a probe payload asks for.
func dbReadProbe(payload string) error {
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	f, err := parseProbeFilters(raw)
	if err != nil {
		return err
	}
	results, err := store.Findings().Probe(f.coords, f.wave, f.mssLabel, f.convergenceLevel, f.limit)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println(string(b))
	return nil
}

// probeCoordKeys are the coordinate keys a probe payload may hold.
var probeCoordKeys = []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}

// probeFilters is what a probe payload asks for: the coordinates, and the
// filters beyond them that it sets.
type probeFilters struct {
	coords                     map[string]any
	wave, limit                *int
	mssLabel, convergenceLevel *string
}

// parseProbeFilters reads every key of a probe payload: the d1..d8
// coordinates and the four filters Probe takes beyond them. Any other key is
// refused, so `probe '{"d1":0,"wave":2}'` answers for wave 2 alone.
func parseProbeFilters(raw map[string]any) (probeFilters, error) {
	f := probeFilters{coords: make(map[string]any)}
	for k, v := range raw {
		if err := f.set(k, v); err != nil {
			return f, err
		}
	}
	return f, nil
}

// set reads one key of a probe payload.
func (f *probeFilters) set(k string, v any) error {
	switch k {
	case "wave":
		return f.setWave(v)
	case "limit":
		return f.setLimit(v)
	case "mss_label", "convergence_level":
		return f.setLabelFilter(k, v)
	}
	return f.setCoord(k, v)
}

// setCoord reads a d1..d8 coordinate, refusing any other key as unknown.
func (f *probeFilters) setCoord(k string, v any) error {
	if !slices.Contains(probeCoordKeys, k) {
		return fmt.Errorf("unknown probe key %q: expected d1..d8, wave, mss_label, convergence_level or limit", k)
	}
	n, err := probeInt(k, v)
	if err != nil {
		return err
	}
	f.coords[k] = n
	return nil
}

// setWave reads the wave filter.
func (f *probeFilters) setWave(v any) error {
	n, err := probeInt("wave", v)
	if err != nil {
		return err
	}
	f.wave = &n
	return nil
}

// setLimit reads the limit, which must be positive.
func (f *probeFilters) setLimit(v any) error {
	n, err := probeInt("limit", v)
	if err != nil {
		return err
	}
	if n <= 0 {
		return fmt.Errorf("limit must be a positive integer, got %d", n)
	}
	f.limit = &n
	return nil
}

// setLabelFilter reads the mss_label or convergence_level filter, which must
// be a string.
func (f *probeFilters) setLabelFilter(k string, v any) error {
	sv, ok := v.(string)
	if !ok {
		return fmt.Errorf("%s must be a string, got %v", k, v)
	}
	if k == "mss_label" {
		f.mssLabel = &sv
	} else {
		f.convergenceLevel = &sv
	}
	return nil
}

func newReadDimensionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dimensions",
		Short: "List registered CDE dimensions",
		RunE: func(cmd *cobra.Command, args []string) error {
			dims, err := store.Dimensions().GetDimensions()
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(dims, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

func newReadGapsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gaps [json]",
		Short: "Query knowledge gaps",
		RunE: func(cmd *cobra.Command, args []string) error {
			gaps, err := store.Gaps().QueryGaps(readGapsPriority(args), true, nil, nil, nil, nil)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(gaps, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

// readGapsPriority is the priority the optional JSON argument names; nil
// with no argument, an argument that does not parse, or no priority in it.
func readGapsPriority(args []string) *string {
	if len(args) == 0 {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(args[0]), &raw); err != nil {
		return nil
	}
	if p, ok := raw["priority"].(string); ok {
		return &p
	}
	return nil
}

func newReadConflictsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "conflicts",
		Short: "Query conflicts",
		RunE: func(cmd *cobra.Command, args []string) error {
			conflicts, err := store.Conflicts().QueryConflicts(true)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(conflicts, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

func newReadMSSAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mss_audit",
		Short: "MSS integrity check",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := store.MSSAudit()
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

func newReadByLabelCmd(name, label string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: fmt.Sprintf("List %s findings, at most %d", label, db.DefaultProbeLimit),
		RunE: func(cmd *cobra.Command, args []string) error {
			results, err := store.Findings().GetByMSSLabel(label)
			if err != nil {
				return err
			}
			// A full page cannot be told apart from an exhausted label, so
			// say so. Stderr, so the JSON on stdout stays clean.
			if len(results) == db.DefaultProbeLimit {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: listed %d %s findings, the most one read returns; there may be more. Narrow with `chb db-read probe '{\"mss_label\":%q,\"wave\":N}'`.\n",
					db.DefaultProbeLimit, label, label)
			}
			b, _ := json.MarshalIndent(results, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}
