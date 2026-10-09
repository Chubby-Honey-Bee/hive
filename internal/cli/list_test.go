package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A synthesizer holds no lens, so `chb list` lists the Queen on her own line
// after the roster, outside the forager count.
func TestListShowsTheQueenApartFromTheForagers(t *testing.T) {
	dir := t.TempDir()
	personas := []struct{ name, archetype string }{
		{"alpha", "lens"},
		{"beta", "dreamer"},
		{"gamma", "lens"},
		{"queen", "synthesizer"},
	}
	foragerCount := 0
	for _, p := range personas {
		body := fmt.Sprintf("---\nname: %s\narchetype: %s\ndescription: the %s persona\n---\nbody\n", p.name, p.archetype, p.name)
		if err := os.WriteFile(filepath.Join(dir, p.name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if p.archetype != "synthesizer" {
			foragerCount++
		}
	}
	t.Setenv("HIVE_FORAGERS_DIR", dir)

	var out bytes.Buffer
	cmd := newSwarmListCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--color", "never"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if want := fmt.Sprintf("HIVE — %d foragers available", foragerCount); !strings.HasPrefix(text, want) {
		t.Errorf("header does not start %q:\n%s", want, text)
	}
	roster, synth, found := strings.Cut(text, "Synthesizer")
	if !found {
		t.Fatalf("no Synthesizer section:\n%s", text)
	}
	for _, p := range personas {
		line := " " + p.name + " "
		inRoster, inSynth := strings.Contains(roster, line), strings.Contains(synth, line)
		if isSynth := p.archetype == "synthesizer"; inRoster == isSynth || inSynth != isSynth {
			t.Errorf("%s (%s): in roster %v, in synthesizer section %v", p.name, p.archetype, inRoster, inSynth)
		}
	}
}

// HIVE_FORAGERS_DIR wins whatever it holds, so an empty one is an error,
// not the carried copy. The hint names the directory, the variable and the
// carried copy, and no build command, which is no remedy for an empty
// directory.
func TestNoForagersHintNamesChb(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVE_FORAGERS_DIR", dir)
	cmd := newSwarmListCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("an empty foragers directory listed without error")
	}
	msg := err.Error()
	for _, want := range []string{dir, "the copy it carries", "HIVE_FORAGERS_DIR"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "go build") {
		t.Errorf("error %q points at `go build`", msg)
	}
}

// chb list shows each shipped forager's description, the line every ask
// also prints for it; each says what the lens does, and none cites the
// project's development: a template version, a self-evaluation, a canary
// run or a wave of evaluation.
func TestListDescriptionsCiteNoDevelopment(t *testing.T) {
	t.Setenv("HIVE_FORAGERS_DIR", filepath.Join("..", "..", "foragers"))
	var out bytes.Buffer
	cmd := newSwarmListCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--color", "never"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	development := regexp.MustCompile(`(?i)template[- ]?v\d|self-evaluation|canary|\bwave \d`)
	for _, line := range strings.Split(out.String(), "\n") {
		if m := development.FindString(line); m != "" {
			t.Errorf("%q cites the project's development (%q)", strings.TrimSpace(line), m)
		}
	}
}
