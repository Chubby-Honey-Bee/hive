package db

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The database path was interpolated straight into a `file:` URI, so SQLite
// read everything after a bare `?` as query parameters and everything after
// `#` as a fragment: a workspace under a directory containing one of those
// opened a different database (or none) with no error.
func TestSqliteDSN_EscapesURIMetacharacters(t *testing.T) {
	got := sqliteDSN("/tmp/a?b#c%d/hive.db", "_pragma=foo(1)")
	want := "file:/tmp/a%3fb%23c%25d/hive.db?_pragma=foo(1)"
	if got != want {
		t.Fatalf("sqliteDSN = %q, want %q", got, want)
	}
	if strings.Count(got, "?") != 1 {
		t.Fatalf("exactly one unescaped '?' (the pragma separator) must remain: %q", got)
	}
}

func TestNewStore_PathWithURIMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj#1?draft")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hive.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.Dimensions().AddDimension("d", "desc", `["a"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the store wrote somewhere other than %s: %v", path, err)
	}
}

// A nameless dimension is unusable and poisons every later unnamed write,
// because "" is the UNIQUE(name) upsert target, so one is refused.
func TestAddDimension_RejectsEmptyName(t *testing.T) {
	store := newTestStore(t)
	for _, name := range []string{"", "   "} {
		if err := store.Dimensions().AddDimension(name, "d", "[]"); err == nil {
			t.Errorf("AddDimension(%q) succeeded; want a name error", name)
		}
	}
	dims, err := store.Dimensions().GetDimensions()
	if err != nil {
		t.Fatal(err)
	}
	if len(dims) != 0 {
		t.Fatalf("nameless dimension persisted: %+v", dims)
	}
}

func TestEnvIntDefault(t *testing.T) {
	cases := []struct {
		name     string
		envVal   string
		fallback string
		want     string
	}{
		{"unset → fallback", "", "100", "100"},
		{"valid positive int", "42", "100", "42"},
		{"non-numeric → fallback", "abc", "100", "100"},
		{"zero → fallback (positive only)", "0", "100", "100"},
		{"negative → fallback", "-5", "100", "100"},
		{"very-large positive accepted", "9999999", "100", "9999999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENVTEST_FOO", tc.envVal)
			// When envVal is "", t.Setenv still sets the key — explicitly
			// unset to exercise the "no env var" branch.
			if tc.envVal == "" {
				t.Setenv("ENVTEST_FOO", "")
			}
			got := envIntDefault("ENVTEST_FOO", tc.fallback)
			if got != tc.want {
				t.Errorf("envIntDefault(%q) = %q; want %q", tc.envVal, got, tc.want)
			}
		})
	}
}

// Several processes opening the same new file at once need no lock file: the
// schema is idempotent DDL. They do contend for the switch to WAL, which
// SQLite refuses with SQLITE_BUSY without consulting busy_timeout, so
// initSchema retries a busy attempt; without the retry this fails about one
// run in ten.
func TestConcurrentFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	const openers = 8
	errs := make([]error, openers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < openers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s, err := NewStore(path)
			if err != nil {
				errs[i] = err
				return
			}
			defer s.Close()
			errs[i] = s.Init()
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, table := range []string{"findings", "comb_state", "tool_invocations", "workflow_node_states"} {
		if ok, err := tableExists(s.ReadDB, table); err != nil || !ok {
			t.Errorf("table %s missing after concurrent init (err=%v)", table, err)
		}
	}
}
