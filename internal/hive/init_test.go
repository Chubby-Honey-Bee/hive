package hive_test

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

// Two stores on one database file, as two `chb hive init` processes are,
// initialize different projects at once. Exactly one is created, and the
// other is told which project holds the database. A check followed by a
// separate insert let both through.
func TestInitProject_ConcurrentStoresLeaveOneHive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hive.db")
	open := func() *db.Store {
		t.Helper()
		s, err := db.NewStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Init(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	stores := []*db.Store{open(), open()}
	projects := []string{"A", "B"}

	type result struct {
		created bool
		other   string
		err     error
	}
	for trial := 0; trial < 40; trial++ {
		if _, err := stores[0].WriteDB.Exec("DELETE FROM hive_state"); err != nil {
			t.Fatal(err)
		}
		results := make([]result, len(projects))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range projects {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				c, o, err := hive.InitProject(stores[i], projects[i])
				results[i] = result{c, o, err}
			}()
		}
		close(start)
		wg.Wait()

		var hives []string
		rows, err := stores[0].ReadDB.Query("SELECT project FROM hive_state")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			hives = append(hives, p)
		}
		rows.Close()
		if len(hives) != 1 {
			t.Fatalf("trial %d: the database holds hives %v, want exactly one", trial, hives)
		}
		for i, p := range projects {
			want := result{created: p == hives[0]}
			if !want.created {
				want.other = hives[0]
			}
			if results[i] != want {
				t.Fatalf("trial %d: InitProject(%s) = %+v, want %+v", trial, p, results[i], want)
			}
		}
	}
}
