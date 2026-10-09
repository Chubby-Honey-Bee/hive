// Package table renders rows of cells as a text table.
package table

import (
	"strings"
	"unicode/utf8"
)

// Render writes rows as a text table: every cell left-aligned and padded
// to its column's width in runes, cells separated by " | ", one row per
// line, and a rule of dashes joined by "-+-" under the first row. A row
// shorter than the widest is padded with empty cells. Nothing is rendered
// for no rows.
func Render(rows [][]string) string {
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	var b strings.Builder
	for ri, r := range rows {
		for i := 0; i < cols; i++ {
			c := ""
			if i < len(r) {
				c = r[i]
			}
			b.WriteString(c)
			b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)))
			if i < cols-1 {
				b.WriteString(" | ")
			}
		}
		b.WriteString("\n")
		if ri == 0 {
			for i := 0; i < cols; i++ {
				b.WriteString(strings.Repeat("-", widths[i]))
				if i < cols-1 {
					b.WriteString("-+-")
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
