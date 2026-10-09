package artifact

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// nodeRecord is the internal read of a workflow_node_states row
// including the rationale column (fetched separately from WorkflowNodeState
// since GetWorkflowNodeStates does not surface rationale).
type nodeRecord struct {
	nodeName    string
	nodeType    string
	status      string
	model       string
	provider    string
	baseURL     string
	rationale   string
	outputsJSON string
	enforcement string
}

// BuildArtifact assembles an Artifact for the given runID. The question
// field is read from workflow_runs.inputs_json (the chb ask command
// stores it there as {"question": "..."}). The Determinism fields are
// caller-supplied so the artifact records what was actually used.
//
// BuildArtifact does NOT require all nodes to be completed — it captures
// whatever is persisted at call time. Callers should only invoke it after
// the run is terminal to get a meaningful artifact.
func BuildArtifact(store *db.Store, runID int64, det Determinism) (*Artifact, error) {
	src, err := readRunSources(store, runID)
	if err != nil {
		return nil, err
	}
	art := src.assemble(det)
	// Compute the hash: encode with Hash="", take SHA256, set Hash.
	if err := art.computeAndSetHash(); err != nil {
		return nil, fmt.Errorf("compute hash: %w", err)
	}
	return art, nil
}

// runSources is what a run's artifact is built from: its question, its node
// records by name, the comb_state rows, and its lens diversity and
// calibration.
type runSources struct {
	question    string
	nodeByName  map[string]nodeRecord
	combRows    []*db.CombRow
	diversity   workflow.LensDiversity
	calibration workflow.Calibration
}

// readRunSources reads what run runID's artifact is built from.
func readRunSources(store *db.Store, runID int64) (*runSources, error) {
	src, err := readRunRows(store, runID)
	if err != nil {
		return nil, err
	}
	if err := src.readMeasures(store, runID); err != nil {
		return nil, err
	}
	return src, nil
}

// readRunRows reads the run's question, its node records and the
// comb_state rows.
func readRunRows(store *db.Store, runID int64) (*runSources, error) {
	question, err := readQuestion(store, runID)
	if err != nil {
		return nil, fmt.Errorf("read question: %w", err)
	}
	// The store's read DB directly, because GetWorkflowNodeStates returns
	// the WorkflowNodeState struct without rationale. We need a richer read
	// here.
	nodes, err := readNodeRecords(store, runID)
	if err != nil {
		return nil, fmt.Errorf("read node records: %w", err)
	}
	// Forager names are inferred from the vantage_key "forager:<name>" rows
	// in comb_state (populated by the runner via comb.BuildForagerVantage).
	combRows, err := store.Comb().List()
	if err != nil {
		return nil, fmt.Errorf("read comb_state: %w", err)
	}
	return &runSources{question: question, nodeByName: byNodeName(nodes), combRows: combRows}, nil
}

// byNodeName indexes node records by node name.
func byNodeName(nodes []nodeRecord) map[string]nodeRecord {
	out := make(map[string]nodeRecord, len(nodes))
	for _, n := range nodes {
		out[n.nodeName] = n
	}
	return out
}

// readMeasures reads the run's lens diversity and calibration.
func (s *runSources) readMeasures(store *db.Store, runID int64) error {
	diversity, err := workflow.RunDiversity(store.Workflows(), runID)
	if err != nil {
		return fmt.Errorf("read lens diversity: %w", err)
	}
	calibration, err := workflow.RunCalibration(store.Workflows(), runID)
	if err != nil {
		return fmt.Errorf("read calibration: %w", err)
	}
	s.diversity, s.calibration = diversity, calibration
	return nil
}

// assemble builds the run's artifact, its hash not yet set.
func (s *runSources) assemble(det Determinism) *Artifact {
	foragers := s.foragersInRun()
	raw := foragerRawJSON(s.combRows)
	synthesis, synthesizer := s.synthesis(raw)
	art := &Artifact{
		SchemaVersion: SchemaVersion,
		Question:      s.question,
		Determinism:   det,
		Foragers:      foragers,
		Models:        make(map[string]string),
		Providers:     make(map[string]string),
		BaseURLs:      make(map[string]string),
		Verdicts:      make(map[string]any),
		Synthesis:     nonNilObject(synthesis),
		Synthesizer:   synthesizer,
		Comb:          combSnapshot(s.combRows, setOf(foragers)),
		Hash:          "",

		SchemaEnforcement: make(map[string]string),
		Diversity:         s.diversity.State(),
		Calibration:       &s.calibration,
	}
	for _, name := range foragers {
		s.addForager(art, name, raw)
	}
	return art
}

// foragersInRun is, sorted, each forager with a comb vantage whose run has a
// node for it. A forager belongs to this run only when the run has a node
// for it: comb_state holds one row per forager for the whole database, so
// taking every forager vantage would put other runs' foragers into this
// run's artifact, and two identical questions asked in one workspace would
// hash apart.
func (s *runSources) foragersInRun() []string {
	allForagers, _ := extractForagerInfo(s.combRows)
	var names []string
	for _, name := range allForagers {
		if s.foragerNode(name) != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// foragerNode is the run's node for forager name: the node of that name,
// else one named by a common swarm convention ("forager-<name>",
// "w1-<name>", "lens-<name>"); nil when the run has none.
func (s *runSources) foragerNode(name string) *nodeRecord {
	if nr, ok := s.nodeByName[name]; ok {
		return &nr
	}
	for nodeName, rec := range s.nodeByName {
		if containsForagerName(nodeName, name) {
			return &rec
		}
	}
	return nil
}

// addForager records forager name's model, provider, endpoint, schema
// enforcement and verdict, from its node and its raw comb output.
func (s *runSources) addForager(art *Artifact, name string, raw map[string]string) {
	nr := s.foragerNode(name)
	if nr != nil {
		art.addNodeFacts(name, nr)
	}
	r, hasRaw := raw[name]
	if verdict, ok := foragerVerdict(nr, r, hasRaw); ok {
		art.Verdicts[name] = verdict
	}
}

// addNodeFacts records forager name's model, provider and endpoint from its
// node, and its schema enforcement when the node recorded one.
func (a *Artifact) addNodeFacts(name string, nr *nodeRecord) {
	a.Models[name] = nr.model
	a.Providers[name] = nr.provider
	a.BaseURLs[name] = nr.baseURL
	if nr.enforcement != "" {
		a.SchemaEnforcement[name] = nr.enforcement
	}
}

// foragerVerdict is a forager's verdict JSON, from its node nr (nil when it
// has none) and its comb row's raw output; ok is false when it has neither.
// Source order: the node's persisted outputs (already extracted from fences
// by the runner) when they hold a verdict, then the comb row's raw_json (the
// forager's final text, fence-tolerant), then the node's rationale.
func foragerVerdict(nr *nodeRecord, raw string, hasRaw bool) (map[string]any, bool) {
	if outs := nodeOutputs(nr); hasVerdict(outs) {
		return outs, true
	}
	if hasRaw {
		return parseRationale(raw), true
	}
	if nr != nil {
		return parseRationale(nr.rationale), true
	}
	return nil, false
}

// nodeOutputs is node nr's persisted outputs parsed as JSON; none without a
// node.
func nodeOutputs(nr *nodeRecord) map[string]any {
	if nr == nil {
		return nil
	}
	return parseRationale(nr.outputsJSON)
}

// foragerRawJSON maps each forager whose comb row holds raw output to it.
func foragerRawJSON(combRows []*db.CombRow) map[string]string {
	raw := make(map[string]string, len(combRows))
	for _, r := range combRows {
		if hasRawJSON(r) {
			raw[strings.TrimPrefix(string(r.VantageKey), "forager:")] = r.RawJSON.String
		}
	}
	return raw
}

// hasRawJSON reports whether r is a forager vantage holding raw output.
func hasRawJSON(r *db.CombRow) bool {
	return r.VantageKind == db.VantageForager && r.RawJSON.Valid && r.RawJSON.String != ""
}

// synthesis is the synthesizer's JSON and what wrote it, from the first node
// named as one — "queen", or containing "synth"; neither when the run has
// none.
func (s *runSources) synthesis(raw map[string]string) (map[string]any, *Synthesizer) {
	for name, rec := range s.nodeByName {
		if isSynthesizerNode(name) {
			return synthesisOf(rec, raw), &Synthesizer{Model: rec.model, Provider: rec.provider, BaseURL: rec.baseURL, SchemaEnforcement: rec.enforcement}
		}
	}
	return nil, nil
}

// synthesisOf is the synthesis node rec wrote. The queen returns one JSON
// object, her Markdown in `report`; the runner's extracted outputs carry its
// fields, and win when they hold a verdict, then the queen's raw comb
// output, then the node's rationale.
func synthesisOf(rec nodeRecord, raw map[string]string) map[string]any {
	if outs := parseRationale(rec.outputsJSON); hasVerdict(outs) {
		return outs
	}
	if r, ok := raw["queen"]; ok {
		return parseRationale(r)
	}
	return parseRationale(rec.rationale)
}

// nonNilObject is m, or an empty object when m is nil.
func nonNilObject(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// setOf is the set of names.
func setOf(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// combSnapshot is the comb_state rows stripped of timestamps and sorted by
// vantage_key; a forager's row is kept only when the forager is in inRun.
func combSnapshot(combRows []*db.CombRow, inRun map[string]bool) []CombVantage {
	snapshot := make([]CombVantage, 0, len(combRows))
	for _, r := range combRows {
		if r.VantageKind != db.VantageForager || inRun[strings.TrimPrefix(r.VantageKey, "forager:")] {
			snapshot = append(snapshot, vantageOf(r))
		}
	}
	sort.Slice(snapshot, func(i, j int) bool {
		return snapshot[i].VantageKey < snapshot[j].VantageKey
	})
	return snapshot
}

// vantageOf is comb row r stripped of its timestamps.
func vantageOf(r *db.CombRow) CombVantage {
	dom := ""
	if r.DominantLabel.Valid {
		dom = r.DominantLabel.String
	}
	return CombVantage{
		VantageKey:    r.VantageKey,
		VantageKind:   string(r.VantageKind),
		Narrative:     r.Narrative,
		Confidence:    r.Confidence,
		DominantLabel: dom,
		Contested:     r.Contested,
		EvidenceCount: r.EvidenceCount,
	}
}

// readQuestion extracts the "question" field from the run's inputs_json.
// Falls back to "" when the field is absent or the JSON is malformed.
func readQuestion(store *db.Store, runID int64) (string, error) {
	run, err := store.Workflows().GetWorkflowRun(runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var inputs map[string]any
	if err := json.Unmarshal([]byte(run.InputsJSON), &inputs); err != nil {
		// Malformed JSON — return empty rather than fail.
		return "", nil
	}
	q, _ := inputs["question"].(string)
	return q, nil
}

// readNodeRecords fetches the fields we need from workflow_node_states
// including the rationale column (not exposed by GetWorkflowNodeStates).
func readNodeRecords(store *db.Store, runID int64) ([]nodeRecord, error) {
	// We need a direct read-DB query since WorkflowsRepo.GetWorkflowNodeStates
	// doesn't include rationale.
	rows, err := store.ReadDB.Query(
		`SELECT node_name, node_type, status,
		        COALESCE(resolved_model, ''),
		        COALESCE(provider, ''),
		        COALESCE(base_url, ''),
		        COALESCE(rationale, ''),
		        COALESCE(outputs_json, ''),
		        COALESCE(schema_enforcement, '')
		 FROM workflow_node_states
		 WHERE run_id=?
		 ORDER BY id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("query node states: %w", err)
	}
	defer rows.Close()

	var out []nodeRecord
	for rows.Next() {
		var n nodeRecord
		if err := rows.Scan(&n.nodeName, &n.nodeType, &n.status, &n.model, &n.provider, &n.baseURL, &n.rationale, &n.outputsJSON, &n.enforcement); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// extractForagerInfo walks the comb_state rows to collect forager names and
// their comb vantages. Returns (sorted forager name list, map forager→CombVantage).
func extractForagerInfo(combRows []*db.CombRow) ([]string, map[string]CombVantage) {
	foragerComb := make(map[string]CombVantage)
	for _, r := range combRows {
		if name, ok := foragerOf(r); ok {
			foragerComb[name] = vantageOf(r)
		}
	}
	return slices.Sorted(maps.Keys(foragerComb)), foragerComb
}

// foragerOf is the forager whose vantage comb row r is: the name its
// "forager:<name>" key carries. ok is false for any other row.
func foragerOf(r *db.CombRow) (string, bool) {
	if r.VantageKind != db.VantageForager {
		return "", false
	}
	name := strings.TrimPrefix(string(r.VantageKey), "forager:")
	return name, name != string(r.VantageKey) && name != ""
}

// containsForagerName reports whether the node name is associated with
// the given forager. Handles "forager-<name>", "<name>", "w1-<name>" etc.
func containsForagerName(nodeName, foragerName string) bool {
	// Exact match
	if nodeName == foragerName {
		return true
	}
	// "forager-<name>" or "<prefix>-<name>"
	// Check suffix: the node name ends with "-<foragerName>"
	suffix := "-" + foragerName
	if len(nodeName) > len(suffix) {
		if nodeName[len(nodeName)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

// isSynthesizerNode reports whether the node name indicates a synthesizer
// (typically named "queen" in swarm workflows).
func isSynthesizerNode(nodeName string) bool {
	for _, keyword := range []string{"queen", "synthesizer", "synthesis", "synth"} {
		if contains(nodeName, keyword) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// parseRationale parses a JSON object out of model text. Personas promise
// bare JSON but tiers below their floor wrap it in a code fence or prose, so
// after a direct parse fails it retries on the outermost {…} span — the same
// tolerance workflow.ExtractJSONOutput applies before accept: runs. Text
// that holds no object is {"raw_text": "<rationale>"}, so the artifact is
// always well-formed.
func parseRationale(rationale string) map[string]any {
	if rationale == "" {
		return map[string]any{}
	}
	if parsed, ok := jsonObject(rationale); ok {
		return parsed
	}
	if parsed, ok := jsonObject(outermostBraces(rationale)); ok {
		return parsed
	}
	return map[string]any{"raw_text": rationale}
}

// jsonObject is s decoded as a JSON object; ok is false when s does not
// decode.
func jsonObject(s string) (map[string]any, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

// outermostBraces is the widest {…} span in s, or "" when it has none.
func outermostBraces(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return ""
	}
	return s[i : j+1]
}

func hasVerdict(m map[string]any) bool {
	_, ok := m["verdict"]
	return ok
}
