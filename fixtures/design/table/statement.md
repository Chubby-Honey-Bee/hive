# Split Render into reusable parts without changing its output

Package `table` (file `table.go`) exports `func Render(rows [][]string) string`, one function that measures columns, pads cells and draws the rule. Refactor it into parts other code can call, keeping `Render`'s output byte-identical for every input.

Add two exported functions and make `Render` use them:

- `func Widths(rows [][]string) []int` returns one width per column, the column count being the length of the longest row, each width the largest rune count (`utf8.RuneCountInString`) of that column's cells over all rows; a row with fewer cells contributes nothing to the missing columns. For no rows it returns an empty, non-nil slice.
- `func RenderRow(cells []string, widths []int) string` returns one rendered line without its trailing newline: each of the `len(widths)` columns left-aligned and padded with spaces to its width, columns joined by `" | "`; a missing cell is empty; cells beyond `len(widths)` are ignored.

`Render` keeps its signature and its exact output, including the rule of dashes joined by `"-+-"` under the first row and the trailing newline after every line. Keep the package name.
