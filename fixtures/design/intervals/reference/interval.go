// Package interval merges closed integer intervals.
package interval

import "sort"

// Interval is the closed range of integers from Lo to Hi.
type Interval struct{ Lo, Hi int }

// Merge returns the union of ivs as non-overlapping, non-touching
// intervals in ascending order. An interval with Lo > Hi is read as the
// range from Hi to Lo. ivs is not modified.
func Merge(ivs []Interval) []Interval {
	sorted := make([]Interval, 0, len(ivs))
	for _, iv := range ivs {
		if iv.Lo > iv.Hi {
			iv.Lo, iv.Hi = iv.Hi, iv.Lo
		}
		sorted = append(sorted, iv)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Lo < sorted[j].Lo })
	out := []Interval{}
	for _, iv := range sorted {
		if n := len(out); n > 0 && iv.Lo <= out[n-1].Hi+1 {
			if iv.Hi > out[n-1].Hi {
				out[n-1].Hi = iv.Hi
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}
