package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The MCP docker wrapper, scripts/chb-mcp-docker.sh, runs the container
// with the arguments the README's MCP configuration shows.
func TestDockerWrapperRunsTheContainer(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	tmp := t.TempDir()
	fakeDocker := filepath.Join(tmp, "docker")
	if err := os.WriteFile(fakeDocker, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(tmp, "ws")
	image := "example/chb:test"
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + tmp,
		"CHB_DOCKER=" + fakeDocker,
		"CHB_IMAGE=" + image,
		"CHB_WORKSPACE=" + workspace,
	}
	run := func(script string) []string {
		t.Helper()
		cmd := exec.Command(bash, filepath.Join("..", "..", "scripts", script), "--flag", "two words")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	}

	argv := run("chb-mcp-docker.sh")
	want := []string{
		"run", "--rm", "-i", "--pull=missing",
		"-v", workspace + ":/app/workspace",
		"-e", "HIVE_DB_PATH=/app/workspace/hive.db",
		image, "--flag", "two words",
	}
	if !slices.Equal(argv, want) {
		t.Errorf("chb-mcp-docker.sh ran docker with\n%q\nwant\n%q", argv, want)
	}
}
