# MCP Server Specification

## Purpose

`chb-mcp` is the stdio MCP server over the store, the workflow engine and the hive: the one machine surface beside the `chb` command line. It listens on no port. Its tools read the store in process and start runs as detached `chb` processes, so anything that can reach its stdin has the operator's access to the store and a shell (§ MCP tools).

## Requirements

### Requirement: MCP transport and protocol

`chb-mcp` SHALL speak newline-delimited JSON-RPC 2.0 on stdio, writing only protocol messages to stdout and diagnostics to stderr, and negotiate MCP revisions `2025-06-18` and `2024-11-05`: echo a supported requested revision, else answer the newest. Methods are `initialize`, `ping` (empty result), `tools/list` and `tools/call`; any other is -32601. A frame not declaring `"jsonrpc": "2.0"` or naming no method is -32600. A request id must be a string or an integer and is echoed. A numeric id is an integer only as digits with an optional leading minus, so `1.5` and `1e3` are not. Any other id shape, or `"id": null`, is -32600 with `"id": null`. A message with no id is a notification and is never answered, checked first. A line that is not JSON is -32700 with `"id": null`. A line that is JSON but not a request object (a number, a string, `null`, a batch array, or members of the wrong type) is -32600 with `"id": null`. A blank line draws nothing. Each response sets exactly one of `result` and `error`. `serverInfo.name` is `chb-mcp`, the command; `serverInfo.title` is `HIVE`, the brand; `serverInfo.version` is the injected build version, `dev` locally. Each request is served on its own goroutine, so responses may arrive out of order; a panicking tool fails only its own request with -32603, and a `chb_research` run that panics is logged and marked `failed` while the server serves on. The server shuts down in order — cancel research runs, await in-flight requests, await runs, close the store — on stdin EOF, SIGINT or SIGTERM.

Once the shutdown begins, a request SHALL be answered with error -32000 (`server is shutting down`) and nothing dispatched for it, and a `chb_research` request taken before then SHALL start no run; notifications are still read. When a SIGINT or SIGTERM starts the shutdown, a second one ends the process at once, with the signal's default effect, while the drain runs. SIGPIPE SHALL NOT end the process: the server catches it, so a response written after the host has closed its end of stdout fails and is dropped, and the ordered shutdown still runs. It is caught rather than ignored, so the programs the server starts, which would inherit an ignored SIGPIPE, keep its default.

#### Scenario: The server names the command and the brand
- **WHEN** a client sends `initialize`
- **THEN** `serverInfo` carries `"name": "chb-mcp"` and `"title": "HIVE"`

#### Scenario: A notification with a bad shape
- **WHEN** a frame with no `id` names an unknown method
- **THEN** nothing is written in response

#### Scenario: A panicking tool
- **WHEN** a tool handler panics
- **THEN** that request gets -32603 and the server keeps answering

#### Scenario: JSON that is not a request
- **WHEN** a line is `42`, or a request whose `id` is `1.5`
- **THEN** the response is -32600 with `"id": null` and nothing is dispatched

#### Scenario: A request once the shutdown has begun
- **WHEN** a `tools/call` arrives after stdin EOF, SIGINT or SIGTERM started the shutdown
- **THEN** it gets error -32000 and no handler runs

#### Scenario: A host that goes away mid-request
- **WHEN** the host closes its end of stdout with a request in flight, then closes stdin
- **THEN** the response's write fails, the server runs its ordered shutdown and exits 0

#### Scenario: A second signal
- **WHEN** a second SIGINT arrives while the shutdown waits on an in-flight request
- **THEN** the process ends at once, killed by SIGINT

#### Scenario: A pipeline in a program the server starts
- **WHEN** a program `chb-mcp` starts runs `yes | head -1`
- **THEN** `yes` ends with SIGPIPE, exit status 141, as it does outside the server

### Requirement: MCP cancellation

`notifications/cancelled` SHALL cancel the named in-flight request's context, killing any subprocess it waits on, and suppress its response; a cancellation for a finished request is logged and ignored. Runs spawned detached (`chb_agent_run`, `chb_swarm`, `chb_self_review`, `chb_self_implement`) outlive the request, which returns as soon as the run id is known; they are stopped by terminating the `pid` the spawn returned, and no tool or command stops them.

#### Scenario: Cancelling a preflight
- **WHEN** a client cancels an in-flight `chb_preflight` call
- **THEN** its `chb preflight` subprocess is killed and no response is sent for it

### Requirement: MCP tools

`tools/list` SHALL return exactly 20 tools, each with a `title`, an `inputSchema` and `annotations`. Read tools declare `readOnlyHint: true`; the rest declare `destructiveHint`, `idempotentHint` and `openWorldHint` — every agent-spawning tool destructive and open-world (its agents hold write, edit and shell), the tools that overwrite a file at `out` destructive, and `chb_db_write`, `chb_set_budget_mode` and `chb_outcome_record` non-destructive. Before a handler runs, `required` properties are enforced, an `integer` refuses a fractional value, an `enum` a value outside its set, and an `array` any element that is not its `items` type; an argument the server cannot read is refused, never dropped. These schema refusals are protocol error -32602, not tool results. A tool that runs and fails returns `isError: true`, except that `chb_outcome_record` answers a failure of the store, as opposed to an outcome it refuses, with protocol error -32603; an unknown tool is protocol error -32602.

| Tool | Arguments | Result |
|---|---|---|
| `chb_summary` | — | the workspace summary (`db.GetSummary`) |
| `chb_findings` | `wave?`, `label?`, `limit?` (default 20, at most 500 — refused above) | findings, a NULL `convergence_count` read as 1 (cde-mss.md § Convergence); a row that does not read whole is `isError` |
| `chb_status` | `project?` | phase, wave, agent and finding counts, `db`; with `project`, read from the database hive.md § One hive per database chooses for it (the project's workspace database when the server's hosts another project's hive; `isError` naming the path when it is missing), else from the server's |
| `chb_run_totals` | `run_id` | node counts, tokens, cost, metered and unmetered calls, the run's constraint probes included (runner.md § Constraint probe); `cost_usd` reads `unmetered` when no call's cost is known (a model with no price off this machine, or a Gemini CLI without `--output-format json`); an unknown run is `isError` |
| `chb_node_rationale` | `run_id`, `node_name` | one node's status, rationale (≤ 64,000 bytes), tokens, cost, `unmetered` likewise |
| `chb_run_state` | `run_id`, `key?` | the run's state or one key (unbounded, by design) |
| `chb_extract_findings` | `node_prefix?` (`audit-`), `run_id?` (latest) | findings by lens, totals, parse failures (unbounded) |
| `chb_preflight` | `workflow_yaml`, `provider?`, `target_dir?` | `pass` (only when the output is a preflight report that passed), `stdout`, `exit_error?` |
| `chb_mss_repo_audit` | `repo_path?`, `max_depth?`, `include_hidden?` | a structural audit of a repository, no LLM, deterministic apart from `generated_at`: label counts, entries by surface (unmatched paths are `unknown` under `unclassified`), missing functionality, and a one-line summary of the counts (`verdict`) |
| `chb_db_write` | `kind` (`finding`\|`gap`\|`source`\|`resolve_gap`\|`resolve_conflict`), `fields` | the row id, written or closed by `db.Store.WriteRecord`, the write path `chb db-write` takes for the same five kinds (cde-mss.md § The write path); `depends_on_ids` as array or JSON string; a gap's priority `high` is stored `important`, `medium` and `low` `minor`; in every kind, a numeric field (`wave`, a coordinate, an id, `primary_source`) that is not a whole number fitting the server's `int` is `isError` and writes nothing, and so is a text field that is not a string, and a field the kind does not read, with an error that names it and lists the kind's fields |
| `chb_set_budget_mode` | `mode` (`premium`\|`standard`\|`cheap`\|`free`) | the session's budget mode, which every run a tool starts from then on uses |
| `chb_outcome_record` | `subject_kind`, `resolution`, and the optional keys of the shape `chb outcome-record` reads (cde-mss.md § Outcomes and calibration scores) | the outcome id, its source (`human`) and the findings the cascade reverted, written through the CLI's shape and refusals; an outcome refused for what it says (`calibration.ErrInvalid`: a key outside the shape, a subject it cannot identify, a confidence outside 0–100, a finding that does not exist) is `isError` and writes nothing, and any other failure, the store's, is protocol error -32603 with its message |
| `chb_calibration_read` | `kind?` (the four kinds, an enum), `scope?` | `{scores, note}`, the calibration scores (cde-mss.md § Calibration recompute) with the correlational note, read-only |
| `chb_render_review` | `findings_json`, `out`, run metadata (`run_id` an integer) | `markdown_path`, `char_count` |
| `chb_gen_implement_workflow` | `findings_json`, `out`, `severity?` (array), `max_fixes?` (default 5, 0 = no cap), `model?`, `repair_model?` | `workflow_yaml_path`, `fix_count` |
| `chb_research` | `topic`, `project?`, `workflow?` (`research-deep`; a bare file name under `workflows/`, without `.yaml` — no separators or leading dot, else `isError`; the file under the working directory's or the binary's `workflows/`, else the carried copy, workflow.md § Workflow file) | starts a run in the server process — one per project, cancelled at shutdown — and returns project, workflow, database; `chb_status` with the project follows it |
| `chb_agent_run` | `workflow_yaml`, `branch?` (explicit; empty means no auto-commit), `provider?`, `budget_mode?` (the session's mode), `max_iterations?` (default 500, agent-run's own), `max_cost_usd?`, `allow_dirty?`, `dry_run?`, `inputs?` | detached run |
| `chb_swarm` | `question`, `foragers?` (`balanced`), `provider?`, `budget_mode?` (the session's mode), `model?`, `synthesizer_model?` | detached run |
| `chb_self_review` | `provider?` | detached run; `report_path` |
| `chb_self_implement` | `findings?`, `severity?`, `max_fixes?` (default 5, 0 = no cap), `model?`, `repair_model?`, `branch?` (server-chosen `self-implement/<UTC timestamp>` when omitted), `dry_run?`, `no_pr?`, `max_cost_usd?`, `allow_dirty?`, `provider?` | detached run; `branch` |

A detached run's result SHALL carry `run_id` (from the child's log; 0 if it has not reported one within 1.5 s), `pid` and `log_path`; the log is where the run's progress is read. A child that exits before reporting a run is returned as `isError` with the tail of its log. `chb-mcp` finds `chb` beside its own real executable (symlinks resolved, `chb.exe` on Windows), else on `PATH`, and warns once when the version that binary reports (the `X` of `chb version X`) is not equal to its own. Two `dev` builds match; a `dev` build and a release do not.

The tools that start a run — `chb_research`, `chb_agent_run`, `chb_swarm`, `chb_self_review`, `chb_self_implement` — SHALL share one spawn limit: at most `HIVE_MCP_MAX_SPAWNS_PER_MIN` (default 6; 0 or less disables it) starts in any minute. A start past the limit returns `isError` and starts nothing. `chb_research`'s one run per project does not bound it, since each topic is its own project.

Every run those tools start SHALL use the session's budget mode: the mode `chb_set_budget_mode` last set, else `chb-mcp`'s `HIVE_BUDGET_MODE`, else `standard`. A `budget_mode` argument on `chb_agent_run` or `chb_swarm` overrides it for that one run. The spawned runs get the mode through their environment. `chb_research` runs in the server process and gets it through the runner's config. It refuses a mode that names none with `isError`, before it starts a run or takes a slot of the spawn limit.

This surface is not a sandbox: connecting a host to `chb-mcp` is equivalent to granting it a shell. The spawn tools run agents with arbitrary commands, and `out` is an unconfined path written with the operator's privileges.

#### Scenario: A fractional integer
- **WHEN** `chb_findings` is called with `limit: 2.5`
- **THEN** the call is refused with protocol error -32602 before the handler runs

#### Scenario: A number that is not a whole int
- **WHEN** `chb_db_write` writes a finding with `d1: 2.5`, or a gap with `wave: 1e19`
- **THEN** the call is `isError` and no row is written

#### Scenario: A text field that is not a string
- **WHEN** `chb_db_write` writes a finding with `agent: 5`
- **THEN** the call is `isError`, naming `agent`, and no row is written

#### Scenario: The session's budget mode reaches a research run
- **WHEN** `chb_set_budget_mode` sets `cheap` and `chb_research` runs a workflow whose node names a tier
- **THEN** that node calls the tier's cheap model

#### Scenario: A budget mode that names no mode
- **WHEN** no session mode is set, `HIVE_BUDGET_MODE` is `premuim`, and `chb_research` is called
- **THEN** the call is `isError` naming `premuim`, no run starts, and the spawn limit is untouched

#### Scenario: An array element of the wrong type
- **WHEN** `chb_gen_implement_workflow` is called with `severity: [1]`
- **THEN** the call is refused with protocol error -32602, not run with the default filter

#### Scenario: No cap on fixes
- **WHEN** `chb_gen_implement_workflow` is called with `max_fixes: 0` on eight matching findings
- **THEN** `fix_count` is 8

#### Scenario: A spawn whose child dies at once
- **WHEN** `chb_agent_run` names a workflow file that does not exist
- **THEN** the result is `isError` carrying the child's log tail, not a run id of 0

#### Scenario: A refused outcome and a store that fails
- **WHEN** `chb_outcome_record` is called with a key outside the shape, and then with a valid outcome the store cannot write
- **THEN** the first is `isError` naming the key, and the second is protocol error -32603 naming the store's failure

### Requirement: Review and implement pipelines

`chb review` (spawned by `chb_self_review`) SHALL run: prepare the workspace, preflight `workflows/self-review.yaml`, run it with `agent-run` in the target repository with no auto-commit branch, extract the findings of the run that agent-run reports (its `workflow run ID:` line) to `findings.json`, render `REVIEW.md` (the synthesis node writes `SYNTHESIS.md`, so rendering never overwrites it), optionally pause at a human gate, then run `go test ./...` and `chb validate` in the target. `chb implement` (spawned by `chb_self_implement`) SHALL: generate a fix workflow from `findings.json` filtered by severity and `max-fixes`, validate and preflight it, stop there under `--dry-run`, optionally pause at a human gate, run it with `agent-run` on its branch under the cost cap — opening a pull request unless `--no-pr` — and report run totals, keeping partial progress on the branch when the run fails. In both, a SIGINT or SIGTERM that arrives while a stage's child runs SHALL reach that child as SIGTERM (killed after 10 s if it is still running), and the stage then fails. Terminating the `pid` of `chb review` or `chb implement` therefore stops its `agent-run` too. After a signal stops its `agent-run`, `chb implement` still reports run totals, then SHALL exit non-zero. The fix workflow's `plan` node runs as the `analyst` persona, because `coder-fix` requires every response to end with the `compile_ok`/`tests_pass` object, which the plan node does not return. Each fix node SHALL name `tier: worker` and its repair `tier: synthesist`, unless `--model` or `--repair-model` (`model` or `repair_model` on the two MCP tools) names a model, which that node or repair then uses. Its final gate is three command nodes in a chain — `go test ./...`, `chb validate`, `chb replay-behavior` — whose exit codes the runner records (runner.md § Command nodes); the first non-zero exit fails the run, and the gates after it do not run.

#### Scenario: Implement dry run
- **WHEN** `chb implement --dry-run` runs
- **THEN** the workflow is generated, validated and preflighted, and no agent runs and no branch is created

#### Scenario: A second review into one workspace
- **WHEN** `chb review` runs twice with the same `--workspace`
- **THEN** the second `findings.json` and `REVIEW.md` hold the second run's findings, not the first's

#### Scenario: Stopping a self-review
- **WHEN** the `pid` that `chb_self_review` returned receives SIGTERM while `agent-run` runs
- **THEN** `agent-run` receives SIGTERM and exits, and no later stage runs

#### Scenario: Stopping a self-implement
- **WHEN** the `pid` that `chb_self_implement` returned receives SIGTERM while `agent-run` runs
- **THEN** `agent-run` receives SIGTERM and exits, the run totals are reported, and `chb implement` exits non-zero without printing `IMPLEMENT COMPLETE`

## Files

- `internal/mcp/main.go`, `protocol.go`, `tools.go`, `tools_db.go`, `tools_research.go`, `tools_status.go`, `tools_run.go`, `tools_review.go`, `tools_repo_audit.go`, `tools_calibration.go`, `spawn.go`, `ratelimit.go`, `schema_validate.go`, `cancel.go` — `chb-mcp`
- `internal/cli/review.go`, `review_pipeline.go`, `implement_pipeline.go` — the pipelines
- `internal/review/` — findings extraction, `REVIEW.md` rendering, the implement-workflow generator and its template
- `internal/cli/mcp_smoke.go` — `chb mcp-smoke`, the stdio smoke over the tools
