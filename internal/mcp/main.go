// Package mcp is chb-mcp, a stdio MCP server (JSON-RPC 2.0) that exposes
// HIVE to any MCP-aware client (e.g. Cursor). It listens on no port: stdin
// and stdout carry the protocol, and a run its tools start is a `chb`
// process whose log the result names.
//
// Tools, by family: the five below are in tools_db.go, tools_research.go
// and tools_status.go; the other 15 are in tools.go and the other
// tools_*.go files.
//
//	chb_db_write      — write CDE-encoded findings/gaps/sources; close gaps/conflicts
//	chb_research      — start a multi-wave research run
//	chb_status        — poll current phase / wave / agent counts
//	chb_summary       — the full summary from db.GetSummary()
//	chb_findings      — query findings by wave / label / limit
//
// Configuration:
//
//	HIVE_DB_PATH       — required: SQLite DB file path
//	ANTHROPIC_API_KEY      — optional: if set uses SDK backend; otherwise falls back to
//	                         the `claude` CLI (Claude Code headless) with its existing OAuth credentials
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
)

// version is the version the server reports: what Main is given, which the
// build injects into cmd/chb-mcp (-X main.version=…); "dev" otherwise.
var version = "dev"

type mcpServer struct {
	store       *db.Store
	mu          sync.Mutex
	out         *json.Encoder
	binOnce     sync.Once // resolves the chb binary once; see chbBin
	bin         string
	spawnLimit  *spawnLimiter
	limiterOnce sync.Once
	inflight    *inflightRegistry
	reqWG       sync.WaitGroup
	outMu       sync.Mutex
	runsWG      sync.WaitGroup
	runs        sync.Map // project → context.CancelFunc
	// draining is set, under drainMu, when the shutdown begins. A request
	// handleLine reads after it is refused, so nothing joins reqWG once the
	// shutdown waits on it, and no research run starts that the shutdown's
	// cancel sweep has passed.
	drainMu  sync.Mutex
	draining bool
	// logCounter is incremented each time runLogPath is called so
	// concurrent spawns within the same wall-clock second still get
	// unique log files (the seconds-resolution timestamp collided when
	// two agent_run tools fired back-to-back during verification).
	logCounter int64
	// budgetMode is the progressive cost-tier mode applied to the runs
	// that chb_research, chb_agent_run, chb_swarm, chb_self_review and
	// chb_self_implement start. Empty means inherit HIVE_BUDGET_MODE;
	// set via chb_set_budget_mode to override for the duration of the
	// session.
	budgetMode string
}

// shutdownGracePeriod bounds how long shutdown waits for in-flight
// requests and research goroutines before forcing termination. Chosen to be
// short enough that an MCP client never feels the difference, long enough
// that an in-flight DB write commits.
const shutdownGracePeriod = 10 * time.Second

// Main runs the server over stdin and stdout and exits the process when
// the host closes stdin or signals it. v is the version it reports.
func Main(v string) {
	// A host that exits with a request in flight leaves stdout a pipe with
	// no reader, where SIGPIPE would end the process at the next write.
	// Caught on a channel no one reads, it does not: the write fails,
	// write() drops it, and the ordered shutdown runs. It is caught, not
	// ignored, because an ignored signal stays ignored in every program the
	// server starts. Windows has no SIGPIPE; the call does nothing there.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	version = v
	useragent.Version = version
	if versionRequested(os.Args) {
		// The one argument a person types at it, answered rather than read
		// as a server waiting on a terminal's stdin for a request that never
		// comes.
		fmt.Println("chb-mcp version " + version)
		return
	}
	s := &mcpServer{
		out:      json.NewEncoder(os.Stdout),
		inflight: newInflightRegistry(),
	}
	s.store = openStoreFromEnv()

	// A host stops an MCP server by closing stdin or by signalling it, and
	// both reach the ordered shutdown below: SIGTERM or Ctrl-C does not kill
	// the process mid-write and skip the drain.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	stdinDone := make(chan struct{})
	go s.readRequests(os.Stdin, stdinDone)
	awaitStop(stdinDone, sigs)

	// Ordered shutdown: cancel research goroutines, drain requests, then
	// close the DB. Order matters — closing the DB first would race against
	// in-flight SQL writes from the request handlers and the runner
	// goroutines.
	s.shutdown()
}

// versionRequested reports whether the command line asks for the version:
// --version or -v.
func versionRequested(args []string) bool {
	return len(args) > 1 && (args[1] == "--version" || args[1] == "-v")
}

// openStoreFromEnv opens the database HIVE_DB_PATH names and makes its
// schema; nil when the variable is unset. A database that cannot be opened
// or initialized ends the process.
func openStoreFromEnv() *db.Store {
	path := os.Getenv("HIVE_DB_PATH")
	if path == "" {
		return nil
	}
	store, err := db.NewStore(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chb-mcp: open db %s: %v\n", path, err)
		os.Exit(1)
	}
	if err := store.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "chb-mcp: init schema: %v\n", err)
		store.Close()
		os.Exit(1)
	}
	return store
}

// readRequests serves the requests stdin carries, one a line, until stdin
// ends, then closes done. A blank line is not a message, and draws no
// answer.
func (s *mcpServer) readRequests(stdin io.Reader, done chan<- struct{}) {
	defer close(done)
	r := bufio.NewReader(stdin)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			s.handleLine(line)
		}
		if err != nil {
			reportStdinEnd(err)
			return
		}
	}
}

// reportStdinEnd logs a read error that ended stdin; EOF is its ordinary
// end.
func reportStdinEnd(err error) {
	if !errors.Is(err, io.EOF) {
		fmt.Fprintf(os.Stderr, "chb-mcp: read: %v\n", err)
	}
}

// awaitStop waits for the host to stop the server: it closes stdin (done)
// or signals it. Only the first signal starts the ordered shutdown: from
// here a second one has its default effect and ends the process while the
// drain runs.
func awaitStop(done <-chan struct{}, sigs chan os.Signal) {
	select {
	case <-done:
	case sig := <-sigs:
		signal.Stop(sigs)
		fmt.Fprintf(os.Stderr, "chb-mcp: %v — shutting down\n", sig)
	}
}

func (s *mcpServer) shutdown() {
	// 0. Refuse what arrives from here on: a request, and a research run
	// whose request came before (handleLine, handleResearch).
	s.drainMu.Lock()
	s.draining = true
	s.drainMu.Unlock()

	// 1. Cancel every research goroutine. Each cancel is a no-op if the run
	// already completed; cumulatively this is what stops a long-running
	// runner.Run from writing to a closed DB after we exit.
	s.cancelRuns()

	// 2. Let in-flight requests finish writing before the encoder and the DB
	// go away. Requests are served concurrently now, so without this a
	// handler could be mid-write when stdin closes.
	waitWithGrace(&s.reqWG, "requests still in flight")

	// 3. Wait for the cancelled runs to actually finish. Cancelling only
	// asks; a runner mid-write keeps writing until it observes the context.
	// Closing the store underneath it would race a live writer ("database is
	// closed", or a half-written node row).
	waitWithGrace(&s.runsWG, "runs still in flight; closing anyway")

	// 4. Close DB last so the prior steps see a live connection.
	if s.store != nil {
		s.store.Close()
	}
}

// cancelRuns cancels every research run the server registered.
func (s *mcpServer) cancelRuns() {
	s.runs.Range(func(_, value any) bool {
		if cancel, ok := value.(context.CancelFunc); ok {
			cancel()
		}
		return true
	})
}

// waitWithGrace waits for wg, for at most shutdownGracePeriod, and logs
// that the period elapsed with what is still running.
func waitWithGrace(wg *sync.WaitGroup, stillRunning string) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownGracePeriod):
		fmt.Fprintf(os.Stderr, "chb-mcp: %v grace period elapsed with %s\n", shutdownGracePeriod, stillRunning)
	}
}
