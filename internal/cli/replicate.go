package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/spf13/cobra"
)

// replicaResult holds the result of one swarm replica run.
type replicaResult struct {
	Index        int    `json:"index"`
	ArtifactPath string `json:"artifact_path"`
	Hash         string `json:"hash"`
	Valid        bool   `json:"valid"`
}

// vantageReplica is one replica's data for a given vantage key.
type vantageReplica struct {
	Narrative     string `json:"narrative"`
	Confidence    int    `json:"confidence"`
	DominantLabel string `json:"dominant_label"`
}

// vantageDiv is the divergence classification for a single vantage across replicas.
type vantageDiv struct {
	ConfidenceRange    int    `json:"confidence_range"`
	DistinctNarratives int    `json:"distinct_narratives"`
	LabelConsensus     string `json:"label_consensus"` // "unanimous" | "majority" | "split"
}

// vantageEntry is the per-vantage meta-diff output.
type vantageEntry struct {
	Key      string           `json:"key"`
	Replicas []vantageReplica `json:"replicas"`
	Div      vantageDiv       `json:"divergence"`
}

// replicateSummary is the aggregate across all vantages.
type replicateSummary struct {
	Convergent         int `json:"convergent"`
	SensitivitySurface int `json:"sensitivity_surface"`
	Contradicting      int `json:"contradicting"`
}

// replicateOutput is the full meta-diff JSON written to --out.
type replicateOutput struct {
	Question string           `json:"question"`
	N        int              `json:"n"`
	Vary     string           `json:"vary"`
	Seed     *int64           `json:"seed,omitempty"`
	Replicas []replicaResult  `json:"replicas"`
	Vantages []vantageEntry   `json:"vantages,omitempty"`
	Summary  replicateSummary `json:"summary"`
}

// newSwarmReplicateCmd implements `chb replicate "<question>"`.
//
// Runs N independent swarm instances, each with its own database (no shared
// Comb) and the caller's agents/ personas, each producing a deterministic
// artifact (internal/artifact), then meta-diffs the artifacts hash-first.
//
//	chb replicate "should we ship the plushie?" --n 3 --vary none --seed 12345
//	chb replicate "should we ship?" --n 3 --vary stochastic
func newSwarmReplicateCmd() *cobra.Command {
	var o replicateOptions

	cmd := &cobra.Command{
		Use:   "replicate <question>",
		Short: "Run N independent swarms and meta-diff their deterministic artifacts",
		Long: `replicate runs N independent swarms, each with its own database in a
temp dir (no shared Comb). Like agent-run, each runs from the working directory,
so personas resolve from its agents/. Each instance writes the canonical
artifact chb ask --artifact writes. The artifacts are then compared
hash-first.

With --vary none --seed N, all N artifacts should hash-match — mismatched output
flags a determinism regression in the comb-write ordering or model-alias pinning.

With --vary stochastic, divergence measures sampling-noise sensitivity; expect
non-zero divergence as proof the harness detects what determinism is buying.

--vary phrasing and --vary model are deferred to v2.

Examples:
  chb replicate "is the bond race fixed?" --n 2 --vary none --seed 12345
  chb replicate "is the bond race fixed?" --n 3 --vary stochastic`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd, strings.Join(args, " "))
		},
	}

	cmd.Flags().IntVar(&o.n, "n", 3, "number of independent swarm replicas to run (keep small to avoid runaway costs; default 3)")
	cmd.Flags().StringVar(&o.vary, "vary", "none", "variation axis: none|stochastic|phrasing|model (phrasing and model are v2)")
	cmd.Flags().StringSliceVar(&o.foragerList, "foragers", []string{"default"}, "forager list ('default' = the 10 default:true foragers, 'all' = every forager)")
	cmd.Flags().StringVar(&o.outPath, "out", "", "meta-diff JSON output path (default: ./replicate-<vary>-<unix-timestamp>.json)")
	cmd.Flags().Int64Var(&o.seedVal, "seed", 0, "seed for --vary none (required; implies --deterministic for each replica)")
	cmd.Flags().Float64Var(&o.maxCostUSD, "max-cost-usd", 0, "per-replica cost cap in USD (0 = no cap); passed through to each runner.Run call")
	cmd.Flags().StringVar(&o.provider, "provider", "", "LLM provider override (anthropic|gemini|openai|claude-cli|gemini-cli)")
	cmd.Flags().StringVar(&o.budgetMode, "budget-mode", "", "cost dial: premium|standard|cheap|free (default: HIVE_BUDGET_MODE, else standard)")

	return cmd
}

// replicateOptions holds the flags of chb replicate.
type replicateOptions struct {
	n           int
	vary        string
	foragerList []string
	outPath     string
	seedVal     int64
	maxCostUSD  float64
	provider    string
	budgetMode  string
}

// run runs the replicas of question and writes their meta-diff to --out.
func (o *replicateOptions) run(cmd *cobra.Command, question string) error {
	seed, err := o.check(cmd)
	if err != nil {
		return err
	}
	// Build the workflow YAML once (all replicas share the same YAML).
	yamlText, err := replicateWorkflow(o.foragerList)
	if err != nil {
		return err
	}
	outPath := cmp.Or(o.outPath, fmt.Sprintf("./replicate-%s-%d.json", o.vary, time.Now().Unix()))

	fmt.Fprintf(cmd.ErrOrStderr(),
		"replicate: n=%d vary=%s question=%q\n",
		o.n, o.vary, truncateForLog(question, 60),
	)
	replicas, loadedArtifacts := o.runReplicas(cmd, question, yamlText, seed)

	// Hash-first compare.
	output := buildMetaDiff(question, o.n, o.vary, seed, replicas, loadedArtifacts)
	if err := writeReplicateOutput(outPath, output); err != nil {
		return err
	}

	// Print summary to stdout.
	fmt.Printf("replicate %s n=%d: convergent=%d sensitivity_surface=%d contradicting=%d\n",
		o.vary, o.n,
		output.Summary.Convergent,
		output.Summary.SensitivitySurface,
		output.Summary.Contradicting,
	)
	fmt.Fprintf(cmd.ErrOrStderr(), "wrote meta-diff to %s\n", outPath)
	return nil
}

// check validates the flags before anything runs, and returns the seed
// every replica runs with.
func (o *replicateOptions) check(cmd *cobra.Command) (*int64, error) {
	if err := o.checkAxes(); err != nil {
		return nil, err
	}
	seed, err := o.seed(cmd)
	if err != nil {
		return nil, err
	}
	if o.n < 1 {
		return nil, fmt.Errorf("--n must be >= 1 (got %d)", o.n)
	}
	return seed, nil
}

// checkAxes validates --budget-mode, then --vary.
func (o *replicateOptions) checkAxes() error {
	if _, err := runner.ResolveBudgetMode(o.budgetMode); err != nil {
		return err
	}
	return checkReplicateVary(o.vary)
}

// checkReplicateVary refuses a --vary axis replicate does not run.
func checkReplicateVary(vary string) error {
	switch vary {
	case "none", "stochastic":
		return nil
	case "phrasing":
		// TODO: implement phrasing paraphrase generation in v2.
		// For now, return a clear "not yet implemented" error.
		return fmt.Errorf("--vary phrasing is not yet implemented; use --vary none|stochastic")
	case "model":
		// TODO: implement model cycling (haiku/sonnet/opus) in v2.
		// The logic is non-trivial when n > 3 (needs wrap-around) and
		// complicates the forager tier system. Deferred.
		return fmt.Errorf("--vary model is not yet implemented; use --vary none|stochastic")
	}
	return fmt.Errorf("--vary must be one of: none|stochastic|phrasing|model (got %q)", vary)
}

// seed is the seed every replica runs with: --seed under --vary none, which
// requires an explicit --seed for byte-identical reproduction, and none
// under any other axis.
func (o *replicateOptions) seed(cmd *cobra.Command) (*int64, error) {
	if o.vary != "none" {
		return nil, nil
	}
	if !cmd.Flags().Changed("seed") {
		return nil, fmt.Errorf("--vary none requires an explicit --seed N for byte-identical reproduction across replicas")
	}
	return &o.seedVal, nil
}

// replicateWorkflow generates the workflow every replica runs: the foragers
// listed, with the Queen as synthesizer.
func replicateWorkflow(foragerList []string) (string, error) {
	all, err := loadForagers()
	if err != nil {
		return "", err
	}
	swarm, err := foragers.Filter(all, foragerList)
	if err != nil {
		return "", err
	}
	synth, _ := foragers.ByName(all, "queen")
	yamlText, err := foragers.GenerateWorkflow(swarm, foragers.WorkflowOptions{
		Synthesizer: synth,
	})
	if err != nil {
		return "", fmt.Errorf("generate workflow: %w", err)
	}
	return yamlText, nil
}

// runReplicas runs the replicas one after another, returning each one's
// result and artifact.
func (o *replicateOptions) runReplicas(cmd *cobra.Command, question, yamlText string, seed *int64) ([]replicaResult, []*artifact.Artifact) {
	replicas := make([]replicaResult, 0, o.n)
	loadedArtifacts := make([]*artifact.Artifact, 0, o.n)
	for i := 0; i < o.n; i++ {
		res, art := o.runReplica(cmd, question, i, yamlText, seed)
		replicas = append(replicas, res)
		loadedArtifacts = append(loadedArtifacts, art)
	}
	return replicas, loadedArtifacts
}

// runReplica runs replica i and reports it on stderr. A replica that fails
// is an invalid result with no artifact — partial results are still useful
// for divergence analysis.
func (o *replicateOptions) runReplica(cmd *cobra.Command, question string, i int, yamlText string, seed *int64) (replicaResult, *artifact.Artifact) {
	res, art, err := runOneReplica(cmd.Context(), question, i, yamlText, o.vary, seed, o.maxCostUSD, o.provider, o.budgetMode, nil)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "  replica %d: ERROR: %v\n", i, err)
		return replicaResult{Index: i, Valid: false}, nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"  replica %d: hash=%s valid=%v\n", i, res.Hash, res.Valid,
	)
	return *res, art
}

// writeReplicateOutput writes the meta-diff to path as indented JSON.
func writeReplicateOutput(path string, output *replicateOutput) error {
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// runOneReplica runs a single swarm instance with its own database in a
// temp dir and returns the replica result plus the loaded artifact. A nil
// backend resolves from the environment, as agent-run does.
func runOneReplica(
	ctx context.Context,
	question string,
	index int,
	yamlText string,
	vary string,
	seed *int64,
	maxCostUSD float64,
	provider string,
	budgetMode string,
	backend runner.LLMBackend,
) (*replicaResult, *artifact.Artifact, error) {
	bm, err := runner.ResolveBudgetMode(budgetMode)
	if err != nil {
		return nil, nil, err
	}
	ws, err := openReplicaWorkspace(index, yamlText)
	if err != nil {
		return nil, nil, err
	}
	// The store is closed and the temp dir removed once the artifact is
	// loaded; the caller reads the artifact from memory.
	defer ws.close()

	// Only none uses deterministic mode.
	deterministic, seedPtr := replicaSeed(vary, seed)

	// Only the database is isolated. ProjectDir stays empty, so the runner
	// uses the caller's working directory as agent-run does: personas
	// resolve from its agents/ and tools run there. A temp dir holds no
	// agents/analyst.md, so every node would run with an empty system
	// prompt. DBPath sends the agents' own writes to the replica's database,
	// not the caller's.
	cfg := runner.Config{
		WorkflowYAML:     ws.wfPath,
		ProjectName:      fmt.Sprintf("replica-%d", index),
		DBPath:           ws.dbPath,
		Inputs:           map[string]any{"question": question, "context": ""},
		Backend:          backend,
		Branch:           "", // no auto-commit
		Deterministic:    deterministic,
		Seed:             seedPtr,
		ArtifactPath:     ws.artifactPath,
		Log:              os.Stderr,
		MaxCostUSDx10000: int64(maxCostUSD * 10000),
		Provider:         provider,
		BudgetMode:       bm,
	}

	// Run the workflow synchronously.
	if _, err := runner.Run(ctx, ws.store, cfg); err != nil {
		return nil, nil, err
	}
	hash, valid, art := loadReplicaArtifact(ws.artifactPath)
	return &replicaResult{
		Index:        index,
		ArtifactPath: ws.artifactPath, // path is gone after cleanup — kept for provenance
		Hash:         hash,
		Valid:        valid,
	}, art, nil
}

// replicaWorkspace is one replica's temp dir: its own database (a fully
// isolated Comb), its workflow, and the path its artifact is written to.
type replicaWorkspace struct {
	dir          string
	dbPath       string
	wfPath       string
	artifactPath string
	store        *db.Store
}

// openReplicaWorkspace creates replica index's temp dir, opens a fresh
// store in it and writes the workflow there.
func openReplicaWorkspace(index int, yamlText string) (*replicaWorkspace, error) {
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("swarm-replicate-%d-*", index))
	if err != nil {
		return nil, fmt.Errorf("mkdirtemp: %w", err)
	}
	ws := &replicaWorkspace{
		dir:          tmpDir,
		dbPath:       filepath.Join(tmpDir, "hive.db"),
		wfPath:       filepath.Join(tmpDir, "swarm.yaml"),
		artifactPath: filepath.Join(tmpDir, "artifact.json"),
	}
	if err := ws.open(yamlText); err != nil {
		ws.close()
		return nil, err
	}
	return ws, nil
}

// open opens the replica's store, creates its schema and writes the
// workflow YAML.
func (ws *replicaWorkspace) open(yamlText string) error {
	s, err := db.NewStore(ws.dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	ws.store = s
	if err := s.Init(); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	if err := os.WriteFile(ws.wfPath, []byte(yamlText), 0o644); err != nil {
		return fmt.Errorf("write workflow yaml: %w", err)
	}
	return nil
}

// close closes the replica's store, if it opened, and removes its temp
// dir.
func (ws *replicaWorkspace) close() {
	if ws.store != nil {
		ws.store.Close()
	}
	os.RemoveAll(ws.dir)
}

// replicaSeed is whether a replica runs deterministically, and the seed it
// runs with: only --vary none is deterministic, with the seed it was given.
func replicaSeed(vary string, seed *int64) (bool, *int64) {
	if vary != "none" {
		return false, nil
	}
	return true, seed
}

// loadReplicaArtifact verifies the artifact a replica wrote and returns its
// stored hash, whether the hash matches, and the full artifact for
// field-level diffing. An artifact that does not verify yields none of
// them, and one that does not decode yields no artifact.
func loadReplicaArtifact(path string) (string, bool, *artifact.Artifact) {
	matched, expected, _, err := artifact.VerifyArtifact(path)
	if err != nil {
		return "", false, nil
	}
	return expected, matched, readReplicaArtifact(path)
}

// readReplicaArtifact decodes the artifact at path, or returns nil.
func readReplicaArtifact(path string) *artifact.Artifact {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var a artifact.Artifact
	if json.Unmarshal(data, &a) != nil {
		return nil
	}
	return &a
}

// buildMetaDiff compares replica artifacts and returns the meta-diff output.
// Hash-first: if all hashes match, short-circuit with convergent summary.
func buildMetaDiff(
	question string,
	n int,
	vary string,
	seed *int64,
	replicas []replicaResult,
	artifacts []*artifact.Artifact,
) *replicateOutput {
	out := &replicateOutput{
		Question: question,
		N:        n,
		Vary:     vary,
		Seed:     seed,
		Replicas: replicas,
	}

	// Hash-first: if all valid replicas have identical hashes, short-circuit.
	if validHashes := validReplicaHashes(replicas); len(validHashes) > 0 && allEqual(validHashes) {
		out.Summary = replicateSummary{Convergent: firstVantageCount(artifacts)}
		return out
	}

	// Hash mismatch — drill into per-vantage diffs.
	out.Vantages = buildVantageDiff(artifacts)
	out.Summary = classifySummary(out.Vantages)
	return out
}

// validReplicaHashes are the hashes of the valid replicas that have one.
func validReplicaHashes(replicas []replicaResult) []string {
	validHashes := make([]string, 0, len(replicas))
	for _, r := range replicas {
		if r.Valid && r.Hash != "" {
			validHashes = append(validHashes, r.Hash)
		}
	}
	return validHashes
}

// firstVantageCount counts the vantages of the first valid artifact.
func firstVantageCount(artifacts []*artifact.Artifact) int {
	for _, a := range artifacts {
		if a != nil {
			return len(a.Comb)
		}
	}
	return 0
}

// buildVantageDiff performs field-level diff across artifact Comb slices.
func buildVantageDiff(artifacts []*artifact.Artifact) []vantageEntry {
	keys := artifactVantageKeys(artifacts)
	entries := make([]vantageEntry, 0, len(keys))
	for _, key := range keys {
		replicaData := vantageAcrossReplicas(artifacts, key)
		entries = append(entries, vantageEntry{
			Key:      key,
			Replicas: replicaData,
			Div:      classifyVantageDiv(replicaData),
		})
	}
	return entries
}

// artifactVantageKeys collects every vantage key across all artifacts,
// sorted for determinism.
func artifactVantageKeys(artifacts []*artifact.Artifact) []string {
	keySet := make(map[string]struct{})
	for _, a := range artifacts {
		if a == nil {
			continue
		}
		for _, v := range a.Comb {
			keySet[v.VantageKey] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(keySet))
}

// vantageAcrossReplicas is each replica's data for the vantage key, in
// replica order.
func vantageAcrossReplicas(artifacts []*artifact.Artifact, key string) []vantageReplica {
	replicaData := make([]vantageReplica, 0, len(artifacts))
	for _, a := range artifacts {
		replicaData = append(replicaData, replicaVantage(a, key))
	}
	return replicaData
}

// replicaVantage is one artifact's data for the vantage key: empty when the
// replica produced no artifact or its Comb lacks the vantage.
func replicaVantage(a *artifact.Artifact, key string) vantageReplica {
	if a == nil {
		return vantageReplica{}
	}
	for _, v := range a.Comb {
		if v.VantageKey == key {
			return vantageReplica{
				Narrative:     v.Narrative,
				Confidence:    v.Confidence,
				DominantLabel: v.DominantLabel,
			}
		}
	}
	return vantageReplica{}
}

// classifyVantageDiv computes divergence metrics for one vantage across replicas.
func classifyVantageDiv(replicas []vantageReplica) vantageDiv {
	if len(replicas) == 0 {
		return vantageDiv{LabelConsensus: "unanimous"}
	}
	return vantageDiv{
		ConfidenceRange:    confidenceRange(replicas),
		DistinctNarratives: distinctNarratives(replicas),
		LabelConsensus:     labelConsensus(replicas),
	}
}

// confidenceRange is the spread between the replicas' lowest and highest
// confidence.
func confidenceRange(replicas []vantageReplica) int {
	minConf, maxConf := replicas[0].Confidence, replicas[0].Confidence
	for _, r := range replicas[1:] {
		minConf = min(minConf, r.Confidence)
		maxConf = max(maxConf, r.Confidence)
	}
	return maxConf - minConf
}

// distinctNarratives counts the replicas' distinct narratives.
func distinctNarratives(replicas []vantageReplica) int {
	narrSet := make(map[string]struct{})
	for _, r := range replicas {
		narrSet[r.Narrative] = struct{}{}
	}
	return len(narrSet)
}

// labelConsensus is "unanimous" when the replicas share one dominant label,
// "majority" when one label holds more than half of them, else "split".
func labelConsensus(replicas []vantageReplica) string {
	labelSet := make(map[string]int)
	for _, r := range replicas {
		labelSet[r.DominantLabel]++
	}
	if len(labelSet) <= 1 {
		return "unanimous"
	}
	if hasMajorityLabel(labelSet, len(replicas)) {
		return "majority"
	}
	return "split"
}

// hasMajorityLabel reports whether any label holds more than half of the n
// replicas.
func hasMajorityLabel(labelSet map[string]int, n int) bool {
	for _, cnt := range labelSet {
		if cnt > n/2 {
			return true
		}
	}
	return false
}

// classifySummary aggregates vantage entries into the summary counts.
// convergent: all replicas agree on narrative, confidence, and label.
// sensitivity_surface: some but not all replicas agree.
// contradicting: no two replicas agree.
func classifySummary(entries []vantageEntry) replicateSummary {
	var convergent, sensitivitySurface, contradicting int
	for _, e := range entries {
		switch {
		case e.convergent():
			convergent++
		case e.Div.DistinctNarratives == len(e.Replicas):
			contradicting++
		default:
			sensitivitySurface++
		}
	}
	return replicateSummary{
		Convergent:         convergent,
		SensitivitySurface: sensitivitySurface,
		Contradicting:      contradicting,
	}
}

// convergent reports whether the replicas agree on the vantage: one
// narrative at one confidence.
func (e vantageEntry) convergent() bool {
	return e.Div.DistinctNarratives <= 1 && e.Div.ConfidenceRange == 0
}

// allEqual returns true when every string in the slice is identical.
func allEqual(ss []string) bool {
	if len(ss) == 0 {
		return true
	}
	first := ss[0]
	for _, s := range ss[1:] {
		if s != first {
			return false
		}
	}
	return true
}
