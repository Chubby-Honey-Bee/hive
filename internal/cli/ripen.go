package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"

	"github.com/Chubby-Honey-Bee/hive/internal/dreamer"
	"github.com/spf13/cobra"
)

// newRipenCmd returns `chb ripen`: the Dreamer forager's five-pass
// consolidation loop against the live Comb.
//
// Defaults are recommend-only: prune emits stop_signal pairs; settle
// emits alarm signals. --apply makes settle demote laundered guarantees
// to assumptions. Prune stays recommend-only in every mode — it never
// merges findings (the `merged` counter in its notes is always 0).
func newRipenCmd() *cobra.Command {
	var o ripenOptions
	cmd := &cobra.Command{
		Use:   "ripen",
		Short: "Run the dreamer's ripening loop against the comb",
		Long: `Ripening is what the dreamer does to the comb between
sessions: prune redundant assumptions, reprove guarantees whose deps
have shifted, contradict claims that newer evidence falsifies,
hypothesize follow-ups for long-open gaps, settle guarantees whose
foundations have flipped to unknown.

Five deterministic passes operate on the existing findings/conflicts/
gaps tables and emit bee-colony signals. The loop:

  - QMP halt when MSS laundering is detected (exits non-zero).
  - Per-pass deadline 5 minutes.
  - One ripen_log row per pass.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run()
		},
	}
	cmd.Flags().IntVar(&o.maxPasses, "max-passes", 5, "stop after N total passes")
	cmd.Flags().StringVar(&o.passList, "passes", "", "comma-separated subset (e.g. 'prune,settle')")
	cmd.Flags().BoolVar(&o.applyChanges, "apply", false, "write changes: settle demotes laundered guarantees (prune is recommend-only in every mode); default recommend-only")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "execute every pass without writing to the DB")
	cmd.Flags().IntVar(&o.gapAgeDays, "gap-age-days", 7, "hypothesize threshold (gap age in days)")
	cmd.Flags().Float64Var(&o.jaccardMin, "jaccard-min", 0.7, "prune threshold for redundancy detection")
	return cmd
}

// ripenOptions holds the flags of chb ripen.
type ripenOptions struct {
	maxPasses    int
	passList     string
	applyChanges bool
	dryRun       bool
	gapAgeDays   int
	jaccardMin   float64
}

// run runs the ripening loop inside its own tick and prints the summary.
func (o *ripenOptions) run() error {
	// The loop turns a MaxPasses of 0 into its default of 5, so a 0
	// here is refused rather than silently run as 5. --gap-age-days 0
	// and --jaccard-min 0 are passed through and honoured.
	if o.maxPasses < 1 {
		return fmt.Errorf("--max-passes must be at least 1, got %d", o.maxPasses)
	}
	if err := store.Init(); err != nil {
		return err
	}
	endTick := beginRipenTick()
	defer endTick()

	lo := o.loopOptions()
	res, err := dreamer.Run(context.Background(), store, lo)
	renderRipenSummary(res, lo)
	return ripenOutcome(res, err)
}

// beginRipenTick opens this invocation's ripen tick and returns the func
// that ends it, which does nothing when the tick did not open.
//
// Ripening is a tick: the Dreamer's consolidation is exactly the kind of
// interval "what did the comb look like before/after the last ripening?"
// wants to address.
//
// The label carries a random suffix so each invocation gets its own tick:
// with the timestamp alone, a second ripen in the same second would get the
// first one's closed tick back from Begin and run with no ripen tick open.
func beginRipenTick() func() {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	tickID, err := store.TimeWheel().Begin(
		fmt.Sprintf("ripen-%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(suffix)),
		db.TickRipen, 0, 0, "dreamer loop",
	)
	if err != nil {
		return func() {}
	}
	return func() { _ = store.TimeWheel().End(tickID) }
}

// loopOptions are the ripening loop's options as the flags set them.
func (o *ripenOptions) loopOptions() dreamer.LoopOptions {
	return dreamer.LoopOptions{
		MaxPasses:  o.maxPasses,
		OnlyPasses: ripenPassList(o.passList),
		Pass: dreamer.Options{
			Apply:        o.applyChanges,
			DryRun:       o.dryRun,
			Now:          time.Now(),
			JaccardMin:   o.jaccardMin,
			GapAgeMin:    time.Duration(o.gapAgeDays) * 24 * time.Hour,
			MaxPerPass:   200,
			PassDeadline: 5 * time.Minute,
		},
	}
}

// ripenPassList is the --passes subset as pass names, nil when it is empty
// and every pass runs.
func ripenPassList(passList string) []string {
	if passList == "" {
		return nil
	}
	passes := strings.Split(passList, ",")
	for i, p := range passes {
		passes[i] = strings.TrimSpace(p)
	}
	return passes
}

// ripenOutcome fails a loop that errored, or that halted on MSS laundering.
func ripenOutcome(res *dreamer.LoopResult, err error) error {
	if errors.Is(err, dreamer.ErrHalted) {
		return fmt.Errorf("ripen halted: %s", res.HaltReason)
	}
	return err
}

func renderRipenSummary(res *dreamer.LoopResult, lo dreamer.LoopOptions) {
	if res == nil {
		return
	}
	printRipenTotals(res, lo)
	for _, p := range res.Passes {
		printRipenPass(p)
	}
}

// printRipenTotals prints the loop's line: the passes that ran, the rows
// they touched and their cost, with its mode and whether it halted.
func printRipenTotals(res *dreamer.LoopResult, lo dreamer.LoopOptions) {
	fmt.Printf("ripen: %d passes ran (touched=%d cost=$%.4f)",
		len(res.Passes), res.TotalTouched, float64(res.TotalCostX10K)/10000.0)
	if lo.Pass.DryRun {
		fmt.Print(" [dry-run]")
	}
	if lo.Pass.Apply {
		fmt.Print(" [apply]")
	}
	if res.Halted {
		fmt.Printf(" [HALTED: %s]", res.HaltReason)
	}
	fmt.Println()
}

// printRipenPass prints one pass's line: its status, rows touched, cost and
// duration, then its notes and its error.
func printRipenPass(p dreamer.PassOutcome) {
	dur := p.CompletedAt.Sub(p.StartedAt).Round(time.Millisecond)
	fmt.Printf("  %-12s status=%-9s touched=%-3d cost=$%.4f dur=%s",
		p.Name, p.Status, p.Touched, float64(p.CostX10K)/10000.0, dur)
	for _, k := range []string{"candidates", "emitted", "demoted", "stale_gaps", "guarantees_scanned"} {
		if v, ok := p.Notes[k]; ok {
			fmt.Printf(" %s=%v", k, v)
		}
	}
	if p.HaltOnError != nil {
		fmt.Printf(" err=%q", p.HaltOnError)
	}
	fmt.Println()
}
