package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SourcesRepo owns the sources table.
type SourcesRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// NewSourcesRepo binds the two pools.
func NewSourcesRepo(writeDB, readDB *sql.DB) *SourcesRepo {
	return &SourcesRepo{writeDB: writeDB, readDB: readDB}
}

// AddSource registers a source URL and returns its row's id. A URL already
// on record keeps what it has, except that a missing title and a missing
// wave are filled; a nil wave is none. The wave matters to the gate: a row
// `chb validate-sources` wrote without --wave has none, and a source
// registered for wave W afterwards must still count as one of W's sources.
func (r *SourcesRepo) AddSource(url, title, agent string, wave *int, contribution string, primarySource int) (int64, error) {
	var id int64
	err := r.writeDB.QueryRow(
		`INSERT INTO sources (url, title, agent, wave, what_it_contributed, primary_source) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(url) DO UPDATE SET
		   title = COALESCE(NULLIF(sources.title,''), excluded.title),
		   wave  = COALESCE(sources.wave, excluded.wave)
		 RETURNING id`,
		url, title, agent, wave, contribution, primarySource,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add source: %w", err)
	}
	return id, nil
}

var (
	sourceURLRE    = regexp.MustCompile(`https?://[^\s,\]\)>"']+`)
	sourceMDLinkRE = regexp.MustCompile(`\[(?:[^\]]*)\]\((https?://[^\)]+)\)`)
)

// ExtractSourceURLs returns the http(s) URLs in a finding's source_urls — a
// JSON array, Markdown links or bare URLs — deduplicated and sorted, with
// trailing sentence punctuation trimmed. `chb validate-sources` and the gate
// both read citations through it, so the gate looks up exactly the URLs
// validate-sources recorded.
func ExtractSourceURLs(text string) []string {
	if text == "" {
		return nil
	}
	if urls := jsonArrayURLs(text); len(urls) > 0 {
		return sortedKeys(urls)
	}
	return sortedKeys(textURLs(text))
}

// jsonArrayURLs are the URLs in source_urls read as a JSON array of
// strings; nil when it is not one.
func jsonArrayURLs(text string) map[string]bool {
	items, ok := jsonStringArray(text)
	if !ok {
		return nil
	}
	urls := make(map[string]bool)
	for _, item := range items {
		addBareURLs(urls, item)
	}
	return urls
}

// jsonStringArray reads text as a JSON array of strings.
func jsonStringArray(text string) ([]string, bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), "[") {
		return nil, false
	}
	var arr []string
	if json.Unmarshal([]byte(text), &arr) != nil {
		return nil, false
	}
	return arr, true
}

// textURLs are the URLs in text: its Markdown links' targets and its bare
// URLs.
func textURLs(text string) map[string]bool {
	urls := make(map[string]bool)
	for _, m := range sourceMDLinkRE.FindAllStringSubmatch(text, -1) {
		urls[trimURL(m[1])] = true
	}
	addBareURLs(urls, text)
	return urls
}

// addBareURLs adds the bare URLs in text to urls.
func addBareURLs(urls map[string]bool, text string) {
	for _, u := range sourceURLRE.FindAllString(text, -1) {
		urls[trimURL(u)] = true
	}
}

// trimURL trims a URL's trailing sentence punctuation.
func trimURL(u string) string {
	return strings.TrimRight(u, ".,;:")
}

// sortedKeys lists a set's members, sorted.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// WaveURLs returns a wave's sources, sorted: every URL the wave's findings
// cite in source_urls and every source registered for the wave. A nil wave
// means every finding and every registered source. The gate checks this set
// and `chb validate-sources` validates it.
func (r *SourcesRepo) WaveURLs(wave *int) ([]string, error) {
	findingsQ, sourcesQ, args := waveSourceQueries(wave)
	set := make(map[string]bool)
	if err := r.collectSources(set, findingsQ, args, true); err != nil {
		return nil, err
	}
	if err := r.collectSources(set, sourcesQ, args, false); err != nil {
		return nil, err
	}
	return sortedKeys(set), nil
}

// waveSourceQueries are the queries for a wave's cited URLs and registered
// sources, and their arguments.
func waveSourceQueries(wave *int) (string, string, []any) {
	findingsQ, sourcesQ := `SELECT source_urls FROM findings WHERE source_urls IS NOT NULL`, `SELECT url FROM sources`
	if wave == nil {
		return findingsQ, sourcesQ, nil
	}
	return findingsQ + ` AND wave = ?`, sourcesQ + ` WHERE wave = ?`, []any{*wave}
}

// collectSources adds the values a query returns to set: the URLs each
// cites when extract is set, else each value as it is.
func (r *SourcesRepo) collectSources(set map[string]bool, q string, args []any, extract bool) error {
	values, err := r.readStrings(q, args)
	if err != nil {
		return fmt.Errorf("read the wave's sources: %w", err)
	}
	for _, v := range values {
		addSource(set, v, extract)
	}
	return nil
}

// readStrings runs a one-column query and reads every value.
func (r *SourcesRepo) readStrings(q string, args []any) ([]string, error) {
	rows, err := r.readDB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		values = append(values, s)
	}
	return values, rows.Err()
}

// addSource adds one value to set: the URLs it cites when extract is set,
// else the value itself.
func addSource(set map[string]bool, v string, extract bool) {
	if !extract {
		set[v] = true
		return
	}
	for _, u := range ExtractSourceURLs(v) {
		set[u] = true
	}
}
