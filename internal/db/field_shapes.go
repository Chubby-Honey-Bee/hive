package db

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Two finding fields arrive in more than one shape: source_urls as a string
// or a JSON array, which agents/researcher.md documents and the citation
// subsystem reads, and depends_on_ids as an array or a JSON string. These
// are the one definition every write surface uses, so none drops an array or
// double-encodes a string.

// NormalizeSourceURLs accepts source_urls as a string (comma-separated, or
// JSON text) or as a real JSON array, and returns the stored form. Returns nil
// when the field is absent or empty, so an absent field stays NULL.
func NormalizeSourceURLs(v any) (*string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return nonBlank(x), nil
	case []any:
		return sourceURLList(x)
	}
	return nil, fmt.Errorf("source_urls must be a string or a list of strings, got %T", v)
}

// nonBlank is s, or nil when it is blank.
func nonBlank(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// sourceURLList stores a list of URLs as a JSON array of its non-blank
// strings; nil when there are none.
func sourceURLList(items []any) (*string, error) {
	urls, err := sourceURLStrings(items)
	if err != nil || len(urls) == 0 {
		return nil, err
	}
	b, err := json.Marshal(urls)
	if err != nil {
		return nil, fmt.Errorf("encode source_urls: %w", err)
	}
	s := string(b)
	return &s, nil
}

// sourceURLStrings is the list's non-blank strings, refusing an item that
// is not a string.
func sourceURLStrings(items []any) ([]string, error) {
	urls := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("source_urls must be a list of strings, got %T in the list", item)
		}
		if strings.TrimSpace(s) != "" {
			urls = append(urls, s)
		}
	}
	return urls, nil
}

// NormalizeDependsOnIDs accepts depends_on_ids as a JSON array of numbers or
// as a JSON string holding one, and returns the canonical JSON-array text.
// Returns "" for absent, empty, "null" or "[]" — the forms that mean "no
// dependencies", which AddFinding refuses for a guarantee.
func NormalizeDependsOnIDs(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return depsFromJSONText(x)
	case []any:
		return depsFromList(x)
	}
	return "", fmt.Errorf("depends_on_ids must be a JSON array of finding ids, got %T", v)
}

// depsFromJSONText reads depends_on_ids given as JSON text.
func depsFromJSONText(x string) (string, error) {
	s := strings.TrimSpace(x)
	if meansNoDeps(s) {
		return "", nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return "", fmt.Errorf("depends_on_ids must be a JSON array of finding ids, got %q: %w", s, err)
	}
	return encodeIDs(ids)
}

// meansNoDeps reports whether trimmed depends_on_ids text names no
// dependency: empty, "null" or "[]".
func meansNoDeps(s string) bool {
	return s == "" || s == "null" || s == "[]"
}

// depsFromList reads depends_on_ids given as a JSON array.
func depsFromList(items []any) (string, error) {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		id, ok := findingIDOf(item)
		if !ok {
			return "", fmt.Errorf("depends_on_ids must contain finding ids, got %T in the list", item)
		}
		ids = append(ids, id)
	}
	return encodeIDs(ids)
}

// findingIDOf reads a finding id given as a JSON number or an integer.
func findingIDOf(item any) (int64, bool) {
	switch n := item.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

func encodeIDs(ids []int64) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("encode depends_on_ids: %w", err)
	}
	return string(b), nil
}
