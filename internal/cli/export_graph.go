package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/mss"
	"github.com/spf13/cobra"
)

// ─── chb export-graph (export-graph.py) ─────────────────────

func newExportGraphCmd() *cobra.Command {
	var outFile string
	var mermaid bool
	var compact bool

	cmd := &cobra.Command{
		Use:   "export-graph",
		Short: "Export the full execution graph as JSON or Mermaid",
		RunE: func(cmd *cobra.Command, args []string) error {
			graph, err := buildExecGraph()
			if err != nil {
				return err
			}
			if compact {
				compactGraphFindings(graph)
			}
			return writeExecGraph(renderExecGraph(graph, mermaid), outFile)
		},
	}
	cmd.Flags().StringVar(&outFile, "out", "", "output file path (default: stdout)")
	cmd.Flags().BoolVar(&mermaid, "mermaid", false, "output Mermaid flowchart instead of JSON")
	cmd.Flags().BoolVar(&compact, "compact", false, "truncate finding text to 120 bytes in the JSON")
	return cmd
}

// compactGraphFindings cuts each finding node's text with compactFinding.
func compactGraphFindings(graph map[string]any) {
	for _, node := range graph["nodes"].([]map[string]any) {
		if node["type"] != "finding" {
			continue
		}
		if data, ok := node["data"].(map[string]any); ok {
			compactFindingData(data)
		}
	}
}

// compactFindingData cuts a finding row's text, when it has one.
func compactFindingData(data map[string]any) {
	if f, ok := data["finding"].(string); ok {
		data["finding"] = compactFinding(f)
	}
}

// renderExecGraph renders the graph as a Mermaid flowchart or as indented
// JSON.
func renderExecGraph(graph map[string]any, mermaid bool) string {
	if mermaid {
		return graphToMermaid(graph)
	}
	b, _ := json.MarshalIndent(graph, "", "  ")
	return string(b)
}

// writeExecGraph writes the rendered graph to outFile and says so on
// stderr, or prints it when outFile is empty.
func writeExecGraph(output, outFile string) error {
	if outFile == "" {
		fmt.Println(output)
		return nil
	}
	if err := os.WriteFile(outFile, []byte(output), 0644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Written to %s\n", outFile)
	return nil
}

// compactFinding cuts finding text longer than 120 bytes to at most 120
// bytes and appends "...". The cut backs off to a rune boundary (clip), as
// the label truncation in buildExecGraph does.
func compactFinding(f string) string {
	if len(f) <= 120 {
		return f
	}
	return clip(f, 120) + "..."
}

// execGraph accumulates the export's nodes and edges. Both start
// initialised, not nil: a nil slice marshals to JSON null, which a reader
// iterating the graph's arrays fails on, so an empty or fresh database
// exports empty arrays. err is the first read that failed.
type execGraph struct {
	nodes []map[string]any
	edges []map[string]any
	err   error
}

// query reads the rows of q for the export. The first read that fails is
// kept in g.err, and every read after it returns no rows, so the export
// fails on it instead of writing a graph without that table's part as
// though it were whole.
func (g *execGraph) query(rdb *sql.DB, q string) []map[string]any {
	if g.err != nil {
		return nil
	}
	var rows []map[string]any
	rows, g.err = db.QueryToMaps(rdb, q)
	return rows
}

// count reads one count for the export. As with query, the first read that
// fails is kept in g.err, and every read after it returns 0.
func (g *execGraph) count(rdb *sql.DB, q string, args ...any) int {
	if g.err != nil {
		return 0
	}
	var n int
	g.err = rdb.QueryRow(q, args...).Scan(&n)
	return n
}

// addNode appends a node.
func (g *execGraph) addNode(id, nodeType, label string, data any) {
	g.nodes = append(g.nodes, map[string]any{
		"id":    id,
		"type":  nodeType,
		"label": label,
		"data":  data,
	})
}

// addEdge appends an unlabelled edge.
func (g *execGraph) addEdge(source, target, edgeType string) {
	g.edges = append(g.edges, map[string]any{
		"source": source,
		"target": target,
		"type":   edgeType,
	})
}

func buildExecGraph() (map[string]any, error) {
	rdb := store.ReadDB
	g := &execGraph{nodes: []map[string]any{}, edges: []map[string]any{}}
	g.addDimensions(rdb)
	waveNums := g.addWaves(rdb)
	agentCount := g.addAgents(rdb)
	findingCount := g.addFindings(rdb)
	conflictCount := g.addConflicts(rdb)
	evalCount := g.addEvaluations(rdb)
	gates := g.addGates(rdb)
	mssCounts := g.mssCounts(rdb)
	if g.err != nil {
		return nil, fmt.Errorf("read the graph: %w", g.err)
	}

	stats := map[string]any{
		"total_findings":    findingCount,
		"total_agents":      agentCount,
		"total_waves":       len(waveNums),
		"total_conflicts":   conflictCount,
		"total_evaluations": evalCount,
		"total_gates":       len(gates),
		"gates_open":        execGraphOpenGates(gates),
		"mss_counts":        mssCounts,
	}

	return map[string]any{
		"metadata":   map[string]any{"generated_at": time.Now().Format(time.RFC3339), "db_path": store.Path},
		"statistics": stats,
		"nodes":      g.nodes,
		"edges":      g.edges,
	}, nil
}

// addDimensions adds a node per registered dimension.
func (g *execGraph) addDimensions(rdb *sql.DB) {
	for _, d := range g.query(rdb, "SELECT * FROM dimensions ORDER BY id") {
		g.addNode(fmt.Sprintf("dim-%v", d["id"]), "dimension",
			fmt.Sprintf("d%v: %v", d["id"], d["name"]), map[string]any{"name": d["name"]})
	}
}

// addWaves adds a node per wave that has findings, each linked to the next,
// and returns the waves in order.
func (g *execGraph) addWaves(rdb *sql.DB) []int {
	var waveNums []int
	for _, w := range g.query(rdb, "SELECT DISTINCT wave FROM findings ORDER BY wave") {
		waveNums = append(waveNums, int(toInt64CLI(w["wave"])))
	}
	for _, w := range waveNums {
		cnt := g.count(rdb, "SELECT COUNT(*) FROM findings WHERE wave=?", w)
		g.addNode(fmt.Sprintf("wave-%d", w), "wave", fmt.Sprintf("Wave %d", w),
			map[string]any{"wave": w, "finding_count": cnt})
	}
	for i := 0; i < len(waveNums)-1; i++ {
		g.addEdge(fmt.Sprintf("wave-%d", waveNums[i]), fmt.Sprintf("wave-%d", waveNums[i+1]), "sequence")
	}
	return waveNums
}

// addAgents adds a node per agent, dispatched from the first wave it wrote
// findings in, and returns how many agents there are.
func (g *execGraph) addAgents(rdb *sql.DB) int {
	agentSeen := make(map[string]bool)
	for _, af := range g.query(rdb, "SELECT agent, wave, COUNT(*) as cnt FROM findings GROUP BY agent, wave ORDER BY wave, agent") {
		name := anyStr(af["agent"])
		if agentSeen[name] {
			continue
		}
		agentSeen[name] = true
		w := int(toInt64CLI(af["wave"]))
		g.addNode(fmt.Sprintf("agent-%s", name), "agent", name,
			map[string]any{"wave": w, "finding_count": af["cnt"]})
		g.addEdge(fmt.Sprintf("wave-%d", w), fmt.Sprintf("agent-%s", name), "dispatched")
	}
	return len(agentSeen)
}

// addFindings adds a node per finding and returns how many there are.
func (g *execGraph) addFindings(rdb *sql.DB) int {
	findings := g.query(rdb, "SELECT * FROM findings ORDER BY wave, d1, d2, id")
	for _, f := range findings {
		g.addFinding(f)
	}
	return len(findings)
}

// addFinding adds a finding's node, the edge from the agent that produced
// it, labelled with its MSS label, and an edge from each finding it depends
// on.
func (g *execGraph) addFinding(f map[string]any) {
	fid := toInt64CLI(f["id"])
	// Back off to a rune boundary, so the cut splits no multi-byte rune.
	finding := clip(anyStr(f["finding"]), 80)
	g.addNode(fmt.Sprintf("finding-%d", fid), "finding", fmt.Sprintf("F%d: %s...", fid, finding), f)
	g.edges = append(g.edges, map[string]any{
		"source": fmt.Sprintf("agent-%s", anyStr(f["agent"])),
		"target": fmt.Sprintf("finding-%d", fid),
		"type":   "produced",
		"label":  anyStr(f["mss_label"]),
	})
	g.addDependsOnEdges(fid, anyStr(f["depends_on_ids"]))
}

// addDependsOnEdges adds an edge from each finding a depends_on_ids list
// names; a list that does not parse adds none.
func (g *execGraph) addDependsOnEdges(fid int64, deps string) {
	if deps == "" {
		return
	}
	var depIDs []int64
	if json.Unmarshal([]byte(deps), &depIDs) != nil {
		return
	}
	for _, depID := range depIDs {
		g.addEdge(fmt.Sprintf("finding-%d", depID), fmt.Sprintf("finding-%d", fid), "depends_on")
	}
}

// addConflicts adds a node per conflict and returns how many there are.
func (g *execGraph) addConflicts(rdb *sql.DB) int {
	conflicts := g.query(rdb, "SELECT * FROM conflicts ORDER BY wave, id")
	for _, c := range conflicts {
		g.addConflict(c)
	}
	return len(conflicts)
}

// addConflict adds a conflict's node, open or resolved, and an edge to each
// finding it names.
func (g *execGraph) addConflict(c map[string]any) {
	cid := toInt64CLI(c["id"])
	label := "OPEN"
	if c["resolution"] != nil {
		label = "RESOLVED"
	}
	g.addNode(fmt.Sprintf("conflict-%d", cid), "conflict", fmt.Sprintf("Conflict %d: %s", cid, label), c)
	for _, key := range []string{"finding_a_id", "finding_b_id"} {
		if id := toInt64CLI(c[key]); id > 0 {
			g.addEdge(fmt.Sprintf("conflict-%d", cid), fmt.Sprintf("finding-%d", id), "conflicts_with")
		}
	}
}

// addEvaluations adds a node per wave evaluation and returns how many there
// are.
func (g *execGraph) addEvaluations(rdb *sql.DB) int {
	evals := g.query(rdb, "SELECT * FROM evaluations ORDER BY wave, id")
	for _, e := range evals {
		eid := toInt64CLI(e["id"])
		g.addNode(fmt.Sprintf("eval-%d", eid), "evaluation",
			fmt.Sprintf("Evaluation Wave %v: %v", e["wave"], e["verdict"]), e)
	}
	return len(evals)
}

// addGates adds a node per wave gate, linked from the evaluation it read,
// and returns the gate rows.
func (g *execGraph) addGates(rdb *sql.DB) []map[string]any {
	gates := g.query(rdb, "SELECT * FROM wave_gates ORDER BY wave")
	for _, row := range gates {
		w := toInt64CLI(row["wave"])
		g.addNode(fmt.Sprintf("gate-w%d", w), "gate", fmt.Sprintf("Gate Wave %d", w), row)
		if evalID := toInt64CLI(row["evaluation_id"]); evalID > 0 {
			g.addEdge(fmt.Sprintf("eval-%d", evalID), fmt.Sprintf("gate-w%d", w), "gates")
		}
	}
	return gates
}

// mssCounts counts the findings per MSS label.
func (g *execGraph) mssCounts(rdb *sql.DB) map[string]int {
	mssCounts := map[string]int{}
	for _, r := range g.query(rdb, "SELECT mss_label, COUNT(*) as cnt FROM findings GROUP BY mss_label") {
		mssCounts[anyStr(r["mss_label"])] = int(toInt64CLI(r["cnt"]))
	}
	return mssCounts
}

// execGraphOpenGates counts the gates whose agents completed and whose
// conflict check ran.
func execGraphOpenGates(gates []map[string]any) int {
	gatesOpen := 0
	for _, g := range gates {
		if toInt64CLI(g["agents_completed"]) > 0 && g["conflict_check_passed"] != nil {
			gatesOpen++
		}
	}
	return gatesOpen
}

// mermaidID turns a node id into a Mermaid identifier: every rune but an
// ASCII letter or digit becomes '_', so an agent named "researcher wifi" or
// "analyst[1]" still yields Mermaid that renders.
func mermaidID(raw string) string {
	id := strings.Map(mermaidIDRune, raw)
	if id == "" {
		return "n_"
	}
	// Mermaid ids may not start with a digit.
	if id[0] >= '0' && id[0] <= '9' {
		return "n_" + id
	}
	return id
}

// mermaidIDRune keeps an ASCII letter or digit and turns any other rune
// into '_'.
func mermaidIDRune(r rune) rune {
	if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
		return r
	}
	return '_'
}

func graphToMermaid(graph map[string]any) string {
	var lines []string
	lines = append(lines, "flowchart TD")
	// The classDefs colour each node type, a finding by its MSS label and
	// a gate by whether it passed.
	lines = append(lines, mermaidClassDefs()...)
	lines = append(lines, "")

	nodes, _ := graph["nodes"].([]map[string]any)
	for _, n := range nodes {
		lines = append(lines, mermaidNodeLines(n)...)
	}

	lines = append(lines, "")
	edges, _ := graph["edges"].([]map[string]any)
	lines = append(lines, mermaidEdgeLines(edges)...)

	return strings.Join(lines, "\n")
}

// mermaidNodeLines are a node's shape line and its class line.
func mermaidNodeLines(n map[string]any) []string {
	id := mermaidID(anyStr(n["id"]))
	ntype := anyStr(n["type"])
	data, _ := n["data"].(map[string]any)
	return []string{
		mermaidNodeShape(ntype, id, mermaidLabel(anyStr(n["label"]))),
		fmt.Sprintf("    class %s %s", id, mermaidClass(ntype, data)),
	}
}

// mermaidLabel is a node label cut to 70 bytes, its double quotes made
// single.
func mermaidLabel(label string) string {
	return strings.ReplaceAll(clip(label, 70), `"`, "'")
}

// mermaidNodeShape draws a gate as a hexagon, a conflict as a rhombus and
// anything else as a rectangle.
func mermaidNodeShape(nodeType, id, label string) string {
	switch nodeType {
	case "gate":
		return fmt.Sprintf(`    %s{{"%s"}}`, id, label)
	case "conflict":
		return fmt.Sprintf(`    %s{"%s"}`, id, label)
	default:
		return fmt.Sprintf(`    %s["%s"]`, id, label)
	}
}

// mermaidEdgeLines are the edges' lines, one per distinct source and target.
func mermaidEdgeLines(edges []map[string]any) []string {
	var lines []string
	seen := make(map[string]bool)
	for _, e := range edges {
		src := mermaidID(anyStr(e["source"]))
		tgt := mermaidID(anyStr(e["target"]))
		key := src + "->" + tgt
		if seen[key] {
			continue
		}
		seen[key] = true
		// Every "produced" edge is drawn: it ties a finding node to the
		// agent that produced it, the one relationship the graph exists to
		// show. The count is bounded by the finding nodes already being
		// printed.
		lines = append(lines, mermaidEdgeLine(src, tgt, anyStr(e["label"])))
	}
	return lines
}

// mermaidEdgeLine draws one edge, with its label when it has one.
func mermaidEdgeLine(src, tgt, label string) string {
	if label != "" {
		return fmt.Sprintf("    %s -->|%s| %s", src, label, tgt)
	}
	return fmt.Sprintf("    %s --> %s", src, tgt)
}

// mermaidNodeTones colour the Mermaid export's node types, in classDef
// emission order; a finding's classDef takes its MSS label's solid colour.
var mermaidNodeTones = []struct{ Name, Fill string }{
	{"wave", "#264653"},
	{"agent", "#56667A"},
	{"process", "#5F6770"},
	{"evaluation", "#6E2C8C"},
	{"gate", "#23724A"},
	{"gate_blocked", "#B3261E"},
	{"synthesis", "#1D6A85"},
	{"conflict", "#B3261E"},
	{"dimension", "#6A7078"},
	{"finding", "#6A7078"},
	{"signal", "#9A3677"},
}

// mermaidInk is the stroke drawn around every node.
const mermaidInk = "#2A1F10"

// Ink colours drawn on a solid fill; inkOn picks whichever contrasts more.
const (
	inkDark  = "#110A02"
	inkLight = "#FFFFFF"
)

// mermaidClassDefs returns the classDef lines of the Mermaid export: one
// per graph node type and one per MSS label for findings.
func mermaidClassDefs() []string {
	var out []string
	for _, n := range mermaidNodeTones {
		out = append(out, fmt.Sprintf("    classDef %s fill:%s,stroke:%s,color:%s", n.Name, n.Fill, mermaidInk, inkOn(n.Fill)))
	}
	for _, l := range mss.OrderedLabels {
		solid := mss.LabelColors[l].Solid
		out = append(out, fmt.Sprintf("    classDef finding_%s fill:%s,stroke:%s,color:%s", string(l), solid, mermaidInk, inkOn(solid)))
	}
	return out
}

// mermaidClass names the classDef a node is drawn with: a finding by its
// MSS label, a gate by whether it passed, anything else by its type.
func mermaidClass(nodeType string, data map[string]any) string {
	switch nodeType {
	case "finding":
		return findingMermaidClass(data)
	case "gate":
		return gateMermaidClass(data)
	}
	return nodeType
}

// findingMermaidClass is finding_<label> for a finding with a valid MSS
// label, else finding.
func findingMermaidClass(data map[string]any) string {
	if l, _ := data["mss_label"].(string); mss.Label(l).Valid() {
		return "finding_" + l
	}
	return "finding"
}

// gateMermaidClass is gate for a gate that passed, else gate_blocked.
func gateMermaidClass(data map[string]any) string {
	if gatePassed(data) {
		return "gate"
	}
	return "gate_blocked"
}

// gateChecks are the wave_gates columns a gate node's data carries.
var gateChecks = []string{"mss_audit_passed", "source_check_passed", "conflict_check_passed", "agents_completed"}

// gatePassed reports whether a gate node passed: its all_pass is true, or
// it carries the wave_gates check columns and every one is set. A gate
// opened with --force past a failing check has one unset.
func gatePassed(data map[string]any) bool {
	if v, ok := data["all_pass"].(bool); ok {
		return v
	}
	return gateChecksPass(data)
}

// gateChecksPass reports whether data carries any wave_gates check column
// and every one it carries is set.
func gateChecksPass(data map[string]any) bool {
	seen := false
	for _, k := range gateChecks {
		v, ok := data[k]
		if !ok {
			continue
		}
		seen = true
		if !truthy(v) {
			return false
		}
	}
	return seen
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

// inkOn returns the ink colour with the higher WCAG contrast on hex.
func inkOn(hex string) string {
	if contrast(hex, inkLight) >= contrast(hex, inkDark) {
		return inkLight
	}
	return inkDark
}

// contrast is the WCAG 2 contrast ratio of two #RRGGBB colours, 1 to 21.
// A malformed colour counts as black.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(hex string) float64 {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		return 0
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0
	}
	ch := func(shift uint) float64 {
		c := float64((v>>shift)&0xFF) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(16) + 0.7152*ch(8) + 0.0722*ch(0)
}
