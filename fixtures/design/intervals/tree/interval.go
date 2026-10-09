// Package interval merges closed integer intervals.
package interval

import "sort"

// Interval is the closed range of integers from Lo to Hi.
type Interval struct{ Lo, Hi int }

// Merge returns the union of ivs as non-overlapping intervals in ascending
// order. It has bugs; see the task statement.
func Merge(ivs []Interval) []Interval {
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].Lo < ivs[j].Lo })
	var out []Interval
	for _, iv := range ivs {
		if n := len(out); n > 0 && iv.Lo < out[n-1].Hi {
			if iv.Hi > out[n-1].Hi {
				out[n-1].Hi = iv.Hi
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}
