package table

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// originalRender is the Render this refactor starts from, kept here as the
// oracle: Render must stay byte-identical to it.
func originalRender(rows [][]string) string {
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

var battery = [][][]string{
	nil,
	{},
	{{"only"}},
	{{"a", "b"}, {"ccc", "d"}},
	{{"name", "count"}, {"x", "1"}, {"longer name", "22"}},
	{{"ragged", "row", "here"}, {"short"}, {}, {"x", "y"}},
	{{"", ""}, {"", "z"}},
	{{"日本語", "é"}, {"ab", "ü"}},
	{{"one"}, {"two", "three"}, {"four", "five", "six"}},
}

func TestHiddenRenderUnchanged(t *testing.T) {
	for _, rows := range battery {
		if got, want := Render(rows), originalRender(rows); got != want {
			t.Errorf("Render(%q) =\n%q\nwant\n%q", rows, got, want)
		}
	}
}

func TestHiddenWidths(t *testing.T) {
	cases := []struct {
		rows [][]string
		want []int
	}{
		{nil, []int{}},
		{[][]string{{"a", "bb"}, {"ccc"}}, []int{3, 2}},
		{[][]string{{"日本語", "é"}, {"ab", "üü"}}, []int{3, 2}},
		{[][]string{{}, {"x"}}, []int{1}},
	}
	for _, c := range cases {
		got := Widths(c.rows)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Widths(%q) = %#v, want %#v", c.rows, got, c.want)
		}
	}
}

func TestHiddenRenderRow(t *testing.T) {
	cases := []struct {
		cells  []string
		widths []int
		want   string
	}{
		{[]string{"a", "b"}, []int{3, 2}, "a   | b "},
		{[]string{"a"}, []int{1, 2}, "a |   "},
		{[]string{"a", "b", "extra"}, []int{1, 1}, "a | b"},
		{nil, []int{2}, "  "},
		{[]string{"日本"}, []int{3}, "日本 "},
		{[]string{"x"}, nil, ""},
	}
	for _, c := range cases {
		if got := RenderRow(c.cells, c.widths); got != c.want {
			t.Errorf("RenderRow(%q, %v) = %q, want %q", c.cells, c.widths, got, c.want)
		}
	}
}

func TestHiddenRenderUsesTheParts(t *testing.T) {
	rows := [][]string{{"h1", "h2"}, {"a", "bbb"}}
	widths := Widths(rows)
	want := RenderRow(rows[0], widths) + "\n" + strings.Repeat("-", widths[0]) + "-+-" + strings.Repeat("-", widths[1]) + "\n" + RenderRow(rows[1], widths) + "\n"
	if got := Render(rows); got != want {
		t.Errorf("Render =\n%q\nwant\n%q", got, want)
	}
}
