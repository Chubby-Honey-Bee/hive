package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The behavior fixture is for developing HIVE. chb preflight checks for it
// only in a HIVE source checkout, a directory whose go.mod names the module,
// and says nothing of it anywhere else: not in an empty directory, nor in
// another module's checkout that holds a file of that name.
func TestPreflight_BehaviorFixtureOnlyInACheckout(t *testing.T) {
	const hive = "module github.com/Chubby-Honey-Bee/hive\n"
	for _, c := range []struct {
		name, gomod, fixture, want string
	}{
		{"an empty directory", "", "", ""},
		{"another module", "module example.com/other\n", "{}\n", ""},
		{"a checkout without the fixture", hive, "", "⚠ behavior fixture — fixtures/cli-behavior.jsonl missing"},
		{"a checkout with it", hive, "{}\n", "✓ behavior fixture — fixtures/cli-behavior.jsonl"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeIfSet(t, "go.mod", c.gomod)
			writeIfSet(t, filepath.Join("fixtures", "cli-behavior.jsonl"), c.fixture)
			cmd := newPreflightCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"workflows/proof.yaml", "--skip-provider-check"})
			_ = cmd.Execute()
			mentions := strings.Contains(out.String(), "cli-behavior") || strings.Contains(out.String(), "behavior fixture")
			if c.want == "" && mentions || c.want != "" && !strings.Contains(out.String(), c.want) {
				t.Errorf("want %q (none when empty):\n%s", c.want, out.String())
			}
		})
	}
}

// writeIfSet writes content to path, making its directory, unless content
// is empty.
func writeIfSet(t *testing.T, path, content string) {
	t.Helper()
	if content == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
