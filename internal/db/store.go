package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// BusyTimeoutPragma is the busy_timeout pragma, in DSN form, that every
// connection to a workspace database sets: HIVE_SQLITE_BUSY_TIMEOUT_MS,
// 30000 by default.
func BusyTimeoutPragma() string {
	return "_pragma=busy_timeout(" + envIntDefault("HIVE_SQLITE_BUSY_TIMEOUT_MS", "30000") + ")"
}

// envIntDefault returns os.Getenv(key) if it parses as a positive int,
// else fallback. Used by NewStore to make pragma tuning runtime-overridable.
func envIntDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return v
		}
	}
	return fallback
}

// Store is the registry over per-entity repositories plus the shared
// connection pools. It embeds no repo: the per-entity repos are private
// fields, reached only through the typed accessors (Findings, Gaps, …).
//
// What lives on Store directly:
//   - Connection pools (WriteDB, ReadDB) — stay public because runner,
//     hive, and analysis use raw SQL for cross-cutting queries that
//     don't fit a per-entity repo.
//   - Cross-table operations: CascadeRevert, GetSummary —
//     these coordinate writes across multiple repos and the gate/audit
//     primitive ops in cascade.go.
//   - WriteRecord, the write path chb_db_write and chb db-write share,
//     which writes each kind of record through its repo.
//   - Init/Close lifecycle.
//   - ReadConn() so cross-cutting consumers (audit) can satisfy a
//     narrow data-source interface.
//
// Writer pool: MaxOpenConns=1 (serialized writes, SQLite constraint).
// Reader pool: MaxOpenConns=NumCPU (parallel CDE probes in WAL mode).
type Store struct {
	WriteDB *sql.DB
	ReadDB  *sql.DB
	Path    string
	mu      sync.Mutex // protects schema init

	// Per-entity repos. Private — reached through typed accessors below.
	findings       *FindingsRepo
	gaps           *GapsRepo
	conflicts      *ConflictsRepo
	sources        *SourcesRepo
	evaluations    *EvaluationsRepo
	dimensions     *DimensionsRepo
	followups      *FollowupsRepo
	agentRuns      *AgentRunsRepo
	signals        *SignalsRepo
	workflows      *WorkflowsRepo
	comb           *CombRepo
	combRevisions  *CombRevisionsRepo
	timeWheel      *TimeWheelRepo
	foragerBonds   *ForagerBondsRepo
	ripen          *RipenRepo
	combEmbeddings *CombEmbeddingsRepo
	outcomes       *OutcomesRepo
	calibration    *CalibrationRepo
}

// sqliteDSN builds the `file:` URI for a database path. The path is
// percent-encoded first: SQLite reads everything after a bare `?` as query
// parameters and everything after `#` as a fragment, so a workspace under a
// directory containing `?`, `#` or `%` silently opened a *different* database
// (or none). `%` must be escaped first or it would double-encode the rest.
func sqliteDSN(path, pragmas string) string {
	esc := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
	return "file:" + esc + "?" + pragmas
}

// NewStore opens (or creates) an SQLite database at path with WAL mode,
// foreign keys, and the two-pool connection pattern.
//
// Writer pragmas tuned for sustained burst-write workloads (high
// throughput from concurrent agent runs):
//
//   - busy_timeout(30000) — 30-second wait before SQLITE_BUSY surfaces
//     to the caller, which then fails: there is no caller-side retry
//     layer, except initSchema's for the switch to WAL, which SQLite
//     refuses without waiting. 30s is well over any healthy WAL checkpoint.
//   - wal_autocheckpoint(1000) — checkpoint every 1000 pages (~4MB at
//     4KB pages). Keeps the WAL from growing unbounded under sustained
//     write load.
//   - cache_size(-65536) — 64 MB page cache (negative = KB). Default
//     2MB is too small for our finding-ingest hot path; 64MB keeps
//     hot indexes resident.
//   - mmap_size(268435456) — 256 MB memory-mapped I/O. Speeds up the
//     CDE-coordinate index lookups that dominate the read path.
//
// Override via HIVE_SQLITE_BUSY_TIMEOUT_MS,
// HIVE_SQLITE_CACHE_KB, HIVE_SQLITE_MMAP_BYTES.
func NewStore(path string) (*Store, error) {
	pragmas := "_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&" + BusyTimeoutPragma() +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=wal_autocheckpoint(1000)" +
		"&_pragma=cache_size(-" + envIntDefault("HIVE_SQLITE_CACHE_KB", "65536") + ")" +
		"&_pragma=mmap_size(" + envIntDefault("HIVE_SQLITE_MMAP_BYTES", "268435456") + ")" +
		"&_pragma=temp_store(MEMORY)"

	dsn := sqliteDSN(path, pragmas)
	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open write db: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetConnMaxLifetime(0) // never recycle the writer
	writeDB.SetConnMaxIdleTime(0) // never close idle (single conn)

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("open read db: %w", err)
	}
	readDB.SetMaxOpenConns(runtime.NumCPU())
	readDB.SetMaxIdleConns(runtime.NumCPU())
	readDB.SetConnMaxLifetime(0)

	return &Store{
		WriteDB: writeDB,
		ReadDB:  readDB,
		Path:    path,

		findings:       newFindingsRepo(writeDB, readDB),
		gaps:           NewGapsRepo(writeDB, readDB),
		conflicts:      NewConflictsRepo(writeDB, readDB),
		sources:        NewSourcesRepo(writeDB, readDB),
		evaluations:    newEvaluationsRepo(writeDB, readDB),
		dimensions:     newDimensionsRepo(writeDB, readDB),
		followups:      newFollowupsRepo(writeDB, readDB),
		agentRuns:      newAgentRunsRepo(writeDB, readDB),
		signals:        newSignalsRepo(writeDB, readDB),
		workflows:      newWorkflowsRepo(writeDB, readDB),
		comb:           NewCombRepo(writeDB, readDB),
		combRevisions:  newCombRevisionsRepo(writeDB, readDB),
		timeWheel:      newTimeWheelRepo(writeDB, readDB),
		foragerBonds:   newForagerBondsRepo(writeDB, readDB),
		ripen:          NewRipenRepo(writeDB, readDB),
		combEmbeddings: newCombEmbeddingsRepo(writeDB, readDB),
		outcomes:       newOutcomesRepo(writeDB, readDB),
		calibration:    newCalibrationRepo(writeDB, readDB),
	}, nil
}

// Close closes both connection pools, joining any errors so neither
// is silently discarded.
func (s *Store) Close() error {
	return errors.Join(s.WriteDB.Close(), s.ReadDB.Close())
}

// Init creates all tables and indexes if they don't exist.
func (s *Store) Init() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return initSchema(s.WriteDB)
}

// ReadConn returns the read-side connection pool. Provided so readers
// that query with SQL — the comb's builder and staleness checks, the CDE
// axis suggester, the CLI's gaps and comb commands — can take a *sql.DB
// without depending on the *Store struct itself.
func (s *Store) ReadConn() *sql.DB { return s.ReadDB }

// ── Typed repo accessors ─────────────────────────────────────────
//
// New consumers depend on the focused type they need:
//
//   svc := MyService{repo: store.Findings()}     // narrow dependency
//
// rather than the whole Store. The repos are private; this is the only
// public way to reach them.

// Findings returns the focused FindingsRepo.
func (s *Store) Findings() *FindingsRepo { return s.findings }

// Gaps returns the focused GapsRepo.
func (s *Store) Gaps() *GapsRepo { return s.gaps }

// Conflicts returns the focused ConflictsRepo.
func (s *Store) Conflicts() *ConflictsRepo { return s.conflicts }

// Sources returns the focused SourcesRepo.
func (s *Store) Sources() *SourcesRepo { return s.sources }

// Evaluations returns the focused EvaluationsRepo.
func (s *Store) Evaluations() *EvaluationsRepo { return s.evaluations }

// Dimensions returns the focused DimensionsRepo.
func (s *Store) Dimensions() *DimensionsRepo { return s.dimensions }

// Followups returns the focused FollowupsRepo.
func (s *Store) Followups() *FollowupsRepo { return s.followups }

// AgentRuns returns the focused AgentRunsRepo.
func (s *Store) AgentRuns() *AgentRunsRepo { return s.agentRuns }

// Signals returns the focused SignalsRepo.
func (s *Store) Signals() *SignalsRepo { return s.signals }

// Workflows returns the focused WorkflowsRepo.
func (s *Store) Workflows() *WorkflowsRepo { return s.workflows }

// Comb returns the focused CombRepo (the hive's shared belief surface): rows
// with vantage_kind ∈ {region, forager}, so foragers write per-forager
// verdicts onto the same surface that holds region digests.
func (s *Store) Comb() *CombRepo { return s.comb }

// CombRevisions returns the chronomantic history repo. Append-only;
// every Comb write also appends a revision row so the system can answer
// "what did the Comb believe about <vantage> at tick T?".
func (s *Store) CombRevisions() *CombRevisionsRepo { return s.combRevisions }

// TimeWheel returns the chronomantic-tick repo — the discretised time
// axis that turns "what did we believe last week?" from a free-form
// date scan into a bounded probe.
func (s *Store) TimeWheel() *TimeWheelRepo { return s.timeWheel }

// ForagerBonds returns the typed-bond repo — cites/contradicts/resonates
// relations between foragers, observed during swarm runs and used by
// the quorum sensor.
func (s *Store) ForagerBonds() *ForagerBondsRepo { return s.foragerBonds }

// Ripen returns the focused RipenRepo (the Dreamer forager's
// per-pass audit trail).
func (s *Store) Ripen() *RipenRepo { return s.ripen }

// CombEmbeddings returns the focused CombEmbeddingsRepo — the semantic
// sidecar over the Comb. Each row stores a packed float32 vector for a
// (vantage_key, model) pair. Pure-Go nearest-neighbour search lives
// in internal/embed.
func (s *Store) CombEmbeddings() *CombEmbeddingsRepo { return s.combEmbeddings }

// Outcomes returns the calibration ledger repo: append-only resolutions of
// findings, lens verdicts and synthesis verdicts.
func (s *Store) Outcomes() *OutcomesRepo { return s.outcomes }

// Calibration returns the repo over calibration_scores and
// calibration_revisions, which `chb calibrate` rewrites from the ledger.
func (s *Store) Calibration() *CalibrationRepo { return s.calibration }

// *Store carries no per-entity method delegations.
// Consumers reach the focused repos through the typed accessors above
// (Findings, Gaps, Workflows, …). Cross-table operations remain on
// Store (CascadeRevert, GetSummary, MSSAudit, plus the
// cascade primitive ops in cascade.go).
