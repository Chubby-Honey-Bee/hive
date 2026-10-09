// Package citations enforces the open-access citation policy:
// research findings cited as guarantees must point to a DOI whose
// fulltext is freely available (Unpaywall is_oa=true). mss_audit lists
// guarantees whose DOIs are all paywalled or unverified under
// oa_citation_downgrades, as a warning. Nothing downgrades them and nothing
// opens a gap — the list is for a person or an agent to act on.
//
// Two design constraints:
//
//  1. No column of its own on findings: it reads `source_urls`. A finding
//     citing a DOI writes "https://doi.org/10.1234/xyz" (or a JSON array of
//     URLs) into source_urls.
//
//  2. Cached — Unpaywall is rate-limited to ~100k req/day; we hit the
//     API once per (doi, email) tuple per workspace and store the
//     result in the citation_oa_cache table.
package citations

import (
	"regexp"
	"strings"
)

// doiRegex matches a DOI anywhere in a string. The DOI standard
// (RFC 5005 / DOI Handbook) allows a wide character class after the
// `10.<registrant>/` prefix; the practical form for the registrants
// we care about (CrossRef, DataCite, etc.) is `10.\d{4,9}/[^\s]+`.
//
// `(`, `)` and `;` are part of many real DOIs — Elsevier's
// 10.1016/S0140-6736(20)30183-5, and SICI forms like …3.0.CO;2-# — so the
// suffix keeps them, and trimDOI removes what prose adds after a DOI: a DOI
// cut at its first `)` or `;` would fail at Unpaywall and be cached as
// unverified.
var doiRegex = regexp.MustCompile(`(?i)\b(10\.\d{4,9}/[^\s,\]"']+)`)

// trimDOI trims trailing sentence punctuation (`.`, `,`, `;`, quotes) and a
// trailing `)` that has no `(` to close, as in "(see 10.1038/x)."; a `)`
// that closes one inside the DOI stays.
func trimDOI(doi string) string {
	for doi != "" && endsInProse(doi) {
		doi = doi[:len(doi)-1]
	}
	return doi
}

// endsInProse reports whether doi's last byte is prose after the DOI, not
// part of it: sentence punctuation, a quote, or a `)` with no `(` to close.
func endsInProse(doi string) bool {
	last := doi[len(doi)-1]
	return strings.IndexByte(`.,;"'`, last) >= 0 ||
		(last == ')' && strings.Count(doi, ")") > strings.Count(doi, "("))
}

// ExtractDOIs scans a free-form string (typically the source_urls
// column, which may be a JSON array OR a space/comma-separated list)
// and returns every DOI it finds, in document order, deduplicated
// while preserving first-seen order.
//
// Empty input → empty slice. Invalid input → empty slice (we never
// error here; the verifier surfaces "no DOI found" as a gap).
func ExtractDOIs(text string) []string {
	if text == "" {
		return nil
	}
	matches := doiRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	return distinctDOIs(matches)
}

// distinctDOIs is the DOI of each match, trimmed and normalised, once each
// in first-seen order.
func distinctDOIs(matches [][]string) []string {
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		// Normalize: DOIs are case-insensitive, and lowercasing yields
		// stable cache keys. The normalised form is what is *returned*, not
		// just what is deduplicated on, so verify-citations, which writes
		// the cache, and mss_audit, which reads it, reach the same verdict
		// on a DOI whatever its case.
		key := strings.ToLower(trimDOI(m[1]))
		if _, dup := seen[key]; key == "" || dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}
