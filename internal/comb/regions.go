// Package comb maintains the hive's shared belief surface — a per-vantage
// belief digest of what the system currently believes. Two vantage kinds:
//
//	region    per-coordinate-prefix digest. Vantage keys are canonical
//	          coord-prefix strings ("", "d1=0", "d1=0;d2=3", …).
//	forager    per-forager verdict from a swarm run. Vantage keys are
//	          "forager:<name>" (e.g. "forager:optimist").
//
// Built deterministically from the CDE/MSS tables, with no model call. Every
// vantage write also lands an immutable row in comb_revisions (chronomantic
// history).
//
// The Comb is the colony's shared memory — what the Queen reads from
// when she synthesizes — the substrate that lets the swarm carry belief
// between foragers (lateral integration), between ticks (temporal
// integration), and between abstraction levels (region ↔ forager).
package comb

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Coords is a sparse map of dimension name → value. Only the dimensions
// that are pinned for this region appear; absent keys widen the region.
type Coords map[string]int

// RegionKey renders a Coords map as a canonical, deterministic key:
//
//	{}                    →  ""           (the global region)
//	{d1: 0}               →  "d1=0"
//	{d1: 0, d2: 3}        →  "d1=0;d2=3"
//
// Keys are sorted by dimension number so map ordering can't drift.
// Used as one form of vantage_key on comb_state (the other is the
// "forager:<name>" form for forager vantages).
func RegionKey(c Coords) string {
	if len(c) == 0 {
		return ""
	}
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return dimOrder(keys[i]) < dimOrder(keys[j]) })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, c[k]))
	}
	return strings.Join(parts, ";")
}

// ParseRegionKey is the inverse of RegionKey. Empty input → empty Coords.
// Returns an error on malformed tokens (e.g., "d1=" or "x=1").
func ParseRegionKey(s string) (Coords, error) {
	out := Coords{}
	for _, tok := range strings.Split(strings.TrimSpace(s), ";") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		k, v, err := parseRegionToken(tok)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// parseRegionToken reads one "dN=v" token of a region key.
func parseRegionToken(tok string) (string, int, error) {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return "", 0, fmt.Errorf("bad region token %q (missing '=')", tok)
	}
	k := strings.TrimSpace(tok[:eq])
	vStr := strings.TrimSpace(tok[eq+1:])
	if !validDim(k) {
		return "", 0, fmt.Errorf("bad region token %q (unknown dim %q)", tok, k)
	}
	v, err := strconv.Atoi(vStr)
	if err != nil {
		return "", 0, fmt.Errorf("bad region token %q (value %q is not int)", tok, vStr)
	}
	return k, v, nil
}

// AsNullCoords expands Coords into a fixed d1..d8 slice of sql.NullInt64
// values, suitable for direct insertion into the comb_state schema.
func (c Coords) AsNullCoords() (d [8]sql.NullInt64) {
	for k, v := range c {
		idx := dimOrder(k) - 1
		if idx < 0 || idx >= 8 {
			continue
		}
		d[idx] = sql.NullInt64{Valid: true, Int64: int64(v)}
	}
	return d
}

// Prefixes enumerates every coordinate prefix of a Coords, in
// most-general → most-specific order:
//
//	{d1:0, d2:3, d4:7} → [
//	  {},              // global
//	  {d1:0},
//	  {d1:0, d2:3},
//	  {d1:0, d2:3, d4:7},  // (d3 is skipped since absent)
//	]
//
// Used by BuildAllRegions to compute every prefix that has a finding.
func Prefixes(c Coords) []Coords {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return dimOrder(keys[i]) < dimOrder(keys[j]) })

	out := []Coords{{}}
	cur := Coords{}
	for _, k := range keys {
		cur[k] = c[k]
		copyMap := make(Coords, len(cur))
		for kk, vv := range cur {
			copyMap[kk] = vv
		}
		out = append(out, copyMap)
	}
	return out
}

// CoveringRegions returns the prefixes the caller should consult when
// querying for a digest at `target`, in most-specific-first order so a
// fallback walk picks the tightest non-empty region.
func CoveringRegions(target Coords) []Coords {
	pfx := Prefixes(target)
	// reverse
	for i, j := 0, len(pfx)-1; i < j; i, j = i+1, j-1 {
		pfx[i], pfx[j] = pfx[j], pfx[i]
	}
	return pfx
}

// dimNumbers maps "d1".."d8" to 1..8.
var dimNumbers = map[string]int{"d1": 1, "d2": 2, "d3": 3, "d4": 4, "d5": 5, "d6": 6, "d7": 7, "d8": 8}

// dimOrder maps "d1".."d8" → 1..8; everything else → -1 (sentinel).
func dimOrder(name string) int {
	if n, ok := dimNumbers[name]; ok {
		return n
	}
	return -1
}

func validDim(name string) bool { return dimOrder(name) >= 1 }

// IsForagerVantage reports whether a vantage key names a forager rather
// than a coordinate region. Forager vantages have the prefix "forager:".
func IsForagerVantage(key string) bool {
	return strings.HasPrefix(key, "forager:")
}
