// Package table renders rows of cells as a text table.
package table

import (
	"strings"
	"unicode/utf8"
)

// Widths is the width in runes of each column of rows: the column count is
// the longest row's, and each width the largest rune count of that column's
// cells. For no rows it is empty.
func Widths(rows [][]string) []int {
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
	return widths
}

// RenderRow is one line of the table without its newline: len(widths)
// columns, each cell left-aligned and padded to its width, joined by " | ".
// A missing cell is empty; cells past the widths are ignored.
func RenderRow(cells []string, widths []int) string {
	var b strings.Builder
	for i, w := range widths {
		c := ""
		if i < len(cells) {
			c = cells[i]
		}
		b.WriteString(c)
		b.WriteString(strings.Repeat(" ", w-utf8.RuneCountInString(c)))
		if i < len(widths)-1 {
			b.WriteString(" | ")
		}
	}
	return b.String()
}

// Render writes rows as a text table: RenderRow per row, one per line, and
// a rule of dashes joined by "-+-" under the first row. Nothing is rendered
// for no rows.
func Render(rows [][]string) string {
	widths := Widths(rows)
	var b strings.Builder
	for ri, r := range rows {
		b.WriteString(RenderRow(r, widths))
		b.WriteString("\n")
		if ri == 0 {
			for i, w := range widths {
				b.WriteString(strings.Repeat("-", w))
				if i < len(widths)-1 {
					b.WriteString("-+-")
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
