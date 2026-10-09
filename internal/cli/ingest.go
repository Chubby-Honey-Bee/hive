package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// ─── chb ingest (ingest-agent-output.py) ────────────────────

var markerHeadRE = regexp.MustCompile(`<!--\s*(FINDING|GAP|FOLLOWUP):`)

func newIngestCmd() *cobra.Command {
	var o ingestOptions
	cmd := &cobra.Command{
		Use:   "ingest [file]",
		Short: "Parse agent output for structured markers and ingest into DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.ErrOrStderr(), args)
		},
	}
	cmd.Flags().IntVar(&o.wave, "wave", 0, "wave number")
	cmd.Flags().StringVar(&o.agent, "agent", "", "agent name")
	cmd.Flags().StringVar(&o.dir, "dir", "", "directory of agent output files")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "parse and display without writing")
	cmd.MarkFlagRequired("wave")
	cmd.MarkFlagRequired("agent")
	return cmd
}

// ingestOptions holds the flags of chb ingest.
type ingestOptions struct {
	wave   int
	agent  string
	dir    string
	dryRun bool
}

// ingestText is one piece of agent output and where it came from.
type ingestText struct{ source, text string }

// ingestTally counts what an ingest did with the markers it found.
type ingestTally struct {
	inserted    int
	writeErrors int
	unparseable int
	markers     int
}

// run ingests the markers in the agent output: every .txt and .md file in
// --dir, else the file named, else stdin.
func (o *ingestOptions) run(stderr io.Writer, args []string) error {
	texts, err := o.readTexts(args)
	if err != nil {
		return err
	}
	var tally ingestTally
	for _, t := range texts {
		o.ingestOne(stderr, t, &tally)
	}
	if !o.dryRun {
		fmt.Printf("\nTotal: %d items ingested, %d errors\n", tally.inserted, tally.writeErrors+tally.unparseable)
	}
	return tally.err()
}

// readTexts reads the agent output to ingest.
func (o *ingestOptions) readTexts(args []string) ([]ingestText, error) {
	if o.dir != "" {
		return readIngestDir(o.dir)
	}
	if len(args) > 0 {
		content, err := os.ReadFile(args[0])
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		return []ingestText{{args[0], string(content)}}, nil
	}
	content, _ := io.ReadAll(os.Stdin)
	return []ingestText{{"stdin", string(content)}}, nil
}

// readIngestDir reads every .txt and .md file in dir.
func readIngestDir(dir string) ([]ingestText, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var texts []ingestText
	for _, e := range entries {
		if t, ok := readIngestFile(dir, e.Name()); ok {
			texts = append(texts, t)
		}
	}
	return texts, nil
}

// readIngestFile reads name in dir when it is a .txt or .md file. A file
// that does not read is skipped.
func readIngestFile(dir, name string) (ingestText, bool) {
	if !strings.HasSuffix(name, ".txt") && !strings.HasSuffix(name, ".md") {
		return ingestText{}, false
	}
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ingestText{}, false
	}
	return ingestText{name, string(content)}, true
}

// ingestOne reports the markers in one text and, unless --dry-run, writes
// them.
func (o *ingestOptions) ingestOne(stderr io.Writer, t ingestText, tally *ingestTally) {
	items, bad := parseMarkers(t.text)
	if len(items) == 0 && len(bad) == 0 {
		fmt.Printf("[%s] No structured markers found.\n", t.source)
		return
	}
	fmt.Printf("[%s] Found %d markers\n", t.source, len(items)+len(bad))
	reportBadMarkers(stderr, t.source, bad)
	tally.unparseable += len(bad)
	tally.markers += len(items) + len(bad)
	if o.dryRun {
		previewMarkers(items)
		return
	}
	o.writeMarkers(items, tally)
}

// reportBadMarkers names on stderr each marker of source that did not parse.
func reportBadMarkers(stderr io.Writer, source string, bad []badMarker) {
	for _, b := range bad {
		fmt.Fprintf(stderr, "[%s] unparseable %s marker, skipped: %s\n", source, b.kind, b.excerpt)
	}
}

// previewMarkers prints each marker as --dry-run shows it.
func previewMarkers(items []map[string]any) {
	for _, item := range items {
		b, _ := json.Marshal(item)
		fmt.Printf("  [%s] %s\n", anyStr(item["_type"]), clip(string(b), 200))
	}
}

// writeMarkers writes each marker, with the flags' wave and agent where it
// names none.
func (o *ingestOptions) writeMarkers(items []map[string]any, tally *ingestTally) {
	for _, item := range items {
		itemType, _ := item["_type"].(string)
		delete(item, "_type")
		o.defaultMarkerFields(item)
		writeMarker(itemType, item, tally)
	}
}

// defaultMarkerFields gives a marker the flags' wave and agent where it
// names none.
func (o *ingestOptions) defaultMarkerFields(item map[string]any) {
	if _, ok := item["wave"]; !ok {
		item["wave"] = float64(o.wave)
	}
	if _, ok := item["agent"]; !ok {
		item["agent"] = o.agent
	}
}

// writeMarker writes one marker by its type and counts the outcome.
func writeMarker(itemType string, item map[string]any, tally *ingestTally) {
	switch itemType {
	case "finding":
		tally.record(itemType, writeFindingMarker(item))
	case "gap":
		tally.record(itemType, ingestGap(item))
	case "followup":
		tally.record(itemType, ingestFollowup(item))
	}
}

// writeFindingMarker writes a FINDING marker and prints the new finding's
// id and the start of its text.
func writeFindingMarker(item map[string]any) error {
	b, _ := json.Marshal(item)
	fid, err := ingestFinding(b)
	if err != nil {
		return err
	}
	fmt.Printf("  Finding id=%d: %s...\n", fid, clip(anyStr(item["finding"]), 80))
	return nil
}

// record counts one marker write, printing the error of one that failed.
func (t *ingestTally) record(itemType string, err error) {
	if err != nil {
		fmt.Printf("  ERROR writing %s: %v\n", itemType, err)
		t.writeErrors++
		return
	}
	t.inserted++
}

// err fails the ingest when a marker failed to parse or to write, so a
// coordinator ingesting agent output, and `chb validate`, whose ingest
// assertion checks the exit status, can tell a clean ingest from one that
// dropped findings. The markers that did write are kept.
func (t *ingestTally) err() error {
	if t.writeErrors > 0 || t.unparseable > 0 {
		return fmt.Errorf("%d of %d markers failed: %d unparseable, %d failed to write",
			t.writeErrors+t.unparseable, t.markers, t.unparseable, t.writeErrors)
	}
	return nil
}

// badMarker is a marker ingest could not read: its kind and the start of
// its body.
type badMarker struct{ kind, excerpt string }

// parseMarkers returns the markers whose body parses as a JSON object, and
// reports every other FINDING, GAP or FOLLOWUP marker as unparseable, so a
// marker whose JSON does not parse is never dropped in silence.
func parseMarkers(text string) ([]map[string]any, []badMarker) {
	var items []map[string]any
	var bad []badMarker
	for {
		m, rest, ok := nextMarker(text)
		if !ok {
			return items, bad
		}
		text = rest
		if data := m.parse(); data != nil {
			items = append(items, data)
		} else {
			bad = append(bad, badMarker{m.kind, markerExcerpt(m.body)})
		}
	}
}

// markerSpan is one marker as it stands in the text: its kind, its body, and
// whether a --> closed it.
type markerSpan struct {
	kind   string
	body   string
	closed bool
}

// nextMarker returns the first marker in text and the text after it, or
// false when text holds none.
//
// A marker is an HTML comment, so its body ends at the first --> after its
// head. It also ends where the next marker's head starts, so a marker that
// lost its --> does not swallow the next one; such a marker, like one that
// reaches the end of the text, is unparseable. Ending the body at the next
// }--> instead would let a marker with no closing brace run on through the
// next marker and lose the valid marker inside that span.
func nextMarker(text string) (markerSpan, string, bool) {
	h := markerHeadRE.FindStringSubmatchIndex(text)
	if h == nil {
		return markerSpan{}, "", false
	}
	kind := strings.ToLower(text[h[2]:h[3]])
	text = text[h[1]:]
	end, next, closed := len(text), len(text), false
	if i := strings.Index(text, "-->"); i >= 0 {
		end, next, closed = i, i+len("-->"), true
	}
	if n := markerHeadRE.FindStringIndex(text[:end]); n != nil {
		end, next, closed = n[0], n[0], false
	}
	return markerSpan{kind: kind, body: strings.TrimSpace(text[:end]), closed: closed}, text[next:], true
}

// parse reads the marker's body as a JSON object tagged with the marker's
// kind under _type, or returns nil for a marker that is unclosed or does
// not parse.
func (m markerSpan) parse() map[string]any {
	if !m.closed {
		return nil
	}
	data := tryParseJSON(m.body)
	if data != nil {
		data["_type"] = m.kind
	}
	return data
}

// markerExcerpt is the start of a marker body on one line, at most 60
// characters.
func markerExcerpt(body string) string {
	r := []rune(strings.Join(strings.Fields(body), " "))
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return string(r)
}

func tryParseJSON(raw string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) == nil {
		return m
	}
	// Try unescaping
	unescaped := strings.ReplaceAll(raw, `\"`, `"`)
	if json.Unmarshal([]byte(unescaped), &m) == nil {
		return m
	}
	return nil
}

// findingMarkerKeys and gapMarkerKeys are the integer and text fields of a
// FINDING and a GAP marker, which chb ingest holds to their types
// (db.CheckFieldTypes) before it writes the marker; a FOLLOWUP marker's are
// followupKeys.
var (
	findingMarkerKeys = writeKeys{
		integer: []string{"wave", "d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"},
		text:    []string{"agent", "mss_label", "finding", "evidence"},
	}
	gapMarkerKeys = writeKeys{
		integer: []string{"wave", "d1", "d2", "d3", "d4"},
		text:    []string{"agent", "description", "priority"},
	}
)

// ingestFinding writes the finding a FINDING marker's JSON describes and
// returns its id.
func ingestFinding(jsonData []byte) (int64, error) {
	var body map[string]any
	_ = json.Unmarshal(jsonData, &body)
	if err := db.CheckFieldTypes(body, findingMarkerKeys.integer, findingMarkerKeys.text); err != nil {
		return 0, err
	}
	f := markerFinding(body)
	if err := addFindingLinks(f, body); err != nil {
		return 0, err
	}
	return store.Findings().AddFinding(f)
}

// markerFinding is the finding a FINDING marker's fields describe, without
// its sources and dependencies.
func markerFinding(body map[string]any) *db.Finding {
	f := &db.Finding{
		Wave:     intFromMap(body, "wave"),
		Agent:    stringFromMap(body, "agent"),
		MSSLabel: stringFromMap(body, "mss_label"),
		Finding:  stringFromMap(body, "finding"),
		D1:       intPtrFromMap(body, "d1"),
		D2:       intPtrFromMap(body, "d2"),
		D3:       intPtrFromMap(body, "d3"),
		D4:       intPtrFromMap(body, "d4"),
		D5:       intPtrFromMap(body, "d5"),
		D6:       intPtrFromMap(body, "d6"),
		D7:       intPtrFromMap(body, "d7"),
		D8:       intPtrFromMap(body, "d8"),
	}
	if v := stringFromMap(body, "evidence"); v != "" {
		f.Evidence = &v
	}
	return f
}

// addFindingLinks sets the finding's source URLs and dependencies from the
// marker through db.Normalize*, the one definition every write surface uses,
// so a JSON-array source_urls is stored as the citation subsystem reads it.
func addFindingLinks(f *db.Finding, body map[string]any) error {
	su, err := db.NormalizeSourceURLs(body["source_urls"])
	if err != nil {
		return err
	}
	f.SourceURLs = su
	deps, err := db.NormalizeDependsOnIDs(body["depends_on_ids"])
	if err != nil {
		return err
	}
	if deps != "" {
		f.DependsOnIDs = &deps
	}
	return nil
}

// ingestGap writes the gap a GAP marker describes.
func ingestGap(item map[string]any) error {
	if err := db.CheckFieldTypes(item, gapMarkerKeys.integer, gapMarkerKeys.text); err != nil {
		return err
	}
	return store.Gaps().AddGap(
		intFromMap(item, "wave"),
		stringFromMap(item, "agent"),
		stringFromMap(item, "description"),
		stringFromMap(item, "priority"),
		intPtrFromMap(item, "d1"),
		intPtrFromMap(item, "d2"),
		intPtrFromMap(item, "d3"),
		intPtrFromMap(item, "d4"),
	)
}

// ingestFollowup writes the follow-up a FOLLOWUP marker describes.
func ingestFollowup(item map[string]any) error {
	if err := db.CheckFieldTypes(item, followupKeys.integer, followupKeys.text); err != nil {
		return err
	}
	return store.Followups().AddFollowup(
		intFromMap(item, "wave"),
		stringFromMap(item, "agent"),
		stringFromMap(item, "question"),
		stringFromMap(item, "priority"),
		intPtrFromMap(item, "d1"),
		intPtrFromMap(item, "d2"),
		intPtrFromMap(item, "d3"),
		intPtrFromMap(item, "d4"),
	)
}
