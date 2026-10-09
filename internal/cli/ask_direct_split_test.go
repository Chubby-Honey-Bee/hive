package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// --direct-voice and --context-split default off on chb ask (swarm.md § The
// direct voice, § The context split).
func TestAskFlags_DirectVoiceAndContextSplitDefaults(t *testing.T) {
	cmd := newSwarmAskCmd()
	for name, want := range map[string]string{"direct-voice": "false", "context-split": "false"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("chb ask has no --%s", name)
		}
		if f.DefValue != want {
			t.Errorf("--%s defaults to %s, want %s", name, f.DefValue, want)
		}
	}
}

// Under --context-split the inputs ask dispatches carry each lens's part
// under its ContextKey, as ContextParts deals them, beside the whole
// context; without it they carry the question and the context alone.
func TestAskNoDispatch_ContextSplitInputs(t *testing.T) {
	foragersAbs, err := filepath.Abs("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_FORAGERS_DIR", foragersAbs)
	t.Setenv("HIVE_MAX_OUTPUT_TOKENS", "")
	t.Chdir(t.TempDir())
	all, err := foragers.Load(foragersAbs)
	if err != nil {
		t.Fatal(err)
	}
	swarm, err := foragers.Filter(all, []string{"minimal"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := "first paragraph\n\nsecond paragraph\nstill the second\n\nthird\n\nfourth\n\nfifth\n\nsixth\n\nseventh\n\neighth"

	for _, split := range []bool{false, true} {
		root := &cobra.Command{Use: "chb", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(newSwarmAskCmd())
		var stderr bytes.Buffer
		root.SetErr(&stderr)
		root.SetOut(&bytes.Buffer{})
		args := []string{"ask", "q", "--no-dispatch", "--no-eval", "--foragers", "minimal", "--context", ctx, "--out", filepath.Join(t.TempDir(), "swarm.yaml")}
		if split {
			args = append(args, "--context-split")
		}
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("ask --no-dispatch: %v\n%s", err, stderr.String())
		}
		var inputsJSON string
		for _, l := range strings.Split(stderr.String(), "\n") {
			if i := strings.Index(l, "'--inputs' '"); i >= 0 {
				rest := l[i+len("'--inputs' '"):]
				inputsJSON = strings.ReplaceAll(rest[:strings.Index(rest, "' '--branch'")], `'\''`, "'")
			}
		}
		if inputsJSON == "" {
			t.Fatalf("no --inputs in the printed command:\n%s", stderr.String())
		}
		var inputs map[string]string
		if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
			t.Fatalf("inputs %q: %v", inputsJSON, err)
		}
		want := map[string]string{"question": "q", "context": ctx}
		if split {
			for k, v := range foragers.ContextParts(swarm, ctx) {
				want[k] = v
			}
		}
		if !reflect.DeepEqual(inputs, want) {
			t.Errorf("split %v: inputs %q, want %q", split, inputs, want)
		}
	}
}
