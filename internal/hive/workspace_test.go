package hive_test

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

func openStoreAt(t *testing.T, path string) *db.Store {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
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

// hiveProjects lists the hives a database holds.
func hiveProjects(t *testing.T, s *db.Store) []string {
	t.Helper()
	rows, err := s.ReadDB.Query("SELECT project FROM hive_state")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// The workspace root is the parent of the `workspace` directory a database
// sits in, at either depth chb lays out, and the current directory for a
// database in neither layout. The expected roots here are read off the
// definition in hive.md § One hive per database.
func TestWorkspaceRoot_TwoLayoutsElseTheCurrentDirectory(t *testing.T) {
	abs := string(filepath.Separator)
	for named, want := range map[string]string{
		filepath.Join("workspace", "hive.db"):                            ".",
		filepath.Join("workspace", "Y", "hive.db"):                       ".",
		filepath.Join(abs+"app", "workspace", "hive.db"):                 abs + "app",
		filepath.Join(abs+"x", "workspace", "Y", "hive.db"):              abs + "x",
		filepath.Join(abs+"data", "research.db"):                         ".",
		"hive.db":                                                        ".",
		filepath.Join(abs+"x", "workspace", "Y", "deeper", "hive.db"):    ".",
		filepath.Join(abs+"home", "me", "workspaces", "proj", "hive.db"): ".",
	} {
		if got := hive.WorkspaceRoot(named); got != want {
			t.Errorf("WorkspaceRoot(%q) = %q, want %q", named, got, want)
		}
	}
	named := filepath.Join(abs+"app", "workspace", "hive.db")
	if got, want := hive.WorkspacePath(named, "B"), filepath.Join(abs+"app", "workspace", "B", "hive.db"); got != want {
		t.Errorf("WorkspacePath(%q, B) = %q, want %q", named, got, want)
	}
}

// A database that hosts another project's hive sends the project to its
// workspace database: refused with the path named until `hive init` creates
// it, refused outright when the caller pinned the database, and used by
// every later lookup once it exists. The named database keeps its own hive
// alone throughout.
func TestResolve_ASecondProjectUsesItsWorkspaceDatabase(t *testing.T) {
	root := t.TempDir()
	named := filepath.Join(root, "workspace", "hive.db")
	wantB := filepath.Join(root, "workspace", "B", "hive.db")
	a := openStoreAt(t, named)

	rA, created, err := hive.Init(hive.Lookup{Project: "A", Named: a, NamedPath: named})
	if err != nil || !created || rA.Store != a || rA.Path != named || rA.HostedBy != "" {
		t.Fatalf("Init(A) on an empty database = %+v created=%v err=%v, want the named database", rA, created, err)
	}

	_, err = hive.Resolve(hive.Lookup{Project: "B", Named: a, NamedPath: named})
	var missing *hive.NoWorkspaceDBError
	if !errors.As(err, &missing) || missing.Path != wantB || missing.HostedBy != "A" || missing.Project != "B" {
		t.Fatalf("Resolve(B) before init: err = %v, want NoWorkspaceDBError naming %s and A", err, wantB)
	}
	if _, err := os.Stat(wantB); !os.IsNotExist(err) {
		t.Fatalf("Resolve(B) without CreateDB left %s on disk (%v)", wantB, err)
	}

	_, err = hive.Resolve(hive.Lookup{Project: "B", Named: a, NamedPath: named, Pinned: true, CreateDB: true})
	var other *hive.OtherHiveError
	if !errors.As(err, &other) || other.Other != "A" || other.Project != "B" || other.Path != named || other.Use != wantB {
		t.Fatalf("Resolve(B) pinned: err = %v, want OtherHiveError naming A, B, %s and %s", err, named, wantB)
	}
	if _, err := os.Stat(wantB); !os.IsNotExist(err) {
		t.Fatalf("a pinned lookup created %s (%v)", wantB, err)
	}

	rB, created, err := hive.Init(hive.Lookup{Project: "B", Named: a, NamedPath: named})
	if err != nil {
		t.Fatalf("Init(B): %v", err)
	}
	defer rB.Close()
	if !created || !rB.CreatedDB || rB.Path != wantB || rB.HostedBy != "A" || rB.Named != named || rB.Store == a {
		t.Fatalf("Init(B) = %+v created=%v, want a new hive in a new %s", rB, created, wantB)
	}
	if got := hiveProjects(t, a); len(got) != 1 || got[0] != "A" {
		t.Fatalf("the named database hosts %v after Init(B), want [A]", got)
	}
	if got := hiveProjects(t, rB.Store); len(got) != 1 || got[0] != "B" {
		t.Fatalf("B's workspace database hosts %v, want [B]", got)
	}

	again, err := hive.Resolve(hive.Lookup{Project: "B", Named: a, NamedPath: named})
	if err != nil || again.Path != wantB || again.Store == a || again.CreatedDB {
		t.Fatalf("Resolve(B) after init = %+v err=%v, want the existing %s", again, err, wantB)
	}
	again.Close()
	stillA, err := hive.Resolve(hive.Lookup{Project: "A", Named: a, NamedPath: named})
	if err != nil || stillA.Store != a {
		t.Fatalf("Resolve(A) = %+v err=%v, want the named database", stillA, err)
	}

	// The workspace database is where the rule ends: one that another
	// project's hive took is refused, for a lookup and for an init alike.
	if _, err := rB.Store.WriteDB.Exec(`UPDATE hive_state SET project='C'`); err != nil {
		t.Fatal(err)
	}
	for name, try := range map[string]func() error{
		"Resolve": func() error {
			_, err := hive.Resolve(hive.Lookup{Project: "B", Named: a, NamedPath: named})
			return err
		},
		"Init": func() error {
			_, _, err := hive.Init(hive.Lookup{Project: "B", Named: a, NamedPath: named})
			return err
		},
	} {
		err := try()
		if !errors.As(err, &other) || other.Other != "C" || other.Path != wantB || other.Use != "" {
			t.Errorf("%s(B) with its workspace database hosting C: err = %v, want OtherHiveError naming C at %s", name, err, wantB)
		}
	}
	if got := hiveProjects(t, rB.Store); len(got) != 1 || got[0] != "C" {
		t.Fatalf("B's workspace database hosts %v after the refusals, want [C]", got)
	}
}

// A database that hosts no hive, or this project's, is used as named,
// whatever directory it sits in, and nothing is created beside it. A
// project that is no bare name cannot be sent to a workspace directory.
func TestResolve_ANamedDatabaseWithNoOtherHiveIsUsedAsIs(t *testing.T) {
	root := t.TempDir()
	named := filepath.Join(root, "elsewhere", "research.db")
	s := openStoreAt(t, named)
	for i := 0; i < 2; i++ {
		r, created, err := hive.Init(hive.Lookup{Project: "B", Named: s, NamedPath: named})
		if err != nil || r.Store != s || r.Path != named {
			t.Fatalf("Init(B) #%d = %+v err=%v, want the named database", i+1, r, err)
		}
		if r.CreatedDB || created != (i == 0) {
			t.Fatalf("Init(B) #%d: created=%v CreatedDB=%v, want created on the first init only and no new file", i+1, created, r.CreatedDB)
		}
	}
	r, err := hive.Resolve(hive.Lookup{Project: "B", Named: s, NamedPath: named, Pinned: true})
	if err != nil || r.Store != s {
		t.Fatalf("Resolve(B) pinned on its own database = %+v err=%v", r, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != "elsewhere" {
		t.Fatalf("the lookups created %v beside the database", entries)
	}
	if _, err := hive.Resolve(hive.Lookup{Project: filepath.Join("..", "escape"), Named: s, NamedPath: named, CreateDB: true}); err == nil {
		t.Fatal("a project with a path separator was sent to a workspace directory")
	}
}

// Two inits of different projects at once, on one empty database, both
// succeed: the one conditional insert decides which project the named
// database hosts, and the other goes on to its workspace database. The
// named database never holds two hives.
func TestInit_TwoProjectsAtOnceLeaveOneHiveInTheNamedDatabase(t *testing.T) {
	root := t.TempDir()
	named := filepath.Join(root, "workspace", "hive.db")
	stores := []*db.Store{openStoreAt(t, named), openStoreAt(t, named)}
	projects := []string{"A", "B"}
	type result struct {
		path string
		err  error
	}
	for trial := 0; trial < 40; trial++ {
		if _, err := stores[0].WriteDB.Exec("DELETE FROM hive_state"); err != nil {
			t.Fatal(err)
		}
		for _, p := range projects {
			if err := os.RemoveAll(filepath.Join(root, "workspace", p)); err != nil {
				t.Fatal(err)
			}
		}
		results := make([]result, len(projects))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range projects {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, created, err := hive.Init(hive.Lookup{Project: projects[i], Named: stores[i], NamedPath: named})
				if err == nil {
					results[i] = result{path: r.Path}
					if !created {
						results[i].err = errors.New("not created")
					}
					r.Close()
				} else {
					results[i].err = err
				}
			}()
		}
		close(start)
		wg.Wait()

		hives := hiveProjects(t, stores[0])
		if len(hives) != 1 {
			t.Fatalf("trial %d: the named database holds hives %v, want exactly one", trial, hives)
		}
		for i, p := range projects {
			want := named
			if p != hives[0] {
				want = filepath.Join(root, "workspace", p, "hive.db")
			}
			if results[i].err != nil || results[i].path != want {
				t.Fatalf("trial %d: Init(%s) = %+v, want a new hive in %s", trial, p, results[i], want)
			}
		}
	}
}
