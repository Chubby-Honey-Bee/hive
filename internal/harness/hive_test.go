package harness

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

// The hive case grades the database, so it fails a run that wrote nothing —
// one that exited 0 with every node accepted and the seeded gap left open —
// and passes one that filled and closed the gap.
func TestGradeHiveRun_FailsAnEmptyRunAndPassesAFilledGap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hive.db")
	s, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, _, err := hive.InitProject(s, "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDB.Exec(`UPDATE hive_state SET iteration = 1 WHERE project = 'p'`); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := s.Gaps().AddGap(1, "seed", "What is the default page size of a new SQLite database?", "critical", &zero, &zero, &zero, &zero); err != nil {
		t.Fatal(err)
	}

	grade := func() map[string]bool {
		got := map[string]bool{}
		record := func(name string, ok bool, _ string) { got[name] = ok }
		gradeHiveRun(dbPath, "p", "sqlite-default-page-size", 1, record, func(string, bool, string) {})
		return got
	}

	if got := grade(); got["the seeded gap is resolved"] {
		t.Fatalf("a run that wrote nothing graded the gap resolved: %v", got)
	}

	pageSize, err := sqliteDefaultPageSize()
	if err != nil {
		t.Fatal(err)
	}
	stated := fmt.Sprint(pageSize)
	if pageSize >= 1000 {
		stated = fmt.Sprintf("%d,%03d", pageSize/1000, pageSize%1000)
	}
	answer := "A new database uses " + stated + "-byte pages."
	src := "https://www.sqlite.org/pragma.html#pragma_page_size"
	id, err := s.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "hive-scout", MSSLabel: "assumption", Finding: answer, SourceURLs: &src,
		D1: &zero, D2: &zero, D3: &zero, D4: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	var gapID int64
	if err := s.ReadDB.QueryRow(`SELECT id FROM gaps WHERE agent = 'seed'`).Scan(&gapID); err != nil {
		t.Fatal(err)
	}
	if err := s.Gaps().ResolveGap(gapID, 1, "hive-scout", id); err != nil {
		t.Fatal(err)
	}

	got := grade()
	for name, ok := range got {
		if !ok {
			t.Errorf("check %q failed on a run that filled and closed the gap (all: %v)", name, got)
		}
	}
	for _, want := range []string{
		"the seeded gap is resolved",
		"the resolving finding exists and a hive agent wrote it",
		"the next plan holds no gap-fill for the resolved gap",
		"MSS audit PASS",
	} {
		if _, ran := got[want]; !ran {
			t.Errorf("check %q did not run (all: %v)", want, got)
		}
	}
}

func TestMentionsNumber(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"pages are 4096 bytes", true},
		{"pages are 4,096 bytes", true},
		{"4096", true},
		{"pages are 40960 bytes", false},
		{"pages are 14096 bytes", false},
		{"pages are four kilobytes", false},
	} {
		if got := mentionsNumber(tc.text, "4096"); got != tc.want {
			t.Errorf("mentionsNumber(%q, 4096) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
