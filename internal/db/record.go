package db

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// recordKinds are the kinds WriteRecord writes, each with its write.
var recordKinds = map[string]func(*Store, map[string]any) (int64, string, error){
	"finding":          writeFinding,
	"gap":              writeGap,
	"source":           writeSource,
	"resolve_gap":      writeResolveGap,
	"resolve_conflict": writeResolveConflict,
}

// WriteRecord writes fields as a record of kind — finding, gap, source,
// resolve_gap or resolve_conflict — and returns the id of the row it wrote
// or closed and the line that reports it. It is the one write path for those
// kinds: chb_db_write, in the runner and over MCP, and `chb db-write` all
// pass through it, so each surface decodes, checks and writes a record the
// same way.
func (s *Store) WriteRecord(kind string, fields map[string]any) (id int64, line string, err error) {
	if fields == nil {
		return 0, "", fmt.Errorf("fields missing")
	}
	write, ok := recordKinds[kind]
	if !ok {
		return 0, "", fmt.Errorf("unknown kind %q", kind)
	}
	return write(s, fields)
}

// checkRecordFields runs the refusals every kind shares on its fields f: a
// field the kind does not read, then a field of the wrong type
// (CheckFieldTypes).
func checkRecordFields(kind string, f map[string]any, accepted, integer, text []string) error {
	if err := checkFields(kind, f, accepted); err != nil {
		return err
	}
	return CheckFieldTypes(f, integer, text)
}

// CheckFieldTypes refuses fields f when one of the integer fields holds no
// number, a fraction, or one outside int's range, or one of the text fields
// holds anything but a string. A field set to null is not set. Every write
// surface holds a record to it: WriteRecord its kinds, `chb db-write` its
// other kinds and `chb ingest` its markers.
func CheckFieldTypes(f map[string]any, integer, text []string) error {
	if err := checkNumeric(f, integer); err != nil {
		return err
	}
	if err := checkWhole(f, integer); err != nil {
		return err
	}
	return checkText(f, text)
}

// checkText refuses fields whose text entries hold anything but a string,
// which reqStr and optStr would read as "" or nil ({"agent":5}).
func checkText(f map[string]any, keys []string) error {
	if bad := nonTextFields(f, keys); len(bad) > 0 {
		return fmt.Errorf("field(s) must be strings: %s", strings.Join(bad, ", "))
	}
	return nil
}

// nonTextFields are "key=value" for each of keys that f sets to something
// other than a string, in keys' order.
func nonTextFields(f map[string]any, keys []string) []string {
	var bad []string
	for _, k := range keys {
		if v := f[k]; v != nil && !isString(v) {
			bad = append(bad, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return bad
}

// isString reports whether v is a string.
func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// checkFields refuses a field the kind does not read, naming it and the
// kind's fields, so a typo ("agent_id" for "agent") is not written without
// the field it meant.
func checkFields(kind string, f map[string]any, accepted []string) error {
	unknown := unknownFields(f, accepted)
	if len(unknown) == 0 {
		return nil
	}
	noun := "field"
	if len(unknown) > 1 {
		noun = "fields"
	}
	return fmt.Errorf("%s: unknown %s %s; accepted fields: %s",
		kind, noun, strings.Join(unknown, ", "), strings.Join(accepted, ", "))
}

// unknownFields are f's fields that accepted does not name, quoted and
// sorted.
func unknownFields(f map[string]any, accepted []string) []string {
	var unknown []string
	for k := range f {
		if !slices.Contains(accepted, k) {
			unknown = append(unknown, strconv.Quote(k))
		}
	}
	slices.Sort(unknown)
	return unknown
}

// checkNumeric refuses fields whose numeric entries hold something optInt
// cannot read, which would otherwise be written as 0 ({"wave":"3"}) or NULL
// ({"d1":"2"}).
func checkNumeric(f map[string]any, keys []string) error {
	if bad := nonNumericFields(f, keys); len(bad) > 0 {
		return fmt.Errorf("field(s) must be numeric: %s", strings.Join(bad, ", "))
	}
	return nil
}

// nonNumericFields are "key=value" for each of keys that f sets to
// something optInt cannot read, in keys' order.
func nonNumericFields(f map[string]any, keys []string) []string {
	var bad []string
	for _, k := range keys {
		if v := f[k]; v != nil && optInt(f, k) == nil {
			bad = append(bad, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return bad
}

// checkWhole refuses a fractional number in an id, wave or coordinate
// field, and one outside the range of int: optInt truncates a fraction, so a
// truncated id names a different record, and Go leaves int() of an
// out-of-range float to the platform.
func checkWhole(f map[string]any, keys []string) error {
	for _, k := range keys {
		if n, ok := f[k].(float64); ok {
			if err := checkWholeNumber(k, n); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkWholeNumber refuses n, field k's value, when it has a fraction or is
// outside the range of int.
func checkWholeNumber(k string, n float64) error {
	if n != math.Trunc(n) {
		return fmt.Errorf("field %s must be a whole number: %v", k, n)
	}
	if n < float64(math.MinInt) || n >= -float64(math.MinInt) {
		return fmt.Errorf("field %s is out of range: %v", k, n)
	}
	return nil
}

// optInt is f's k as an int (anyInt), nil when f has no k or it is not a
// number anyInt reads.
func optInt(f map[string]any, k string) *int {
	if n, ok := anyInt(f[k]); ok {
		return &n
	}
	return nil
}

// reqInt is f's k as an int, 0 when optInt reads none.
func reqInt(f map[string]any, k string) int {
	if p := optInt(f, k); p != nil {
		return *p
	}
	return 0
}

// anyInt is v, a decoded number, as an int: an int, an int64 or a float64,
// truncated, or a json.Number that holds an integer. ok is false for
// anything else.
func anyInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return jsonNumberInt(v)
}

// jsonNumberInt is v as an int when it is a json.Number holding an integer.
func jsonNumberInt(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}

// reqStr is f's k when it is a string, else "".
func reqStr(f map[string]any, k string) string {
	s, _ := f[k].(string)
	return s
}

// optStr is f's k when it is a non-empty string, else nil.
func optStr(f map[string]any, k string) *string {
	if v, ok := f[k].(string); ok && v != "" {
		return &v
	}
	return nil
}

// writeFinding writes a finding, the "finding" kind.
func writeFinding(s *Store, f map[string]any) (int64, string, error) {
	if err := checkRecordFields("finding", f,
		[]string{"wave", "agent", "mss_label", "finding", "evidence", "source_urls", "depends_on_ids", "d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"},
		[]string{"wave", "d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"},
		[]string{"agent", "mss_label", "finding", "evidence"}); err != nil {
		return 0, "", err
	}
	finding, err := findingFromFields(f)
	if err != nil {
		return 0, "", err
	}
	id, err := s.Findings().AddFinding(finding)
	if err != nil {
		return 0, "", err
	}
	return id, fmt.Sprintf("finding id=%d", id), nil
}

// findingFromFields is the finding f, a "finding" record's fields,
// describes; one with no mss_label is an assumption. source_urls and
// depends_on_ids arrive in more than one shape, which NormalizeSourceURLs
// and NormalizeDependsOnIDs read.
func findingFromFields(f map[string]any) (*Finding, error) {
	su, err := NormalizeSourceURLs(f["source_urls"])
	if err != nil {
		return nil, err
	}
	deps, err := NormalizeDependsOnIDs(f["depends_on_ids"])
	if err != nil {
		return nil, err
	}
	finding := &Finding{
		Wave:       reqInt(f, "wave"),
		Agent:      reqStr(f, "agent"),
		D1:         optInt(f, "d1"),
		D2:         optInt(f, "d2"),
		D3:         optInt(f, "d3"),
		D4:         optInt(f, "d4"),
		D5:         optInt(f, "d5"),
		D6:         optInt(f, "d6"),
		D7:         optInt(f, "d7"),
		D8:         optInt(f, "d8"),
		MSSLabel:   cmp.Or(reqStr(f, "mss_label"), "assumption"),
		Finding:    reqStr(f, "finding"),
		Evidence:   optStr(f, "evidence"),
		SourceURLs: su,
	}
	if deps != "" {
		finding.DependsOnIDs = &deps
	}
	return finding, nil
}

// writeGap writes a gap, the "gap" kind.
func writeGap(s *Store, f map[string]any) (int64, string, error) {
	if err := checkRecordFields("gap", f,
		[]string{"wave", "agent", "description", "priority", "d1", "d2", "d3", "d4"},
		[]string{"wave", "d1", "d2", "d3", "d4"},
		[]string{"agent", "description", "priority"}); err != nil {
		return 0, "", err
	}
	id, err := s.Gaps().insertGap(reqInt(f, "wave"), reqStr(f, "agent"), reqStr(f, "description"),
		gapPriority(reqStr(f, "priority")),
		optInt(f, "d1"), optInt(f, "d2"), optInt(f, "d3"), optInt(f, "d4"))
	if err != nil {
		return 0, "", err
	}
	return id, fmt.Sprintf("gap id=%d", id), nil
}

// gapPriority is priority normalized to the allowed values, critical,
// important or minor: high and none are important, medium and low minor.
// Any other value is kept, for the gaps table's CHECK to refuse.
func gapPriority(priority string) string {
	switch priority {
	case "high", "":
		return "important"
	case "medium", "low":
		return "minor"
	}
	return priority
}

// writeSource cites a source, the "source" kind. A URL already on record
// keeps what it has; the write fills a missing title and a missing wave
// (SourcesRepo.AddSource) and reports the record's id.
func writeSource(s *Store, f map[string]any) (int64, string, error) {
	if err := checkRecordFields("source", f,
		[]string{"url", "title", "wave", "agent", "contribution", "primary_source"},
		[]string{"wave", "primary_source"},
		[]string{"url", "title", "agent", "contribution"}); err != nil {
		return 0, "", err
	}
	url := reqStr(f, "url")
	id, err := s.Sources().AddSource(url, reqStr(f, "title"), reqStr(f, "agent"), optInt(f, "wave"), reqStr(f, "contribution"), reqInt(f, "primary_source"))
	if err != nil {
		return 0, "", err
	}
	return id, fmt.Sprintf("source id=%d url=%s", id, url), nil
}

// writeResolveGap closes a gap with the finding that answers it, the
// "resolve_gap" kind, with GapsRepo.ResolveGap's refusals.
func writeResolveGap(s *Store, f map[string]any) (int64, string, error) {
	if err := checkRecordFields("resolve_gap", f,
		[]string{"gap_id", "wave", "agent", "finding_id"},
		[]string{"gap_id", "wave", "finding_id"},
		[]string{"agent"}); err != nil {
		return 0, "", err
	}
	gapID, findingID, err := resolveGapIDs(f)
	if err != nil {
		return 0, "", err
	}
	if err := s.Gaps().ResolveGap(gapID, reqInt(f, "wave"), reqStr(f, "agent"), findingID); err != nil {
		return 0, "", err
	}
	return gapID, fmt.Sprintf("gap %d resolved by finding %d", gapID, findingID), nil
}

// resolveGapIDs are a resolve_gap record's gap and the finding that answers
// it, both of which it needs.
func resolveGapIDs(f map[string]any) (gapID, findingID int64, err error) {
	gapID, findingID = int64(reqInt(f, "gap_id")), int64(reqInt(f, "finding_id"))
	if gapID <= 0 || findingID <= 0 {
		return 0, 0, fmt.Errorf("resolve_gap needs a gap_id and the finding_id that answers it")
	}
	return gapID, findingID, nil
}

// writeResolveConflict closes a conflict, recording how it was settled, the
// "resolve_conflict" kind, with ConflictsRepo.Resolve's refusals.
func writeResolveConflict(s *Store, f map[string]any) (int64, string, error) {
	if err := checkRecordFields("resolve_conflict", f,
		[]string{"conflict_id", "wave", "resolution"},
		[]string{"conflict_id", "wave"},
		[]string{"resolution"}); err != nil {
		return 0, "", err
	}
	conflictID := int64(reqInt(f, "conflict_id"))
	if conflictID <= 0 {
		return 0, "", fmt.Errorf("resolve_conflict needs a positive conflict_id")
	}
	if err := s.Conflicts().Resolve(conflictID, reqInt(f, "wave"), reqStr(f, "resolution")); err != nil {
		return 0, "", err
	}
	return conflictID, fmt.Sprintf("conflict %d resolved", conflictID), nil
}
