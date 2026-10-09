package foragers

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docs/assumptions.md states facts about the roster — the preset sizes A1
// was verified against, the sigils A6 bets on — and nothing checked them:
// the only automated check on the ledger was that the file existed. This
// holds those statements to the shipped foragers.
func TestAssumptionsLedgerMatchesTheRoster(t *testing.T) {
	raw, err := os.ReadFile("../../docs/assumptions.md")
	if err != nil {
		t.Fatal(err)
	}
	ledger := string(raw)
	all, err := Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}

	m := regexp.MustCompile("`minimal` = (\\d+), `balanced` = (\\d+), `default` = (\\d+),\\s+`all` = (\\d+)").FindStringSubmatch(ledger)
	if m == nil {
		t.Fatal("A1's verified preset sizes not found in docs/assumptions.md")
	}
	for i, preset := range []string{"minimal", "balanced", "default", "all"} {
		want, _ := strconv.Atoi(m[i+1])
		got, err := Filter(all, []string{preset})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != want {
			t.Errorf("assumptions.md says `%s` = %d; the roster resolves %d", preset, want, len(got))
		}
	}

	var palette []struct {
		Sigil string `json:"sigil"`
	}
	pb, err := os.ReadFile("../../foragers/palette.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pb, &palette); err != nil {
		t.Fatal(err)
	}
	a6 := ledger[strings.Index(ledger, "## A6"):]
	a6 = a6[:strings.Index(a6, "**What changes if wrong:**")]
	for _, p := range palette {
		if !strings.Contains(a6, p.Sigil) {
			t.Errorf("A6 does not list the sigil %s that palette.json ships", p.Sigil)
		}
	}
}
