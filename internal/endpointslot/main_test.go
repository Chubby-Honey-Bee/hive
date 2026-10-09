package endpointslot

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// helperEnv, set in a helper's environment, makes this test binary a
// helper process that takes endpoint slots (helper) instead of running the
// tests. A helper starts no process, so none recurses.
const helperEnv = "HIVE_ENDPOINTSLOT_HELPER"

// holdCycle is how long a cycling helper holds its slot each time: long
// enough that a process with no slot would show up in its turn.
const holdCycle = 20 * time.Millisecond

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(helper(os.Args[1:]))
	}
	// The tests' lock files, and their helpers', stay out of the user's
	// cache directory.
	dir, err := os.MkdirTemp("", "endpointslot-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// helper takes slots at an endpoint, writing what it does under a work
// directory. It exits when its stdin closes, so it outlives no test.
//
//	hold <endpoint> <work>
//	    takes the slot, writes <work>/held, and holds it until stdin closes
//	again <endpoint> <work> <id>
//	    takes the slot, writes <work>/held, and holds it until it reads a
//	    line on stdin; then lets go and at once takes it again, appending
//	    "start <id>" and "end <id>" to <work>/log. It never takes a slot
//	    out of its turn (stallPolls), so next.lock alone decides who goes
//	    next
//	cycle <endpoint> <work> <id> <count>
//	    writes <work>/ready-<id>, then count times (0: until <work>/stop
//	    exists) takes the slot, appends "start <id>" to <work>/log, holds
//	    it holdCycle, appends "end <id>" and lets it go
func helper(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "endpointslot helper: want <mode> <endpoint> <work>")
		return 2
	}
	mode, endpoint, work := args[0], args[1], args[2]
	switch mode {
	case "hold":
		release, _, err := Acquire(context.Background(), endpoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, "endpointslot helper:", err)
			return 2
		}
		if err := os.WriteFile(filepath.Join(work, "held"), nil, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "endpointslot helper:", err)
			return 2
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		release()
		return 0
	case "again":
		stallPolls = math.MaxInt
		id := args[3]
		release, _, err := Acquire(context.Background(), endpoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, "endpointslot helper:", err)
			return 2
		}
		if err := os.WriteFile(filepath.Join(work, "held"), nil, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "endpointslot helper:", err)
			return 2
		}
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return 3
		}
		release()
		release, _, err = Acquire(context.Background(), endpoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, "endpointslot helper:", err)
			return 2
		}
		appendLine(filepath.Join(work, "log"), "start "+id)
		appendLine(filepath.Join(work, "log"), "end "+id)
		release()
		return 0
	case "cycle":
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(3)
		}()
		id := args[3]
		count, _ := strconv.Atoi(args[4])
		if err := os.WriteFile(filepath.Join(work, "ready-"+id), nil, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "endpointslot helper:", err)
			return 2
		}
		for i := 0; count == 0 || i < count; i++ {
			if _, err := os.Stat(filepath.Join(work, "stop")); count == 0 && err == nil {
				return 0
			}
			release, _, err := Acquire(context.Background(), endpoint)
			if err != nil {
				fmt.Fprintln(os.Stderr, "endpointslot helper:", err)
				return 2
			}
			appendLine(filepath.Join(work, "log"), "start "+id)
			time.Sleep(holdCycle)
			appendLine(filepath.Join(work, "log"), "end "+id)
			release()
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "endpointslot helper: unknown mode", mode)
	return 2
}

// appendLine appends line to path in one write.
func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		panic(err)
	}
}
