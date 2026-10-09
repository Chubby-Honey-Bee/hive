package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// commandHelperEnv, set in a test's environment, makes this test binary act
// as the program a command node runs. A command node resolves `chb` to the
// running binary, which under `go test` is this one.
const commandHelperEnv = "HIVE_RUNNER_COMMAND_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(commandHelperEnv) == "1" {
		os.Exit(commandHelper(os.Args[1:]))
	}
	// A call to a test server on this machine holds an endpoint slot's lock
	// file; keep them out of the user's cache directory.
	dir, err := os.MkdirTemp("", "endpointslot-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	code := runWithShippedModels(m)
	os.RemoveAll(dir)
	os.Exit(code)
}

// runWithShippedModels runs the tests on the models config the binary
// embeds and no routing profile, whatever the user's own models.yaml and
// HIVE_PROFILE say; a test that needs a config of its own points
// HIVE_MODELS_PATH at it. A child test process this one starts keeps the
// config its test gives it (shippedModelsPinned).
func runWithShippedModels(m *testing.M) int {
	if os.Getenv(shippedModelsPinned) == "1" {
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "runner-test-models-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	os.Setenv(shippedModelsPinned, "1")
	os.Setenv("HIVE_MODELS_PATH", filepath.Join(dir, "absent.yaml"))
	os.Unsetenv("HIVE_PROFILE")
	return m.Run()
}

// shippedModelsPinned marks a process whose models config a test process
// has pinned already.
const shippedModelsPinned = "HIVE_TEST_MODELS_PINNED"

// commandHelper is the fake program. Its first argument picks what it does:
//
//	print <text>        writes text to stdout, exits 0
//	exit <code> <text>  writes text to stderr, exits code
//	args <a>...         prints {"args": [...]} — what argv arrived as
//	stdin               prints {"stdin": "<what it read>"}
//	env <NAME>          prints {"value": "<$NAME>"}
//	pwd                 prints {"dir": "<working directory>"}
//	stdoutexit <code> <text>  writes text to stdout, exits code
//	spawn <pidfile>     starts a child that sleeps, writes its pid to
//	                    pidfile, then sleeps itself
//	sleep               sleeps for a minute
func commandHelper(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "command helper: no mode")
		return 2
	}
	emit := func(v any) int {
		b, _ := json.Marshal(v)
		fmt.Print(string(b))
		return 0
	}
	switch args[0] {
	case "print":
		fmt.Print(args[1])
		return 0
	case "exit":
		code, _ := strconv.Atoi(args[1])
		fmt.Fprint(os.Stderr, args[2])
		return code
	case "args":
		return emit(map[string]any{"args": args[1:]})
	case "stdin":
		b, _ := io.ReadAll(os.Stdin)
		return emit(map[string]any{"stdin": string(b)})
	case "env":
		return emit(map[string]any{"value": os.Getenv(args[1])})
	case "pwd":
		dir, _ := os.Getwd()
		return emit(map[string]any{"dir": dir})
	case "stdoutexit":
		code, _ := strconv.Atoi(args[1])
		fmt.Print(args[2])
		return code
	case "spawn":
		child := exec.Command(os.Args[0], "sleep")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "command helper: spawn:", err)
			return 2
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "command helper: spawn:", err)
			return 2
		}
		time.Sleep(time.Minute)
		return 0
	case "sleep":
		time.Sleep(time.Minute)
		return 0
	}
	fmt.Fprintln(os.Stderr, "command helper: unknown mode", args[0])
	return 2
}
