package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/embed"
	"github.com/spf13/cobra"
)

// newCombEmbedCmd implements `chb comb embed` — generate embeddings
// for Comb vantages (region/forager) and, with `--findings`, for raw
// findings under the `finding:<id>` vantage_key convention.
// Auto-detects the provider; pass `--provider stub` for isolated test
// runs.
//
//	chb comb embed                                   # all Comb vantages
//	chb comb embed --vantage d1=0                    # one vantage
//	chb comb embed --kind forager                     # only foragers
//	chb comb embed --findings                        # every finding (substrate for Dreamer prune)
//	chb comb embed --findings --wave 3               # findings from one wave only
//	chb comb embed --provider openai --model text-embedding-3-large
//	chb comb embed --dry-run                         # report only
func newCombEmbedCmd() *cobra.Command {
	var o combEmbedOptions
	cmd := &cobra.Command{
		Use:   "embed",
		Short: "Generate embeddings for Comb vantages (semantic-similarity sidecar)",
		Long: `Generates a float32 embedding per vantage and writes it to the
comb_embeddings table. Re-runs are idempotent at (vantage_key, model)
granularity — same model + same key overwrites; different model adds
a new row.

Provider precedence: --provider flag > HIVE_EMBED_PROVIDER env >
auto-detect (HIVE_LOCAL_EMBED_URL → localhttp, OPENAI_API_KEY →
openai, else stub). The stub is deterministic but not semantically
meaningful — its presence in production fires a stderr warning.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombEmbed(cmd.OutOrStdout(), &o)
		},
	}
	cmd.Flags().StringVar(&o.vantage, "vantage", "", "embed only this vantage_key")
	cmd.Flags().StringVar(&o.kind, "kind", "", "limit to vantage kind (region|forager); findings and questions have their own flags")
	cmd.Flags().StringVar(&o.provider, "provider", "", "embedding provider (openai|localhttp|stub)")
	cmd.Flags().StringVar(&o.modelName, "model", "", "model identifier (provider-specific)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "report what would be embedded; don't write")
	cmd.Flags().BoolVar(&o.questions, "questions", false, "embed the question each swarm run was asked (the substrate chb recall matches against)")
	cmd.Flags().BoolVar(&o.findings, "findings", false, "embed every finding under finding:<id> (substrate for Dreamer semantic prune)")
	cmd.Flags().IntVar(&o.findingWave, "wave", 0, "with --findings: only embed findings in this wave (0 = all waves)")
	return cmd
}

// combEmbedOptions holds chb comb embed's flag values.
type combEmbedOptions struct {
	vantage     string
	kind        string
	provider    string
	modelName   string
	dryRun      bool
	findings    bool
	questions   bool
	findingWave int
}

// runCombEmbed embeds what the flags select with the provider they choose,
// reporting on w.
func runCombEmbed(w io.Writer, o *combEmbedOptions) error {
	if err := store.Init(); err != nil {
		return err
	}
	p, err := selectProvider(o.provider, o.modelName)
	if err != nil {
		return err
	}
	return o.embedSelection(context.Background(), w, p)
}

// embedSelection runs the branch the flags choose: findings, questions, or
// else the Comb vantages.
func (o *combEmbedOptions) embedSelection(ctx context.Context, w io.Writer, p embed.Provider) error {
	switch {
	case o.findings:
		// Findings branch — substrate for the Dreamer's semantic
		// prune pass. Iterates findings, writes one embedding row
		// per finding under "finding:<id>" with vantage_kind
		// "finding". Re-runs are idempotent per (key, model).
		return o.embedFindings(ctx, w, p)
	case o.questions:
		// Questions branch — substrate for `chb recall`. One embedding
		// per swarm run, keyed "question:<run_id>", of the question the
		// run was asked. Without these, recall can only compare a new
		// question against prior *verdicts*, which is a different thing.
		return o.embedQuestions(ctx, w, p)
	default:
		return o.embedVantages(ctx, w, p)
	}
}

// embedVantages embeds the Comb vantages --vantage and --kind select.
func (o *combEmbedOptions) embedVantages(ctx context.Context, w io.Writer, p embed.Provider) error {
	rows, err := combEmbedTargets(o.vantage, o.kind)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no vantages match the selection")
		return nil
	}
	if o.dryRun {
		fmt.Fprintf(w,
			"[dry-run] %d vantages would be embedded via %s\n",
			len(rows), p.Name())
		return nil
	}
	return writeVantageEmbeddings(ctx, w, p, rows)
}

// combEmbedTargets checks --kind and returns the Comb rows --vantage and
// --kind select.
func combEmbedTargets(vantage, kind string) ([]*db.CombRow, error) {
	// A kind the Comb does not have is refused, not answered "no vantages
	// match" as an empty Comb would be.
	if err := flagOneOf("--kind", kind, "region", "forager"); err != nil {
		return nil, err
	}
	return selectEmbedTargets(store, vantage, kind)
}

// writeVantageEmbeddings embeds the rows in one batch and upserts each
// embedding under its vantage key.
func writeVantageEmbeddings(ctx context.Context, w io.Writer, p embed.Provider, rows []*db.CombRow) error {
	texts := combEmbedTexts(rows, embedTextFor)
	vecs, err := p.BatchEmbed(ctx, texts)
	if err != nil {
		return fmt.Errorf("batch embed: %w", err)
	}
	for i, v := range vecs {
		if err := upsertVantageEmbedding(rows[i], texts[i], p.Name(), v); err != nil {
			return err
		}
	}
	fmt.Fprintf(w,
		"embedded %d vantages via %s (dim=%d)\n",
		len(vecs), p.Name(), p.Dim())
	return nil
}

// upsertVantageEmbedding writes one vantage's embedding, with its source text
// when there is one.
func upsertVantageEmbedding(r *db.CombRow, text, model string, v []float32) error {
	row := &db.CombEmbeddingRow{
		VantageKey:  r.VantageKey,
		VantageKind: r.VantageKind,
		Model:       model,
		Dim:         len(v),
		Embedding:   v,
	}
	if text != "" {
		row.SourceText.String = text
		row.SourceText.Valid = true
	}
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		return fmt.Errorf("upsert %s: %w", r.VantageKey, err)
	}
	return nil
}

// combEmbedTexts is the text of each item, in order.
func combEmbedTexts[T any](items []T, text func(T) string) []string {
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = text(it)
	}
	return texts
}

// skipCombEmbedded drops, in place, the items that already have an
// embedding under model, returning the rest in order and how many it
// dropped.
func skipCombEmbedded[T any](items []T, model string, key func(T) string) ([]T, int, error) {
	skipped := 0
	pending := items[:0]
	for _, it := range items {
		k := key(it)
		existing, err := store.CombEmbeddings().Get(k, model)
		if err != nil {
			return nil, 0, fmt.Errorf("probe existing %s: %w", k, err)
		}
		if existing != nil {
			skipped++
			continue
		}
		pending = append(pending, it)
	}
	return pending, skipped, nil
}

// reportIdleCombEmbed handles a run that writes nothing: a dry run says what it
// would embed, and a run with nothing pending says every item is embedded
// already. It reports whether the run ends here.
func reportIdleCombEmbed(w io.Writer, noun string, pending, skipped int, model string, dryRun bool) bool {
	if dryRun {
		fmt.Fprintf(w,
			"[dry-run] would embed %d %s via %s (skipping %d already done)\n",
			pending, noun, model, skipped)
		return true
	}
	if pending == 0 {
		fmt.Fprintf(w,
			"all %d %s already embedded under %s\n", skipped, noun, model)
		return true
	}
	return false
}

// embedFindings runs the --findings branch: streams findings from the
// DB, embeds in batches of 64 (kind to OpenAI rate limits + memory),
// upserts each result under vantage_key "finding:<id>".
//
// Idempotent — re-running on the same DB skips findings that already
// have an embedding for this model. The skip check is per-batch so a
// partial run resumes cleanly.
func (o *combEmbedOptions) embedFindings(ctx context.Context, w io.Writer, p embed.Provider) error {
	rows, err := loadFindingsForEmbed(o.findingWave)
	if err != nil {
		return fmt.Errorf("load findings: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no findings match the selection")
		return nil
	}
	pending, skipped, err := skipCombEmbedded(rows, p.Name(), func(f findingForEmbed) string {
		return db.FindingVantageKey(f.id)
	})
	if err != nil {
		return err
	}
	return o.writePendingFindings(ctx, w, p, pending, skipped)
}

// writePendingFindings embeds the findings not embedded yet and reports the
// run; a dry run, or a run with none pending, only says so.
func (o *combEmbedOptions) writePendingFindings(ctx context.Context, w io.Writer, p embed.Provider, pending []findingForEmbed, skipped int) error {
	if reportIdleCombEmbed(w, "findings", len(pending), skipped, p.Name(), o.dryRun) {
		return nil
	}
	written, err := writeFindingEmbeddings(ctx, p, pending)
	if err != nil {
		return err
	}
	fmt.Fprintf(w,
		"embedded %d findings via %s (dim=%d, skipped %d already-done)\n",
		written, p.Name(), p.Dim(), skipped)
	return nil
}

// writeFindingEmbeddings embeds the findings in batches of 64 and upserts
// each embedding, returning how many it wrote.
func writeFindingEmbeddings(ctx context.Context, p embed.Provider, pending []findingForEmbed) (int, error) {
	const batchSize = 64
	written := 0
	for i := 0; i < len(pending); i += batchSize {
		n, err := writeFindingBatch(ctx, p, pending[i:min(i+batchSize, len(pending))], i)
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// writeFindingBatch embeds one batch, which starts at offset i of the
// pending findings, and upserts each embedding, returning how many it
// wrote.
func writeFindingBatch(ctx context.Context, p embed.Provider, batch []findingForEmbed, i int) (int, error) {
	texts := combEmbedTexts(batch, func(f findingForEmbed) string { return f.text })
	vecs, err := p.BatchEmbed(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("batch embed (i=%d): %w", i, err)
	}
	for j, v := range vecs {
		if err := upsertFindingEmbedding(batch[j], p.Name(), v); err != nil {
			return j, err
		}
	}
	return len(vecs), nil
}

// upsertFindingEmbedding writes one finding's embedding under
// "finding:<id>".
func upsertFindingEmbedding(f findingForEmbed, model string, v []float32) error {
	row := &db.CombEmbeddingRow{
		VantageKey:  db.FindingVantageKey(f.id),
		VantageKind: db.VantageFinding,
		Model:       model,
		Dim:         len(v),
		Embedding:   v,
	}
	row.SourceText.String = f.text
	row.SourceText.Valid = true
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		return fmt.Errorf("upsert finding:%d: %w", f.id, err)
	}
	return nil
}

// findingForEmbed is the minimal projection of a finding row needed
// for embedding — keeps memory low when iterating thousands.
type findingForEmbed struct {
	id   int64
	text string
}

// loadFindingsForEmbed reads (id, finding_text) for every finding,
// optionally restricted to a wave. Streams via Query/Next so memory
// stays bounded even on workspaces with tens of thousands of findings.
func loadFindingsForEmbed(wave int) ([]findingForEmbed, error) {
	q, args := findingsForEmbedQuery(wave)
	rows, err := store.ReadConn().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []findingForEmbed
	for rows.Next() {
		var f findingForEmbed
		if err := rows.Scan(&f.id, &f.text); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// findingsForEmbedQuery selects every finding's id and text in id order, of
// one wave when wave is above 0.
func findingsForEmbedQuery(wave int) (string, []any) {
	q := `SELECT id, finding FROM findings`
	args := []any{}
	if wave > 0 {
		q += ` WHERE wave = ?`
		args = append(args, wave)
	}
	return q + ` ORDER BY id`, args
}

// newCombEmbedStatusCmd reports embedding coverage per (model, kind).
func newCombEmbedStatusCmd() *cobra.Command {
	var emitJSON bool
	cmd := &cobra.Command{
		Use:   "embed-status",
		Short: "Report embedding coverage across the Comb",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombEmbedStatus(emitJSON)
		},
	}
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit JSON")
	return cmd
}

// combEmbedStatusRow is the embedding coverage of one (model, kind).
type combEmbedStatusRow struct {
	Model string `json:"model"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Dim   int    `json:"dim"`
}

// runCombEmbedStatus prints the embedding coverage per (model, kind).
func runCombEmbedStatus(emitJSON bool) error {
	if err := store.Init(); err != nil {
		return err
	}
	out, err := queryCombEmbedStatus()
	if err != nil {
		return err
	}
	printCombEmbedStatus(out, emitJSON)
	return nil
}

// queryCombEmbedStatus counts the embeddings and their largest dimension
// per (model, kind), in that order.
func queryCombEmbedStatus() ([]combEmbedStatusRow, error) {
	rows, err := store.ReadConn().Query(
		`SELECT model, vantage_kind, COUNT(*), MAX(dim)
		 FROM comb_embeddings GROUP BY model, vantage_kind ORDER BY model, vantage_kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []combEmbedStatusRow
	for rows.Next() {
		var r combEmbedStatusRow
		if err := rows.Scan(&r.Model, &r.Kind, &r.Count, &r.Dim); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// printCombEmbedStatus prints the coverage as JSON or as a table.
func printCombEmbedStatus(out []combEmbedStatusRow, emitJSON bool) {
	if emitJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(out) == 0 {
		fmt.Println("no embeddings yet")
		return
	}
	fmt.Printf("%-40s %-8s %-6s %-5s\n", "MODEL", "KIND", "COUNT", "DIM")
	for _, r := range out {
		fmt.Printf("%-40s %-8s %-6d %-5d\n", r.Model, r.Kind, r.Count, r.Dim)
	}
}

// newCombSimilarCmd surfaces semantically-nearest vantages.
//
//	chb comb similar --vantage forager:optimist --top 5
//	chb comb similar --vantage d1=0;d2=3 --top 10 --kind region
func newCombSimilarCmd() *cobra.Command {
	var o combSimilarOptions
	cmd := &cobra.Command{
		Use:   "similar",
		Short: "Find semantically-nearest vantages on the Comb",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombSimilar(o)
		},
	}
	cmd.Flags().StringVar(&o.vantage, "vantage", "", "anchor vantage key")
	cmd.Flags().IntVar(&o.top, "top", 5, "max matches to return")
	cmd.Flags().StringVar(&o.kind, "kind", "", "limit results to vantage kind (region|forager|finding|question)")
	cmd.Flags().StringVar(&o.modelName, "model", "", "embedding model (default: most-recently-used)")
	cmd.Flags().BoolVar(&o.emitJSON, "json", false, "emit JSON")
	return cmd
}

// combSimilarOptions holds chb comb similar's flag values.
type combSimilarOptions struct {
	vantage   string
	top       int
	kind      string
	modelName string
	emitJSON  bool
}

// runCombSimilar prints the vantages nearest the anchor vantage.
func runCombSimilar(o combSimilarOptions) error {
	if o.vantage == "" {
		return fmt.Errorf("--vantage required (region key or forager:<name>)")
	}
	if err := store.Init(); err != nil {
		return err
	}
	model, matches, err := o.search()
	if err != nil {
		return err
	}
	printCombSimilar(matches, o.vantage, model, o.emitJSON)
	return nil
}

// search resolves the model and finds the vantages nearest the anchor's
// embedding under it.
func (o combSimilarOptions) search() (string, []embed.Match, error) {
	model, anchor, err := combSimilarAnchor(o.vantage, o.modelName)
	if err != nil {
		return "", nil, err
	}
	matches, err := nearestCombVantages(anchor, model, o.top, o.kind, o.vantage)
	return model, matches, err
}

// combSimilarAnchor resolves the model and reads the anchor vantage's
// embedding under it, refusing a vantage with none.
func combSimilarAnchor(vantage, modelName string) (string, *db.CombEmbeddingRow, error) {
	model, err := resolveActiveModel(modelName)
	if err != nil {
		return "", nil, err
	}
	anchor, err := store.CombEmbeddings().Get(vantage, model)
	if err != nil {
		return "", nil, err
	}
	if anchor == nil {
		return "", nil, fmt.Errorf("no embedding for vantage %q with model %q — run `chb comb embed` first", vantage, model)
	}
	return model, anchor, nil
}

// nearestCombVantages checks --kind and finds the top vantages nearest the
// anchor's embedding, the anchor itself left out.
func nearestCombVantages(anchor *db.CombEmbeddingRow, model string, top int, kind, vantage string) ([]embed.Match, error) {
	if err := flagOneOf("--kind", kind, "region", "forager", "finding", "question"); err != nil {
		return nil, err
	}
	searcher := embed.NewSearcher(store, model)
	return searcher.NearestK(anchor.Embedding, top, db.VantageKind(kind), vantage)
}

// printCombSimilar prints the matches as JSON or one line each.
func printCombSimilar(matches []embed.Match, vantage, model string, emitJSON bool) {
	if emitJSON {
		b, _ := json.MarshalIndent(matches, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("nearest %d to %s (model=%s)\n", len(matches), vantage, model)
	for _, m := range matches {
		fmt.Printf("  %.4f  %-30s  %s\n", m.Score, m.VantageKey, truncFor(m.SourceText, 60))
	}
}

// embedTextFor builds the input string fed to the embedding provider
// for one Comb row. Region vantages: the deterministic narrative.
// Forager vantages: the verdict's recommendation if present, else the
// raw narrative.
func embedTextFor(r *db.CombRow) string {
	if rec := foragerRecommendation(r); rec != "" {
		return rec
	}
	if strings.TrimSpace(r.Narrative) != "" {
		return r.Narrative
	}
	return r.VantageKey
}

// foragerRecommendation is a forager vantage's verdict recommendation; ""
// for any other vantage or a verdict that does not parse.
func foragerRecommendation(r *db.CombRow) string {
	if !r.RawJSON.Valid || r.VantageKind != db.VantageForager {
		return ""
	}
	var verdict struct {
		Recommendation string `json:"recommendation"`
	}
	if err := json.Unmarshal([]byte(r.RawJSON.String), &verdict); err != nil {
		return ""
	}
	return verdict.Recommendation
}

// selectEmbedTargets returns the Comb rows to embed based on flags.
func selectEmbedTargets(store *db.Store, vantage, kind string) ([]*db.CombRow, error) {
	if vantage != "" {
		return selectEmbedVantage(store, vantage)
	}
	if kind != "" {
		return store.Comb().ListKind(db.VantageKind(kind))
	}
	return store.Comb().List()
}

// selectEmbedVantage is the one Comb row --vantage names, refusing a key
// with no row.
func selectEmbedVantage(store *db.Store, vantage string) ([]*db.CombRow, error) {
	row, err := store.Comb().Get(vantage)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("no Comb row for vantage %q", vantage)
	}
	return []*db.CombRow{row}, nil
}

// selectProvider applies the provider-precedence rule.
func selectProvider(name, model string) (embed.Provider, error) {
	if model != "" {
		// The providers read their model from HIVE_EMBED_MODEL, so the
		// flag is applied by setting it. Single-shot CLI process, so the
		// splice is not restored.
		_ = os.Setenv("HIVE_EMBED_MODEL", model)
	}
	if name != "" {
		return embed.SelectByName(name)
	}
	return embed.Detect()
}

// resolveActiveModel returns the model identifier to use for a query.
// Priority: explicit --model flag > most-recently-written model.
func resolveActiveModel(flagModel string) (string, error) {
	if flagModel != "" {
		return flagModel, nil
	}
	var model string
	err := store.ReadConn().QueryRow(
		`SELECT model FROM comb_embeddings ORDER BY id DESC LIMIT 1`,
	).Scan(&model)
	if err != nil {
		return "", fmt.Errorf("no embeddings exist yet — run `chb comb embed` first")
	}
	return model, nil
}

// questionRow is one swarm run's question, keyed by run id.
type questionRow struct {
	runID int64
	text  string
}

// loadQuestionsForEmbed reads the question each workflow run was asked from
// workflow_runs.inputs_json. Runs without a `question` input (an implement
// or review run, say) have nothing to recall and are skipped.
func loadQuestionsForEmbed() ([]questionRow, error) {
	rows, err := store.ReadDB.Query(
		`SELECT id, COALESCE(inputs_json, '') FROM workflow_runs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestionRows(rows)
}

// scanQuestionRows reads each run's question from the rows, skipping a run
// without one.
func scanQuestionRows(rows *sql.Rows) ([]questionRow, error) {
	var out []questionRow
	for rows.Next() {
		var id int64
		var inputsJSON string
		if err := rows.Scan(&id, &inputsJSON); err != nil {
			return nil, err
		}
		if q, ok := questionFromInputs(inputsJSON); ok {
			out = append(out, questionRow{runID: id, text: q})
		}
	}
	return out, rows.Err()
}

// questionFromInputs is the question a run's inputs JSON carries; ok is
// false when the JSON does not parse or its question is missing or blank.
func questionFromInputs(inputsJSON string) (string, bool) {
	var inputs map[string]any
	if json.Unmarshal([]byte(inputsJSON), &inputs) != nil {
		return "", false
	}
	q, _ := inputs["question"].(string)
	return q, strings.TrimSpace(q) != ""
}

// embedQuestions writes one embedding per run under "question:<run_id>".
// Idempotent per (key, model), like the findings branch.
func (o *combEmbedOptions) embedQuestions(ctx context.Context, w io.Writer, p embed.Provider) error {
	rows, err := loadQuestionsForEmbed()
	if err != nil {
		return fmt.Errorf("load questions: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no runs in this workspace carry a question input")
		return nil
	}
	pending, skipped, err := skipCombEmbedded(rows, p.Name(), func(q questionRow) string {
		return db.QuestionVantageKey(q.runID)
	})
	if err != nil {
		return err
	}
	return o.writePendingQuestions(ctx, w, p, pending, skipped)
}

// writePendingQuestions embeds the questions not embedded yet and reports
// the run; a dry run, or a run with none pending, only says so.
func (o *combEmbedOptions) writePendingQuestions(ctx context.Context, w io.Writer, p embed.Provider, pending []questionRow, skipped int) error {
	if reportIdleCombEmbed(w, "questions", len(pending), skipped, p.Name(), o.dryRun) {
		return nil
	}
	if err := writeQuestionEmbeddings(ctx, p, pending); err != nil {
		return err
	}
	fmt.Fprintf(w,
		"embedded %d questions via %s (dim=%d, skipped %d already-done)\n",
		len(pending), p.Name(), p.Dim(), skipped)
	return nil
}

// writeQuestionEmbeddings embeds the questions in one batch and upserts each
// embedding.
func writeQuestionEmbeddings(ctx context.Context, p embed.Provider, pending []questionRow) error {
	vecs, err := p.BatchEmbed(ctx, combEmbedTexts(pending, func(q questionRow) string { return q.text }))
	if err != nil {
		return fmt.Errorf("batch embed questions: %w", err)
	}
	for i, v := range vecs {
		if err := upsertQuestionEmbedding(pending[i], p.Name(), v); err != nil {
			return err
		}
	}
	return nil
}

// upsertQuestionEmbedding writes one run's question embedding under
// "question:<run_id>".
func upsertQuestionEmbedding(q questionRow, model string, v []float32) error {
	row := &db.CombEmbeddingRow{
		VantageKey:  db.QuestionVantageKey(q.runID),
		VantageKind: db.VantageQuestion,
		Model:       model,
		Dim:         len(v),
		Embedding:   v,
	}
	row.SourceText.String = q.text
	row.SourceText.Valid = true
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		return fmt.Errorf("upsert question:%d: %w", q.runID, err)
	}
	return nil
}

// flagOneOf refuses a value outside a flag's closed set. Empty means the
// flag was not given. A typo is refused rather than filtering everything out
// into an empty result that reads like an empty database.
func flagOneOf(flag, value string, allowed ...string) error {
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("unknown %s %q (want %s)", flag, value, strings.Join(allowed, "|"))
}
