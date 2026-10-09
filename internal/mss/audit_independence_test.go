package mss

import (
	"testing"
)

func TestIndependenceAudit_NoAssumptions(t *testing.T) {
	db := partitionTestDB(t)
	got, err := IndependenceAudit(db, 0.7)
	if err != nil {
		t.Fatalf("IndependenceAudit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no candidates; got %v", got)
	}
}

func TestIndependenceAudit_SingleAssumptionInGroup(t *testing.T) {
	db := partitionTestDB(t)
	if _, err := db.Exec(
		"INSERT INTO findings(mss_label, finding, d1) VALUES ('assumption', 'lone bet', 0)",
	); err != nil {
		t.Fatal(err)
	}
	got, err := IndependenceAudit(db, 0.7)
	if err != nil {
		t.Fatalf("IndependenceAudit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("singletons can't be candidates; got %v", got)
	}
}

func TestIndependenceAudit_DistinctAssumptionsBelowThreshold(t *testing.T) {
	db := partitionTestDB(t)
	if _, err := db.Exec(`INSERT INTO findings(mss_label, finding, d1) VALUES
		('assumption', 'shipping cost is twelve dollars per package', 0),
		('assumption', 'manufacturing yields are seventy percent', 0)`); err != nil {
		t.Fatal(err)
	}
	got, err := IndependenceAudit(db, 0.9)
	if err != nil {
		t.Fatalf("IndependenceAudit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("distinct content shouldn't pair; got %v", got)
	}
}

func TestIndependenceAudit_DBError(t *testing.T) {
	db := partitionTestDB(t)
	db.Close()
	_, err := IndependenceAudit(db, 0.7)
	if err == nil {
		t.Error("expected error from closed DB")
	}
}

func TestContentSimilarity_Basic(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // similarity > 0
	}{
		{"the quick brown fox", "the quick brown fox", true},
		{"hello world", "completely different", false},
		{"", "", false}, // both empty → 0
		{"alpha beta gamma", "", false},
	}
	for _, tc := range cases {
		got := contentSimilarity(tc.a, tc.b)
		if (got > 0) != tc.want {
			t.Errorf("contentSimilarity(%q, %q) = %g; want >0=%v", tc.a, tc.b, got, tc.want)
		}
	}
}
