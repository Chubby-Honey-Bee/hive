package dreamer

import (
	"database/sql"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
)

// rowQuerier is a pool or a transaction, read one row at a time.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// readFindingCoords pulls d1..d8 for one finding into a sparse Coords.
func readFindingCoords(read rowQuerier, id int64) (comb.Coords, error) {
	var ds [8]sql.NullInt64
	err := read.QueryRow(
		`SELECT d1, d2, d3, d4, d5, d6, d7, d8 FROM findings WHERE id = ?`, id,
	).Scan(&ds[0], &ds[1], &ds[2], &ds[3], &ds[4], &ds[5], &ds[6], &ds[7])
	if err != nil {
		return nil, err
	}
	out := comb.Coords{}
	for i, n := range ds {
		if n.Valid {
			out[dimName(i+1)] = int(n.Int64)
		}
	}
	return out, nil
}

func dimName(n int) string {
	if n < 1 || n > 8 {
		return ""
	}
	return [8]string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}[n-1]
}
