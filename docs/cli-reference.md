# CLI reference

The `chb` binary has 53 top-level commands, several with subcommands.
`chb --help` is the live list (with cobra's `completion` and `help` beside
them); the groups below are this page's, not the help screen's, which is
alphabetical. The MCP server's tools are at the end.

## Data layer

| Command | Purpose |
|---|---|
| `chb db-init` | Initialize a project's CDE-aware SQLite schema |
| `chb db-read` | Query findings, gaps, conflicts, dimensions and MSS audits |
| `chb db-write` | Write CDE-encoded findings with MSS enforcement; `finding`, `gap`, `source`, `resolve_gap` and `resolve_conflict` take the write path `chb_db_write` takes, with its refusals (a `wave` or other numeric field that is not a whole number, a text field that is not a string, a field the kind does not read) and its gap priorities (`high` is `important`, `medium` and `low` `minor`); every other kind refuses a numeric or text field of the wrong type with the same error; `conflict_winner` adjudicates a conflict and records the loser `refuted` with source `downstream_run` |
| `chb db-read calibration [--kind K] [--scope S] [--json]` | Calibration scores per predictor (lens, label, convergence, synthesizer) and scope; a row under 10 outcomes is marked `uncalibrated (n<10)`, and every listing says the scores are correlational |
| `chb outcome-record <json>` | Append one outcome (or an array) to the ledger with source `human`: whether a finding, a lens verdict or a synthesis verdict was `confirmed`, `refuted` or `partial`. A finding outcome copies the finding's label and coordinates; a refuted finding runs the alarm cascade over what depends on it and keeps its own label |
| `chb outcome-import <path.json>` | The same from a file, with source `external` |
| `chb calibrate [--rebuild] [--scope S] [--json]` | Recompute `calibration_scores` from the whole ledger under a `calibrate` tick: the changed rows, drift (a calibrated guarantee hit rate under 0.95, or a calibrated hit rate down 0.20 since its previous revision), the lowest calibrated lens and the ∇ report per scope, which says when ∇ is not predictive. With nothing recorded since the last calibrate tick it changes nothing unless `--rebuild`; two rebuilds over the same rows write a byte-identical table. No model call; no finding label changes |
| `chb calibration-export [--out PATH]` | This workspace's calibration counts per predictor and scope, every scope's total and the provenance, as one JSON object; no weight, hit rate or calibrated flag, no tick, nothing written |
| `chb calibration-merge <files…> [--json]` | Sum such files key by key and re-run the formula over the union, deciding each scope's eligibility over the summed total; printed, stored nowhere; refuses another format, a duplicate key or counts that do not add up |
| `chb db-repair` | Back up a corrupt SQLite DB, then rebuild it with VACUUM INTO, or salvage the `signals` table row by row with `--table signals`. `--table` supports only `signals`; any other name is refused before the backup |
| `chb init` | Scaffold a new isolated project workspace |

## Ingestion

| Command | Purpose |
|---|---|
| `chb ingest` | Parse structured `<!-- FINDING: ... -->` markers from agent text; reports each marker that does not parse on stderr and exits non-zero when any marker fails to parse or write, as one with a numeric field that is not a whole number or a text field that is not a string does |
| `chb ingest-findings` | Bulk ingest JSON finding files |

## Verification & gates

| Command | Purpose |
|---|---|
| `chb check-agents` | Report agent completion status for a wave |
| `chb detect-conflicts` | Find structural conflicts between findings at the same CDE coordinates; `--json` prints the report as one object |
| `chb validate-sources` | Check the URLs findings cite and the registered sources, the set `chb guard` checks, and record each verdict. With `--wave`, only that wave's, and a URL with no wave on record takes it. Run it before `chb guard` |
| `chb verify-citations` | Resolve DOI sources via Unpaywall and cache the result; `mss_audit` then reports paywalled-only guarantees (it does not gate on them) |
| `chb guard` | One-command wave gate: checks agents, runs conflict detection, reads `validate-sources` results, runs the MSS audit, records the evaluation and opens the gate; exits non-zero while the gate stays closed. `--eval-stdin` reads the scores and gaps, derives the verdict, and records each gap the wave does not already hold open; `--json` prints one object; `--require-outcome-review` (off by default) blocks on a guarantee in a domain whose calibrated guarantee hit rate is under 0.95 until a human or external outcome resolves it |
| `chb wasp-scan-report` | Read-query shapes the planner resolved as a full table scan rather than a bounded probe, most hits first |
| `chb cde suggest-axis [--min-evidence N]` | Non-coordinate attributes that distinguish findings within one coordinate cell — candidates for the next free slot, `d<N+1>` for N registered dimensions (none once all eight are taken) |
| `chb verify-artifact <path>` | Require the file's bytes to be the canonical encoding of what they decode to, re-hash those bytes with the `sha256` value emptied, and compare against the stored sha256 |

## Autonomous mode (`chb hive`) & merge

| Command | Purpose |
|---|---|
| `chb hive init/next/complete/status/signals/reset/report/write-synthesis` | Bee-colony-inspired autonomous orchestration. `init`, `complete` and `status` take `--json`. `next --apply` runs the plan's `fix_mss` (settle), `cascade_revert` (cascade, then close the conflict), caps and parameter changes in the scan's one transaction and lists `open_actions`, `research_actions`, `gate_requested` and `gate_wave`; `next --max-iterations N` records nothing on a hive that has run N passes and prints phase `capped`. `complete --max-iterations N --json` reports `should_continue`, false at the cap or once two passes change nothing; `complete --expect-iteration N` refuses a pass whose count moved, and `next`, `complete` and `reset` refuse the database of the run whose model runs them (`HIVE_AGENT_DB`). `report` prints what a final synthesis reads, and with `--wave W` one wave's findings and sources, what the hive's gate evaluator reads; `write-synthesis --out <path>` writes one from stdin. Each verb uses the database `--db` or `HIVE_DB_PATH` names when it hosts no other project's hive, else `workspace/<project>/hive.db` beside it, which `init` creates and the others require; each names the database it used (`db` in JSON, a `Database:` line in text). A `--db` pinned to another project's hive is refused |
| `chb swarm-merge` | Merge and score swarm findings by convergence using CDE coordinates; `--json` prints the stats as one object |

## Workflow

| Command | Purpose |
|---|---|
| `chb workflow` `list`, `validate`, `init`, `next`, `complete`, `fail`, `resume`, `runs`, `status` | Graph-based workflow engine with conditional branching and retry loops |
| `chb agent-run` | Drive a workflow to completion unattended (closes the fully-automated gap). A workflow that drives the hive runs, whole, on the database the hive rule chooses for its `project` input. A `calibrate` node runs the calibration recompute in the run with no model, writing `calibration_drift_count` and `lowest_calibrated_lens` (workflow.md § The calibrate node); `workflows/research-calibrated.yaml` is the shipped example |
| `chb preflight` | Pre-launch checklist for a workflow YAML; lists its command and calibrate nodes, which run with no model |

## Forager swarm (`chb ask` and its helpers)

| Command | Purpose |
|---|---|
| `chb list` | Show the registry — sigil, name and lens; ★ marks the balanced preset |
| `chb ask "<q>"` | Ask the forager swarm a question (the balanced nine by default). Stdout holds the verdict alone: `Quorum: …`, `∇ fired: …`, `Verdict: …`; the run's narration and agent-run's JSON trailer go to stderr. `--json` prints one object and nothing else on stdout: `run_id`, `question`, `verdict`, `recommendation`, `report`, `calibration` (as the artifact records it) and `run` (the trailer's fields); not with `--no-dispatch` |
| `chb generate "<q>" --out <path>` | Emit the YAML without dispatching |
| `chb ripen` | Run the dreamer's five-pass ripening loop on the comb |
| `chb gaps` | Surface (subject coordinate × forager) cells with zero coverage |
| `chb recall "<q>"` | Find prior swarm runs on semantically similar questions |
| `chb palette --out foragers/palette.json` | Export the sigil + accent palette as JSON |
| `chb replicate` | Run N swarms from the working directory, each on its own temporary database (the only per-replica state), and compare their artifacts hash-first |
| `chb validate-personas` | Check the template-v1 personas against the spec |

## Comb

| Command | Purpose |
|---|---|
| `chb comb status` | Vantage, contested, stale and capped counts, and the last refresh |
| `chb comb refresh` | Rebuild every region digest |
| `chb comb query --d1 0 --d2 3` | Most-specific-fallback lookup; reports `calibrated_confidence` beside `confidence` once the dominant label is calibrated in one of the region's scopes |
| `chb comb at --vantage K --time T` | Belief at a moment via the Time Wheel, with the calibrated confidence under the scores current then (`--tick N` likewise) |
| `chb comb diff --vantage K --from T1 --to T2` | Confidence delta + label flips between two moments (RFC3339) |
| `chb comb history --vantage K` | Full revision arc, newest first |
| `chb comb wheel --kind swarm` | Time Wheel ticks; `--kind` takes `swarm`, `wave`, `session`, `day`, `manual`, `ripen` or `calibrate` |
| `chb comb embed [--findings] [--provider …]` | Embed every Comb vantage / finding |
| `chb comb similar --vantage K --top N` | Nearest-K semantic neighbors |
| `chb comb embed-status` | Embedding coverage across the Comb |
| `chb comb synthesize` | Render the Comb as one markdown synthesis document |

## Self-* one-command drivers

| Command | Purpose |
|---|---|
| `chb validate` | The regression gate; `--json` prints the counts, the failing checks and the log's path as one object |
| `chb agent-harness` | Drive the foragers, agent templates and the proof workflow through the local `claude` CLI (or `--provider`) and check each prompt's declared contract; report in `workspace/agent-harness/`. With `--suite fixtures/bench/suite.yaml`, run the graded twin items; with `--suite fixtures/design/suite.yaml`, the `design` case kind: HIVE designs and plans each task under `fixtures/design/`, one call does the same without the research, the plain coder executes each plan, and hidden tests grade it (`docs/specs/bench-design.md`) |
| `chb bench decide [--rule RULE] <results.jsonl>...` | Apply the pre-registered decision rule to bench results: accept, reject, inconclusive or void, and one configuration per machine class |
| `chb design report [--alpha A] <results.jsonl>...` | Print the design bench's per-task and per-arm table (hidden tests, the reference files the plan names, steps, deviations, file coverage, labels, audit) and apply its rule: `worth-it`, `not-worth-it`, `no-difference` or `too-few`, with the two arms' token-cost ratio beside the verdict; refuses a file marked `not_measured` |
| `chb review` | Self-review pipeline: five audit lenses → synthesis → extract findings → render REVIEW.md → regression |
| `chb implement` | Read findings.json, generate fix workflow, run agent-run |
| `chb extract-findings` | Parse a self-review run's lens outputs into findings.json |
| `chb render-review` | Render REVIEW.md from findings.json |
| `chb gen-implement-workflow` | Generate a fix workflow from findings.json |
| `chb run-totals <run_id>` | Node counts, tokens and cost for one workflow run; a call whose cost is unknown (a model with no price off this machine, or a gemini CLI without `--output-format json`) reads `unmetered`, and a call on this machine's server costs $0 |
| `chb models` | Inspect and edit the models config behind the cost meter, tiers and aliases |
| `chb proof` | Smallest end-to-end demo run (cost-capped at $1 by default; `--max-cost-usd`) |
| `chb gen-behavior` | Capture CLI behavior into JSONL fixtures |
| `chb replay-behavior` | Replay fixtures against any binary (byte-equal contract) |
| `chb mcp-smoke` | End-to-end MCP server smoke: calls twelve of the 20 tools against a seeded database, plus two dry-run spawn proxies |

## Graph export

| Command | Purpose |
|---|---|
| `chb export-graph` | Extract the execution graph as JSON or Mermaid |

```bash
# Export graph JSON from a project DB
chb export-graph --out graph.json

# Generate Mermaid flowchart instead
chb export-graph --mermaid --out graph.md
```

## Formal verification

| Command | Purpose |
|---|---|
| `chb lean4-extract` | Extract DB state as Lean 4 terms for formal verification |

## Multi-project support

Each project is isolated in `workspace/<name>/` with its own SQLite DB:

```bash
chb init <project-name>
export HIVE_DB_PATH=workspace/<project-name>/hive.db
```

Commands read `--db`, else `HIVE_DB_PATH`, else
`workspace/hive.db`. A hive command that names a project, and
`chb agent-run` of the hive workflow, move to `workspace/<project>/hive.db`
on their own when that database hosts another project's hive
([`docs/specs/hive.md`](specs/hive.md#requirement-one-hive-per-database)).
These commands ignore both and use their own database:

- `chb init <name>` creates `workspace/<name>/hive.db`.
- `chb replicate` gives each replica a fresh database in a temporary
  directory. `chb mcp-smoke` and `chb replay-behavior` also run against a
  fresh database in a temporary directory, removed when they exit.
- `chb proof`, `chb review`, `chb implement`, `chb validate`,
  `chb gen-behavior` and `chb agent-harness` use `hive.db` under
  their `--workspace` directory. Most of them delete it and start fresh
  on every run.

Neither these nor the commands that read files only (`chb list`,
`chb palette`, `chb preflight`, `chb validate-personas`,
`chb verify-artifact`, `chb generate`, `chb gen-implement-workflow`,
`chb render-review`, `chb calibration-merge`, `chb models`, `chb bench`,
`chb design`, `chb workflow list` and `validate`, `chb hive
write-synthesis`, help and shell completion) open the default database, so
none is created where they run.

External projects can maintain their own DBs at any path and use the
binary by setting `HIVE_DB_PATH`.

## MCP tools

`chb-mcp` serves 20 tools over stdio JSON-RPC 2.0. The contract — arguments,
results, annotations, cancellation — is [`docs/specs/mcp.md`](specs/mcp.md#requirement-mcp-tools).

| Tool | What it does | Kind |
|---|---|---|
| `chb_summary` | Workspace summary: findings, gaps, conflicts, sources | read |
| `chb_findings` | Query findings by wave, label and limit (at most 500) | read |
| `chb_status` | Autonomous-mode phase, wave, agent and finding counts; with `project`, from that project's database, named in `db` | read |
| `chb_run_totals` | Node counts, tokens and cost for one run | read |
| `chb_node_rationale` | One node's persisted final text | read |
| `chb_run_state` | A run's accumulated state, or one key of it | read |
| `chb_extract_findings` | Parse a self-review run's lens outputs into findings | read |
| `chb_preflight` | Run the launch checklist on a workflow | read |
| `chb_mss_repo_audit` | Structural MSS audit of a repository, no LLM | read |
| `chb_calibration_read` | Calibration scores per predictor and scope, with the correlational note; `kind` and `scope` filter | read |
| `chb_db_write` | Write a finding, gap or source, or close a gap or conflict, through the write path `chb db-write` takes for those kinds | write |
| `chb_set_budget_mode` | Set the cost mode later spawns inherit | write |
| `chb_outcome_record` | Record whether a finding, lens verdict or synthesis verdict held, with source `human`; the shape of `chb outcome-record` | write |
| `chb_render_review` | Render REVIEW.md from findings JSON | writes a file |
| `chb_gen_implement_workflow` | Generate a fix workflow from findings JSON | writes a file |
| `chb_research` | Start a research-deep run in the server process | spawns agents |
| `chb_agent_run` | Run a workflow YAML in a detached `chb agent-run` | spawns agents |
| `chb_swarm` | Ask the forager swarm (`chb ask`) | spawns agents |
| `chb_self_review` | Run `chb review` on this repository | spawns agents |
| `chb_self_implement` | Run `chb implement` from review findings | spawns agents |

`chb mcp-smoke` calls twelve of them against a seeded database and checks the
list itself. It does not call `chb_db_write`, `chb_set_budget_mode`,
`chb_mss_repo_audit` or the five agent-spawning tools;
`chb agent-run` and `chb implement` are exercised as dry-run CLI proxies.
