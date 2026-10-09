package db

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// SchemaVersion is the schema this binary creates. Bump it with any change a
// previous release could not read.
const SchemaVersion = 1

// initSchema creates every table and index. It is the whole schema: there
// are no migrations. Everything is CREATE ... IF NOT EXISTS, so re-running
// it on a current database changes nothing and concurrent first opens are
// safe without a lock.
//
// A database whose tables predate the current schema must be deleted and
// then created again with `chb db-init`: running db-init on such a file
// keeps its tables and rows and stamps it current, and later writes fail on
// the missing columns.
//
// The schema version is stamped into PRAGMA user_version. A database stamped
// higher was written by a newer chb; this binary refuses it rather than
// creating its own tables beside ones it does not understand. A database
// with no stamp reads as version 0 and is not distinguished from a new file.
func initSchema(db *sql.DB) error {
	// Concurrent first opens of a new file race to switch it to WAL, and
	// SQLite answers the losers SQLITE_BUSY at once — the lock upgrade
	// involved skips busy_timeout. Every statement below is idempotent, so a
	// busy attempt is retried whole, for as long as busy_timeout's default.
	// Without this, eight processes opening one new database failed about one
	// run in ten.
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := applySchema(db)
		if !retrySchema(err, deadline) {
			return err
		}
		time.Sleep(time.Duration(10+rand.IntN(40)) * time.Millisecond)
	}
}

// retrySchema reports whether a schema attempt that ended in err is tried
// again: it was busy, and the deadline has not passed.
func retrySchema(err error, deadline time.Time) bool {
	return err != nil && isBusy(err) && !time.Now().After(deadline)
}

// isBusy reports whether err is SQLITE_BUSY, including its extended codes.
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// applySchema refuses a database stamped newer than SchemaVersion, runs
// every schema statement in order, then stamps the version.
func applySchema(db *sql.DB) error {
	version, err := checkSchemaVersion(db)
	if err != nil {
		return err
	}
	if err := execSchema(db); err != nil {
		return err
	}
	return stampSchemaVersion(db, version)
}

// checkSchemaVersion reads the database's schema version, refusing one
// newer than SchemaVersion.
func checkSchemaVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion {
		return 0, fmt.Errorf("database schema version %d is newer than this chb supports (%d): use the chb release that wrote it", version, SchemaVersion)
	}
	return version, nil
}

// statementHead is the start of stmt a failure names: its first 60 bytes
// (truncateBytes), or all of it when shorter.
func statementHead(stmt string) string {
	return truncateBytes(stmt, 60)
}

// execSchema runs every schema statement, in order.
func execSchema(db *sql.DB) error {
	for _, stmt := range schemaStatements() {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", statementHead(stmt), err)
		}
	}
	return nil
}

// stampSchemaVersion stamps SchemaVersion into a database read at an older
// version.
func stampSchemaVersion(db *sql.DB, version int) error {
	if version >= SchemaVersion {
		return nil
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
		return fmt.Errorf("stamp schema version: %w", err)
	}
	return nil
}

// schemaStatements is the whole schema, in the order it runs, the signals
// table and its indexes last.
func schemaStatements() []string {
	return append([]string{
		// ── DIMENSION REGISTRY ──
		`CREATE TABLE IF NOT EXISTS dimensions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			description TEXT,
			values_json TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── FINDINGS (CDE-encoded) ──
		`CREATE TABLE IF NOT EXISTS findings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wave INTEGER NOT NULL,
			agent TEXT NOT NULL,
			d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER,
			d5 INTEGER, d6 INTEGER, d7 INTEGER, d8 INTEGER,
			mss_label TEXT CHECK(mss_label IN (
				'definition','guarantee','assumption','unknown'
			)) NOT NULL DEFAULT 'assumption',
			finding TEXT NOT NULL,
			evidence TEXT,
			source_urls TEXT,
			convergence_count INTEGER DEFAULT 1,
			convergence_level TEXT CHECK(convergence_level IN ('high','medium','low')) DEFAULT 'low',
			depends_on_ids TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			-- Stamped by UpdateFinding. NULL means never edited since it was
			-- written, which is what lets the dreamer's reprove pass ask
			-- whether a dependency actually changed instead of inferring it.
			updated_at TIMESTAMP
		)`,

		// ── SOURCES ──
		`CREATE TABLE IF NOT EXISTS sources (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT UNIQUE NOT NULL,
			title TEXT,
			agent TEXT,
			wave INTEGER,
			what_it_contributed TEXT,
			primary_source INTEGER DEFAULT 0,
			fetched_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			validation_status TEXT DEFAULT 'unchecked',
			validated_at TIMESTAMP,
			http_status INTEGER
		)`,

		// ── GAPS ──
		`CREATE TABLE IF NOT EXISTS gaps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wave INTEGER NOT NULL,
			agent TEXT NOT NULL,
			description TEXT NOT NULL,
			d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER,
			priority TEXT CHECK(priority IN ('critical','important','minor')) DEFAULT 'important',
			resolved_by_wave INTEGER,
			resolved_by_agent TEXT,
			resolution_finding_id INTEGER REFERENCES findings(id),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── CONFLICTS ──
		`CREATE TABLE IF NOT EXISTS conflicts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wave INTEGER NOT NULL,
			finding_a_id INTEGER REFERENCES findings(id),
			finding_b_id INTEGER REFERENCES findings(id),
			description TEXT NOT NULL,
			resolution TEXT,
			resolved_by_wave INTEGER,
			winner_finding_id INTEGER REFERENCES findings(id),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		// The detection passes' record-once probe (ConflictsRepo.RecordOnce).
		`CREATE INDEX IF NOT EXISTS idx_conflicts_pair ON conflicts(finding_a_id, finding_b_id)`,

		// ── FOLLOW-UPS ──
		`CREATE TABLE IF NOT EXISTS followups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wave INTEGER NOT NULL,
			agent TEXT NOT NULL,
			question TEXT NOT NULL,
			d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER,
			priority TEXT CHECK(priority IN ('critical','important','minor')) DEFAULT 'important',
			assigned_to_wave INTEGER,
			assigned_to_agent TEXT,
			answered INTEGER DEFAULT 0,
			answer_finding_id INTEGER REFERENCES findings(id),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── EVALUATIONS ──
		`CREATE TABLE IF NOT EXISTS evaluations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wave INTEGER NOT NULL,
			coverage_score INTEGER CHECK(coverage_score BETWEEN 1 AND 5),
			depth_score INTEGER CHECK(depth_score BETWEEN 1 AND 5),
			source_score INTEGER CHECK(source_score BETWEEN 1 AND 5),
			actionability_score INTEGER CHECK(actionability_score BETWEEN 1 AND 5),
			mss_integrity_score INTEGER CHECK(mss_integrity_score BETWEEN 1 AND 5),
			verdict TEXT CHECK(verdict IN ('COMPLETE','NEEDS_MORE_WORK','NEEDS_MINOR_FOLLOWUP')),
			laundering_violations INTEGER DEFAULT 0,
			untraceable_guarantees INTEGER DEFAULT 0,
			redundant_assumptions INTEGER DEFAULT 0,
			notes TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── AGENT RUNS ──
		`CREATE TABLE IF NOT EXISTS agent_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wave INTEGER NOT NULL,
			agent_name TEXT NOT NULL,
			agent_type TEXT CHECK(agent_type IN (
				'researcher','analyst','evaluator','coder','creative','verifier',
				'coder-fix','comb-editor','hive-scout','self-reviewer'
			)),
			model TEXT,
			target_d1 INTEGER, target_d2 INTEGER, target_d3 INTEGER, target_d4 INTEGER,
			prompt_summary TEXT,
			status TEXT CHECK(status IN ('running','completed','failed')) DEFAULT 'running',
			tool_uses INTEGER,
			duration_ms INTEGER,
			total_tokens INTEGER,
			summary TEXT,
			started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP
		)`,

		// ── WAVE GATES ──
		`CREATE TABLE IF NOT EXISTS wave_gates (
			wave INTEGER PRIMARY KEY,
			evaluation_id INTEGER REFERENCES evaluations(id),
			mss_audit_passed INTEGER NOT NULL DEFAULT 0,
			source_check_passed INTEGER NOT NULL DEFAULT 0,
			conflict_check_passed INTEGER NOT NULL DEFAULT 0,
			agents_completed INTEGER NOT NULL DEFAULT 0,
			gated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── WORKFLOW RUNS ──
		`CREATE TABLE IF NOT EXISTS workflow_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_name TEXT NOT NULL,
			workflow_version INTEGER NOT NULL DEFAULT 1,
			definition_yaml TEXT NOT NULL,
			inputs_json TEXT NOT NULL,
			state_json TEXT NOT NULL DEFAULT '{}',
			status TEXT CHECK(status IN ('running','completed','failed','paused')) DEFAULT 'running',
			started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP,
			-- The constraint probes' usage: model calls the run made for no
			-- node, so no node row carries them. Every run total adds them.
			probe_tokens_in INTEGER DEFAULT 0,
			probe_tokens_out INTEGER DEFAULT 0,
			probe_cost_usd_x10000 INTEGER DEFAULT 0,
			probe_metered_calls INTEGER DEFAULT 0,
			probe_unmetered_calls INTEGER DEFAULT 0
		)`,

		// ── WORKFLOW NODE STATES ──
		`CREATE TABLE IF NOT EXISTS workflow_node_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES workflow_runs(id),
			node_name TEXT NOT NULL,
			node_type TEXT NOT NULL,
			status TEXT CHECK(status IN (
				'pending','running','completed','failed','skipped','waiting_human','rejected'
			)) DEFAULT 'pending',
			outputs_json TEXT,
			error TEXT,
			attempt INTEGER DEFAULT 0,
			started_at TIMESTAMP,
			completed_at TIMESTAMP,
			rationale TEXT,
			tokens_in INTEGER DEFAULT 0,
			tokens_out INTEGER DEFAULT 0,
			cost_usd_x10000 INTEGER DEFAULT 0,
			provider TEXT,
			resolved_model TEXT,
			-- The endpoint that served the node's latest call, for the
			-- providers whose host the runner chooses (openai, gemini,
			-- anthropic). NULL for the CLI backends.
			base_url TEXT,
			-- The model calls charged to the node, split by whether the
			-- models config prices their model. cost_usd_x10000 holds only
			-- the metered calls' cost: an unmetered call's is unknown.
			metered_calls INTEGER DEFAULT 0,
			unmetered_calls INTEGER DEFAULT 0,
			-- The node's model calls whose reply the provider stopped at
			-- the output cap. The Gemini CLI reports no stop reason, so a
			-- node it served counts none.
			cutoff_calls INTEGER DEFAULT 0,
			-- For a node with an output_schema: 'enforced at decode' when
			-- its accepted text came from a call that sent the schema to a
			-- model the constraint probe showed enforces it, else
			-- 'post-hoc only'. NULL for a node without one.
			schema_enforcement TEXT,
			UNIQUE(run_id, node_name)
		)`,

		// ── HIVE STATE ──
		`CREATE TABLE IF NOT EXISTS hive_state (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project TEXT NOT NULL UNIQUE,
			iteration INTEGER NOT NULL DEFAULT 0,
			phase TEXT NOT NULL DEFAULT 'scanning' CHECK(phase IN (
				'scanning','dispatching','waiting','gating','terminal'
			)),
			batch_size INTEGER NOT NULL DEFAULT 5,
			convergence_threshold INTEGER NOT NULL DEFAULT 3,
			model_tier TEXT NOT NULL DEFAULT 'sonnet',
			conflict_rate_threshold REAL NOT NULL DEFAULT 0.15,
			total_findings INTEGER DEFAULT 0,
			unresolved_gaps INTEGER DEFAULT 0,
			unresolved_conflicts INTEGER DEFAULT 0,
			mss_integrity TEXT DEFAULT 'UNKNOWN',
			label_skew REAL DEFAULT 0.0,
			terminal_reason TEXT,
			-- the highest pending signal id the last plan read; completing
			-- the iteration consumes signals up to it and no further
			signals_through INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── CAPPED FINDINGS ──
		// One row per finding the hive's quorum capped, written by the
		// cap_finding actions `chb hive next --apply` applies in its scan's
		// transaction (hive.RecordScan; hive.ApplyCaps, the test seam, makes
		// the same write on the write pool). The finding keeps its label:
		// agreement among agents is not a derivation. A new table, not a
		// findings column, so an existing database gains it on open.
		`CREATE TABLE IF NOT EXISTS capped_findings (
			finding_id INTEGER PRIMARY KEY REFERENCES findings(id),
			capped_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── HIVE ITERATIONS ──
		// One row per hive pass: the research state as `chb hive next`
		// began the pass and as `chb hive complete` ended it, which the
		// stall rule compares (hive.Stalled). A new table, so an existing
		// database gains it on open.
		`CREATE TABLE IF NOT EXISTS hive_iterations (
			project TEXT NOT NULL,
			iteration INTEGER NOT NULL,
			start_progress TEXT NOT NULL,
			end_progress TEXT,
			started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP,
			PRIMARY KEY (project, iteration)
		)`,

		// ── HIVE TIER LOG ──
		// One row per `chb hive next --apply` pass: what the model-tier rule
		// decided, why, and its clock after the pass, which the next scan
		// reads (hive.ScanState). A new table, so an existing database gains
		// it on open.
		`CREATE TABLE IF NOT EXISTS hive_tier_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project TEXT NOT NULL,
			iteration INTEGER NOT NULL,
			wave INTEGER NOT NULL,
			unknowns_dominate INTEGER NOT NULL,
			from_tier TEXT NOT NULL,
			to_tier TEXT NOT NULL,
			outcome TEXT NOT NULL CHECK(outcome IN ('hold','rise','fall','clamp','not_applicable')),
			reason TEXT NOT NULL,
			wait_scans INTEGER NOT NULL,
			clear_scans INTEGER NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── CDE INDEXES ──
		// idx_cde_d1d2d3d4d5 supports the merge.go / conflict.go workload
		// (group findings by full coordinate); without it those passes scan
		// the full table. The (d1,d2,d3) and (d1,d2,d3,d4) prefixes, and a
		// d1-only probe on its leading column, are served by this same index
		// (B-tree prefix rule), so there are no separate prefix indexes
		// (cde-mss.md § Index Set Derived from the Workload).
		`CREATE INDEX IF NOT EXISTS idx_cde_d1d2d3d4d5 ON findings(d1, d2, d3, d4, d5)`,
		// idx_cde_d2 / idx_cde_d3 / idx_findings_convergence intentionally NOT
		// created — analysis showed they're never used by the planner (low
		// cardinality + always queried with d1) and just cost write throughput.
		// Composite index supports ORDER BY convergence_count DESC, created_at DESC
		// in Probe queries, avoiding a full in-memory sort of the filtered result set.
		`CREATE INDEX IF NOT EXISTS idx_findings_convergence_created ON findings(convergence_count DESC, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_findings_wave ON findings(wave)`,
		`CREATE INDEX IF NOT EXISTS idx_findings_agent ON findings(agent)`,
		`CREATE INDEX IF NOT EXISTS idx_findings_mss ON findings(mss_label)`,
		// Partial index on depends_on_ids — supports MSSAudit + CascadeRevert
		// without indexing the (vast majority) NULL rows.
		`CREATE INDEX IF NOT EXISTS idx_findings_depends_on ON findings(depends_on_ids) WHERE depends_on_ids IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_gaps_priority ON gaps(priority)`,
		`CREATE INDEX IF NOT EXISTS idx_gaps_resolved ON gaps(resolved_by_wave)`,
		`CREATE INDEX IF NOT EXISTS idx_gaps_coords ON gaps(d1, d2, d3, d4)`,
		`CREATE INDEX IF NOT EXISTS idx_followups_answered ON followups(answered)`,
		`CREATE INDEX IF NOT EXISTS idx_sources_primary ON sources(primary_source)`,

		// Gate-pipeline support (gate.go reads each of these per gate run).
		`CREATE INDEX IF NOT EXISTS idx_agent_runs_wave_status ON agent_runs(wave, status)`,
		`CREATE INDEX IF NOT EXISTS idx_conflicts_wave_resolution ON conflicts(wave, resolution)`,
		`CREATE INDEX IF NOT EXISTS idx_evaluations_wave ON evaluations(wave, id DESC)`,

		// Workflow indexes
		`CREATE INDEX IF NOT EXISTS idx_wf_run_status ON workflow_runs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_run_name ON workflow_runs(workflow_name)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_node_run ON workflow_node_states(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_node_status ON workflow_node_states(status)`,

		// ── WORKFLOW DECISIONS ──
		// Persists every decision-node evaluation so post-hoc replay can
		// show *why* a branch was taken.
		`CREATE TABLE IF NOT EXISTS workflow_decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES workflow_runs(id),
			node_name TEXT NOT NULL,
			condition TEXT NOT NULL,
			evaluated_value TEXT NOT NULL,
			branch_taken TEXT,
			eval_error TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_decisions_run ON workflow_decisions(run_id)`,

		// ── WORKFLOW REPAIRS ──
		// One row per repair attempt triggered by a failed accept:
		// predicate. accept_passed=1 marks the attempt that actually
		// got the node back into 'completed' state.
		`CREATE TABLE IF NOT EXISTS workflow_repairs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES workflow_runs(id),
			node_name TEXT NOT NULL,
			attempt INTEGER NOT NULL,
			failure_reason TEXT NOT NULL,
			repair_prompt TEXT NOT NULL,
			repair_outputs_json TEXT,
			accept_passed INTEGER DEFAULT 0,
			tokens_in INTEGER DEFAULT 0,
			tokens_out INTEGER DEFAULT 0,
			cost_usd_x10000 INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP,
			-- The attempt ledger: the model and provider this attempt ran on,
			-- and what started it (accept: the previous attempt failed an
			-- accept: predicate; backend_error: the previous repair call
			-- failed).
			model TEXT,
			provider TEXT,
			trigger TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_repairs_run ON workflow_repairs(run_id)`,

		// ── COMB_STATE (the hive's shared belief surface) ──
		// One row per addressable vantage on the Comb, of two vantage_kinds:
		//   region: per-coordinate-prefix belief digest. vantage_key is the
		//           canonical coord-prefix string (""|d1=0|d1=0;d2=3|...).
		//   forager: per-forager verdict from a swarm run.
		//            vantage_key is "forager:<name>" (e.g. "forager:optimist").
		// The full revision history lives in comb_revisions; comb_state holds
		// the denormalised "head" pointer for fast reads.
		`CREATE TABLE IF NOT EXISTS comb_state (
			vantage_key TEXT PRIMARY KEY,
			vantage_kind TEXT NOT NULL DEFAULT 'region'
				CHECK(vantage_kind IN ('region','forager')),
			d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER,
			d5 INTEGER, d6 INTEGER, d7 INTEGER, d8 INTEGER,
			narrative TEXT NOT NULL DEFAULT '',
			confidence INTEGER CHECK(confidence BETWEEN 0 AND 100) NOT NULL DEFAULT 0,
			contested INTEGER NOT NULL DEFAULT 0,
			dominant_label TEXT,
			evidence_count INTEGER NOT NULL DEFAULT 0,
			open_questions_count INTEGER NOT NULL DEFAULT 0,
			digest_method TEXT NOT NULL DEFAULT 'heuristic',
			raw_json TEXT,
			last_revised_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_d1 ON comb_state(d1)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_contested ON comb_state(contested)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_kind_key ON comb_state(vantage_kind, vantage_key)`,

		// ── TIME_WHEEL (chronomantic: discretised, named time axis) ──
		// A tick is a bounded interval with a kind (swarm, wave, session,
		// day, manual, ripen, calibrate). Four producers write ticks: each
		// workflow run opens and closes a `swarm` tick (the runner),
		// `db-write gate_wave` records a closed `wave` tick, `chb ripen`
		// opens a `ripen` tick around its loop, and `chb calibrate` opens a
		// `calibrate` tick around a recompute. Ticks let temporal queries be
		// bounded probes:
		//   "what did the Comb believe at swarm tick #N?" → one row scan
		//   "show belief drift across the last 4 wave ticks" → 4-row scan
		// rather than free-form timestamp range scans. This is the WASP
		// move applied to time itself.
		`CREATE TABLE IF NOT EXISTS time_wheel (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			label TEXT NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN (
				'swarm','wave','session','day','manual','ripen','calibrate'
			)),
			run_id INTEGER REFERENCES workflow_runs(id),
			wave INTEGER,
			started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			ended_at TIMESTAMP,
			notes TEXT,
			UNIQUE(kind, label)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_time_wheel_kind ON time_wheel(kind, started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_time_wheel_run ON time_wheel(run_id)`,

		// ── COMB_REVISIONS (chronomantic: every comb write as immutable history) ──
		// Append-only log of every change to comb_state. Lets us answer
		// "what did the Comb believe about <vantage> at tick T?" with a
		// bounded probe (vantage_key + tick_id), and "what changed
		// between two ticks?" by diffing two revisions.
		`CREATE TABLE IF NOT EXISTS comb_revisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			vantage_key TEXT NOT NULL,
			vantage_kind TEXT NOT NULL DEFAULT 'region',
			tick_id INTEGER REFERENCES time_wheel(id),
			narrative TEXT NOT NULL DEFAULT '',
			confidence INTEGER NOT NULL DEFAULT 0,
			contested INTEGER NOT NULL DEFAULT 0,
			dominant_label TEXT,
			evidence_count INTEGER NOT NULL DEFAULT 0,
			open_questions_count INTEGER NOT NULL DEFAULT 0,
			raw_json TEXT,
			source TEXT NOT NULL DEFAULT 'unknown',
			revision_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_rev_vantage_time
		   ON comb_revisions(vantage_key, revision_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_rev_kind_time
		   ON comb_revisions(vantage_kind, revision_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_rev_tick
		   ON comb_revisions(tick_id, vantage_key)`,

		// ── FORAGER_BONDS (∇ quorum firings) ──
		// One row per resonates pair whose verdicts converged within a run,
		// written by the quorum sensor with fired=1. Bonds are declared in
		// forager frontmatter; nothing records cites or contradicts bonds here.
		`CREATE TABLE IF NOT EXISTS forager_bonds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER REFERENCES workflow_runs(id),
			from_forager TEXT NOT NULL,
			to_forager TEXT NOT NULL,
			bond_kind TEXT NOT NULL
				CHECK(bond_kind IN ('cites','contradicts','resonates')),
			weight REAL NOT NULL DEFAULT 1.0,
			fired INTEGER NOT NULL DEFAULT 0,
			payload_json TEXT NOT NULL DEFAULT '{}',
			observed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_forager_bonds_run ON forager_bonds(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_forager_bonds_pair
		   ON forager_bonds(from_forager, to_forager, bond_kind)`,

		// ── RIPEN_LOG (the Dreamer forager's audit trail) ──
		// One row per consolidation pass `chb ripen` runs: the dreamer-archetype
		// forager's record of its work on the Comb between sessions.
		`CREATE TABLE IF NOT EXISTS ripen_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pass_name TEXT NOT NULL,
			status TEXT CHECK(status IN ('completed','skipped','halted','dry_run','failed')) NOT NULL,
			touched_count INTEGER DEFAULT 0,
			cost_usd_x10000 INTEGER DEFAULT 0,
			notes_json TEXT NOT NULL DEFAULT '{}',
			started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ripen_log_started ON ripen_log(started_at DESC)`,

		// ── COMB_EMBEDDINGS (semantic-similarity sidecar) ──
		// One row per (vantage_key, model). Embedding stored as a
		// raw float32 BLOB, dim recorded so the search layer can
		// reject mixed-dim queries before they pollute results.
		// UNIQUE(vantage_key, model) lets multiple models coexist —
		// queries filter by `WHERE model = ?` so vector spaces never
		// mix.
		`CREATE TABLE IF NOT EXISTS comb_embeddings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			vantage_key TEXT NOT NULL,
			vantage_kind TEXT NOT NULL CHECK(vantage_kind IN (
				'region','forager','finding','question'
			)),
			model TEXT NOT NULL,
			dim INTEGER NOT NULL,
			embedding BLOB NOT NULL,
			source_text TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(vantage_key, model)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_embed_vantage
		   ON comb_embeddings(vantage_key)`,
		`CREATE INDEX IF NOT EXISTS idx_comb_embed_model_dim
		   ON comb_embeddings(model, dim)`,
		// Open-access citation cache: avoids hammering Unpaywall on
		// every research run. One row per (doi, source) pair; reads
		// hit cache before HTTP. Stale rows older than 90 days are
		// re-checked on next access.
		`CREATE TABLE IF NOT EXISTS citation_oa_cache (
			doi          TEXT NOT NULL,
			source       TEXT NOT NULL,   -- "unpaywall" | "openalex"
			is_oa        INTEGER NOT NULL CHECK(is_oa IN (0,1)),
			oa_url       TEXT,
			license      TEXT,
			host_type    TEXT,
			title        TEXT,
			year         INTEGER,
			error        TEXT,
			verified_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (doi, source)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_citation_oa_doi
		   ON citation_oa_cache(doi)`,

		// tool_invocations — the per-node tool-call audit trail the runner
		// writes, created with the rest of the schema, so a database that
		// has never run a workflow holds it too.
		`CREATE TABLE IF NOT EXISTS tool_invocations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL,
			node_name TEXT NOT NULL,
			tool TEXT NOT NULL,
			input TEXT,
			output TEXT,
			is_error INTEGER DEFAULT 0,
			duration_s REAL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// scan_events — the WASP runtime scan detector's evidence log. Each
		// row is a distinct read-query shape that EXPLAIN QUERY PLAN
		// reported as a full SCAN (rather than an indexed SEARCH).
		// UNIQUE(query_shape) plus an ON CONFLICT upsert keeps one row per
		// shape while bumping hit_count and last_seen — not INSERT OR
		// IGNORE, which would throw away exactly the counters the row exists
		// to carry.
		`CREATE TABLE IF NOT EXISTS scan_events (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			query_shape  TEXT NOT NULL UNIQUE,
			table_name   TEXT NOT NULL DEFAULT '',
			detail       TEXT NOT NULL DEFAULT '',
			hit_count    INTEGER NOT NULL DEFAULT 1,
			first_seen   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// ── OUTCOMES (the calibration ledger) ──
		// Append-only: one row per resolution of a subject — a finding, a
		// lens verdict or a synthesis verdict — from a human, an external
		// import or an adjudicated conflict (downstream_run). subject_label
		// is the finding's mss_label when the outcome was recorded; the
		// cascade changes labels later, and scoring by the label at
		// recompute time would move old outcomes between predictors.
		// Tables of their own, not findings columns, so a database without
		// them gains them on open (cde-mss.md § Outcomes and calibration
		// scores).
		`CREATE TABLE IF NOT EXISTS outcomes (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			subject_kind      TEXT NOT NULL CHECK(subject_kind IN ('finding','lens_verdict','synthesis_verdict')),
			finding_id        INTEGER REFERENCES findings(id),
			subject_label     TEXT CHECK(subject_label IN ('definition','guarantee','assumption','unknown')),
			lens              TEXT,
			run_id            INTEGER REFERENCES workflow_runs(id),
			belief_tick_id    INTEGER REFERENCES time_wheel(id),
			d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER,
			resolution        TEXT NOT NULL CHECK(resolution IN ('confirmed','refuted','partial')),
			stated_confidence INTEGER CHECK(stated_confidence BETWEEN 0 AND 100),
			predicted_value   REAL,
			actual_value      REAL,
			source            TEXT NOT NULL CHECK(source IN ('human','downstream_run','external')),
			rationale         TEXT,
			evidence_urls     TEXT,
			resolved_tick_id  INTEGER REFERENCES time_wheel(id),
			resolved_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			CHECK (subject_kind != 'finding'           OR (finding_id IS NOT NULL AND subject_label IS NOT NULL)),
			CHECK (subject_kind != 'lens_verdict'      OR (lens IS NOT NULL AND run_id IS NOT NULL)),
			CHECK (subject_kind != 'synthesis_verdict' OR run_id IS NOT NULL)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_outcomes_finding ON outcomes(finding_id)`,
		`CREATE INDEX IF NOT EXISTS idx_outcomes_lens ON outcomes(lens)`,
		`CREATE INDEX IF NOT EXISTS idx_outcomes_run ON outcomes(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_outcomes_coords ON outcomes(d1, d2)`,

		// ── CALIBRATION_SCORES ──
		// One row per (predictor_kind, predictor_key, scope_key), rewritten
		// by each recompute from the whole ledger. The counts are what the
		// formula reads; hit_rate, weight, calibrated and brier_score are
		// derived from them. updated_tick_id is the calibrate tick that last
		// changed the row, so a recompute that changes nothing leaves the
		// table byte-identical.
		`CREATE TABLE IF NOT EXISTS calibration_scores (
			predictor_kind  TEXT NOT NULL CHECK(predictor_kind IN ('lens','label','convergence','synthesizer')),
			predictor_key   TEXT NOT NULL,
			scope_key       TEXT NOT NULL DEFAULT '',
			n_resolved      INTEGER NOT NULL DEFAULT 0,
			n_confirmed     INTEGER NOT NULL DEFAULT 0,
			n_refuted       INTEGER NOT NULL DEFAULT 0,
			n_partial       INTEGER NOT NULL DEFAULT 0,
			hit_rate        REAL NOT NULL DEFAULT 0,
			brier_sum       REAL NOT NULL DEFAULT 0,
			brier_n         INTEGER NOT NULL DEFAULT 0,
			brier_score     REAL,
			weight          REAL NOT NULL DEFAULT 1.0,
			calibrated      INTEGER NOT NULL DEFAULT 0,
			updated_tick_id INTEGER REFERENCES time_wheel(id),
			PRIMARY KEY (predictor_kind, predictor_key, scope_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_calibration_scores_kind_scope ON calibration_scores(predictor_kind, scope_key)`,

		// ── CALIBRATION_REVISIONS ──
		// Append-only: a snapshot of each score row a calibrate tick
		// changed, so "how calibrated was this lens at tick T" is a bounded
		// probe on (tick_id, predictor_key).
		`CREATE TABLE IF NOT EXISTS calibration_revisions (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			tick_id         INTEGER NOT NULL REFERENCES time_wheel(id),
			predictor_kind  TEXT NOT NULL,
			predictor_key   TEXT NOT NULL,
			scope_key       TEXT NOT NULL DEFAULT '',
			n_resolved      INTEGER NOT NULL DEFAULT 0,
			n_confirmed     INTEGER NOT NULL DEFAULT 0,
			n_refuted       INTEGER NOT NULL DEFAULT 0,
			n_partial       INTEGER NOT NULL DEFAULT 0,
			hit_rate        REAL NOT NULL DEFAULT 0,
			brier_sum       REAL NOT NULL DEFAULT 0,
			brier_n         INTEGER NOT NULL DEFAULT 0,
			brier_score     REAL,
			weight          REAL NOT NULL DEFAULT 1.0,
			calibrated      INTEGER NOT NULL DEFAULT 0,
			revision_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_calibration_revisions_tick_key ON calibration_revisions(tick_id, predictor_key)`,

		// finding_confirmation counts a finding's outcomes per resolution.
		`CREATE VIEW IF NOT EXISTS finding_confirmation AS
		 SELECT finding_id,
		        COUNT(*) AS n_outcomes,
		        SUM(resolution = 'confirmed') AS n_confirmed,
		        SUM(resolution = 'refuted')   AS n_refuted,
		        SUM(resolution = 'partial')   AS n_partial
		 FROM outcomes
		 WHERE finding_id IS NOT NULL
		 GROUP BY finding_id`,
	}, SignalsSchema()...)
}

// SignalsSchema is the signals table and its indexes, the statements the
// schema creates them with. `chb db-repair --table signals` rebuilds the
// table from them, so a repaired table is the one a new store has.
func SignalsSchema() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS signals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			signal_type TEXT NOT NULL CHECK(signal_type IN (
				'waggle_dance','stop_signal','alarm','tremble_dance',
				'shaking_signal','quorum','qmp',
				'nabla','chronomantic_drift'
			)),
			source_type TEXT CHECK(source_type IN (
				'finding','conflict','gap','gate','audit','system'
			)),
			source_id INTEGER,
			target_d1 INTEGER, target_d2 INTEGER,
			target_d3 INTEGER, target_d4 INTEGER,
			payload_json TEXT NOT NULL DEFAULT '{}',
			acted_on INTEGER DEFAULT 0,
			wave INTEGER,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_signals_type ON signals(signal_type)`,
		`CREATE INDEX IF NOT EXISTS idx_signals_acted ON signals(acted_on)`,
		`CREATE INDEX IF NOT EXISTS idx_signals_coords ON signals(target_d1, target_d2, target_d3, target_d4)`,
		`CREATE INDEX IF NOT EXISTS idx_signals_created ON signals(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_signals_wave ON signals(wave)`,
	}
}
