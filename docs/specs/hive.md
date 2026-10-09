# Hive Autonomous Orchestration Specification

## Purpose

Bee-colony-inspired orchestration. Signals — computed from the project's state or written by agents and the dreamer — drive an ordered dispatch plan and the hive's gain-control parameters, so a research project can direct itself without a human coordinator.

## Requirements

### Requirement: Signal vocabulary

The `signals.signal_type` CHECK SHALL admit exactly nine types, and every writer SHALL use one of them.

Seven of them borrow a honeybee behaviour. The table gives each one's bee reading and what it means here. The `qmp` note below says where HIVE departs from the bee.

| Type | The bee behaviour | Meaning here | Produced by |
|---|---|---|---|
| `waggle_dance` | a forager's report of a rich patch, which recruits others to it | a rich patch (findings at `convergence_level` high) recruits a scout to an open gap with the same `d1` | `hive.EvaluateSignals` |
| `stop_signal` | the head-butt that stops recruitment to a contested, dangerous or crowded site | dispatch no scout to these coordinates | `hive.EvaluateSignals` (contested coordinates), the dreamer's `prune` pass (near-duplicate assumptions), agents |
| `alarm` | danger to the colony | a guarantee's foundation is suspect | `hive.EvaluateSignals` (a conflict with a named winner), `CascadeRevert`, the dreamer's `reprove` and `settle` passes |
| `tremble_dance` | foragers cannot unload: receivers are the bottleneck, so the dance recruits receivers and damps recruitment | more conflicts wait for a verifier than the batch dispatches | `hive.EvaluateSignals` |
| `shaking_signal` | the vibration signal that rouses idle workers | the colony is under-working: gaps it can dispatch outnumber the batch, or unknowns dominate the latest wave, so its workers are not finding answers. The first raises `batch_size`; the second drives the tier rule, which raises `model_tier` one rung at most once per two scans, and lowers it again after three scans in a row on which unknowns are at most a quarter of the latest wave (§ Gain control) | `hive.EvaluateSignals` |
| `quorum` | enough independent scouts at one site | a finding's convergence count reached `convergence_threshold`: the hive caps it once, and it stays an assumption | `hive.EvaluateSignals` |
| `qmp` | queen mandibular pheromone, which keeps the colony cohesive | the non-overridable MSS-integrity halt | `hive.EvaluateSignals`, the dreamer's QMP gate (`dreamer.EmitQMP`) |
| `nabla` | — | ∇ convergence between two `resonates`-bonded foragers | the ∇ convergence sensor (`comb.QuorumSensor`) |
| `chronomantic_drift` | — | belief drift across Time Wheel ticks | agents only; no Go code writes it |

`qmp` borrows the pheromone's authority, not its timing. A queen emits QMP all the time, and its absence is the trouble sign. HIVE's `qmp` fires only when the MSS audit fails.

`hive.EvaluateSignals` SHALL raise signals by these rules, and no others:
- `qmp` when the MSS audit fails.
- `alarm` for each unresolved conflict whose winner is named, carrying the losing finding.
- `stop_signal` at each coordinate an unresolved conflict's findings sit at, while the conflict rate (unresolved conflicts ÷ findings) exceeds `conflict_rate_threshold`. There is one signal per coordinate, sourced from the oldest conflict there.
- `waggle_dance` for each group of findings at `convergence_level` high, targeting the first open gap (in coordinate order) that shares its `d1`, that no other dance has claimed, and that sits at no capped finding's coordinate.
- `tremble_dance` when more unresolved conflicts with no named winner wait for a verifier than `batch_size`.
- `shaking_signal` when the gaps the gap-fill step can dispatch outnumber `batch_size`. Those gaps are the open critical ones, or the open important ones when no critical gap is open, less any at a coordinate a `stop_signal` covers. A stop the scan raises and a pending one anyone wrote both count.
- a second `shaking_signal` when `unknown` findings make up more than half of the latest wave, and that wave holds at least five findings. The latest wave is the highest `wave` any finding carries. No other label mix raises it. Honest research is mostly assumptions, since a sourced fact the agent did not verify is one. The half and the five are choices.
- `quorum` for each `assumption` at `convergence_level` high whose convergence count is at least `convergence_threshold`, which no unresolved conflict names, and which is not capped.

Signals `hive.EvaluateSignals` computes are written by `chb hive next`. Agents write signals with `chb db-write signal`. `chb hive signals --type X --pending` reads them, the 50 most recent, newest first, and of signals written in one second the higher id first; `--pending` reads only `acted_on=0` rows.

#### Scenario: A type outside the vocabulary is refused
- **WHEN** any writer inserts a signal whose type is not one of the nine
- **THEN** the insert fails the CHECK constraint and no row is written

#### Scenario: A contested coordinate is stopped
- **WHEN** 2 unresolved conflicts touch coordinates (2,0,0,0), (2,1,0,0) and (2,2,0,0) among 10 findings, `conflict_rate_threshold` is 0.15, and critical gaps sit at (2,1,0,0) and (7,0,0,0)
- **THEN** the scan raises 3 `stop_signal`s, one per contested coordinate, and the plan dispatches a scout to (7,0,0,0) and none to (2,1,0,0)

#### Scenario: The dreamer's reprove pass raises the alarm
- **WHEN** a dependency of a guarantee is edited after the guarantee was written, and `chb ripen` runs
- **THEN** the `reprove` pass writes an `alarm` for the guarantee, and no `tremble_dance`

### Requirement: Plan generation

`chb hive next` SHALL compute the iteration's signals from the current state (`hive.EvaluateSignals`) and run them through the ordered plan steps in `internal/hive/plan_steps.go`: QMP → alarm cascade → conflict resolve → gap fill → tremble → shaking → quorum cap → gate check. Evaluated signals are level-triggered: they are recomputed every iteration and fire again for as long as their condition holds. The `stop_signal`s the scan evaluates, and pending `stop_signal` rows anyone wrote, are coordinates to suppress. Of the signals agents write, only pending `stop_signal` rows affect the plan; an agent-written `waggle_dance` does not by itself add an action. Suppression compares coordinate values: a stop signal matches a dispatch when all four target axes are equal, and an absent (NULL) axis matches only an absent axis. Signal-driven research is dispatched to the `hive-scout` persona, conflict resolution to `verifier`. A conflict whose winner is already named is not dispatched to `verifier`; the `cascade_revert` its alarm raises is its only action. A plan with no actions is the empty list `[]`. The gap-fill step dispatches open critical gaps. When no critical gap is open, it dispatches open important gaps instead. It never dispatches a minor gap. Gap fills and waggle dances share one budget of `batch_size` dispatches. Conflict resolution has its own budget of `batch_size` dispatches, which is 20 while a `tremble_dance` fires. A gap-fill `dispatch_agent` action carries the gap's id in `payload.gap_id`. A conflict-resolution `dispatch_agent` action, and the `cascade_revert` an alarm raises, carry the conflict's id in `payload.conflict_id`. Dispatches go to the latest wave plus one. The plan SHALL refuse a latest wave that cannot be incremented, rather than wrap it to a negative wave: `chb hive next` exits with an error before it records signals or advances the iteration.

#### Scenario: A stop signal suppresses dispatch
- **WHEN** a pending `stop_signal` targets coordinates the plan would dispatch to, whether for a gap or for a waggle dance
- **THEN** the plan emits no dispatch action for those coordinates

#### Scenario: A wave that cannot be incremented
- **WHEN** the latest finding's wave is 2^63 − 1, the largest integer SQLite stores
- **THEN** `chb hive next` exits with an error, records no signal, and leaves the iteration unchanged

#### Scenario: An unresolved condition fires again
- **WHEN** the state that fired a `qmp` or `alarm` is unchanged at the next iteration
- **THEN** the next plan fires that signal again

### Requirement: A dispatched gap is filled and closed

The `hive` workflow's `dispatch` node reads the plan's research actions as JSON through `{hive_plan}`, which the `scan` node sets from `research_actions`, so each action's `payload.gap_id` reaches it intact. It SHALL carry out each gap-fill and waggle-dance `dispatch_agent` action itself, under the `hive-scout` protocol: write each result as a finding at the action's wave and coordinates, register its sources, and close a filled gap with `chb db-write resolve_gap`, naming the finding that answers it. A gap stays open until then. Only `GapsRepo.ResolveGap` closes a gap. `chb db-write resolve_gap` calls it, and so does the `resolve_gap` kind of `chb_db_write`, in the runner's in-process registry and over MCP alike, with the same refusals. Termination waits on critical and important gaps, and the plan dispatches both, so each gap that blocks termination can be filled and closed.

#### Scenario: A seeded gap is resolved
- **WHEN** the only critical gap is resolved by a finding
- **THEN** the next plan holds no gap-fill action for it, and `CheckTermination` no longer names critical gaps

#### Scenario: An important gap is dispatched once no critical gap is open
- **WHEN** an important gap is open and the only critical gap has been resolved
- **THEN** the next plan holds a gap-fill action carrying the important gap's `gap_id`, and once that gap is resolved `CheckTermination` no longer names important gaps

### Requirement: A dispatched conflict is adjudicated and closed

The `hive` workflow's `dispatch` node SHALL adjudicate each conflict-resolution action under the `verifier` protocol. When the evidence settles it, it names the surviving finding with `chb db-write conflict_winner`, reverts what rests on the other finding with `chb db-write cascade_revert`, and closes the conflict with `chb db-write resolve_conflict`. A `cascade_revert` action is not the dispatch's: `chb hive next --apply` runs the cascade and closes the conflict in `payload.conflict_id` in the scan's transaction (§ The plan is advisory unless applied). `resolve_conflict` (`ConflictsRepo.Resolve`) records the resolution and the resolving wave. It refuses an unknown conflict, a conflict already resolved, and an empty resolution. The `resolve_conflict` kind of `chb_db_write`, in the runner and over MCP, and `--apply`'s `cascade_revert` close a conflict the same way, with the same refusals. Only those and the gate's numeric auto-resolve close a conflict. An open conflict with no winner is dispatched to `verifier` again every iteration. Once a winner is named, the conflict is no longer dispatched, and it raises the alarm again every iteration until it is closed; a scan with `--apply` closes it in the pass the alarm fires.

#### Scenario: A conflict with a named winner is not adjudicated again
- **WHEN** a conflict's winner is named and the conflict is still open
- **THEN** the plan holds the alarm's `cascade_revert` of the other finding for it, and no `verifier` dispatch

#### Scenario: An adjudicated conflict is resolved
- **WHEN** a conflict's winner is named, the other finding is cascaded, and the conflict is resolved
- **THEN** the next plan holds no action for that conflict, and `CheckTermination` no longer names unresolved conflicts

### Requirement: The hive ripens an existing corpus

Seven of the eight plan steps are reactive: each iterates a collection — pending signals, unresolved conflicts, critical and important gaps, findings — that is empty until research exists, and every `dispatch_agent` action is emitted inside one of those loops. `stepGateCheck` alone emits without iterating a collection. It emits `run_gate` whenever no critical action has been emitted, no critical gap or unresolved conflict is present, and the MSS audit passes, as in an empty database. `chb guard` refuses that gate for a wave with no agents or findings. So against a workspace holding no findings, gaps, conflicts or signals the plan holds exactly one action, the gate declines it, and the hive iterates to its cap without dispatching anything. The `hive` workflow takes `project` and `max_iterations` and no research question, so it has no topic to seed itself with either.

The hive SHALL therefore be run against a seeded workspace — one where `chb db-write gap`, `chb ingest-findings`, `chb ingest`, a `chb ask --lens-tools all` run whose lenses wrote findings, or an earlier wave has already put findings or gaps in the database; a default `chb ask` writes its verdict and ∇ signals and no findings or gaps, so it seeds nothing. This is a property of the design, not a bug to route around: the hive directs research it can see, and sees nothing in an empty database.

#### Scenario: A cold workspace
- **WHEN** `chb hive next` runs against a database with no findings, gaps, conflicts or signals
- **THEN** the plan carries only `run_gate`, and the gate blocks for want of findings

### Requirement: The plan is advisory unless applied

The plan SHALL change nothing by being generated, except as follows. `chb hive next` advances the iteration, records the signals it fires (already acted on) and the signal watermark. It also records the health metrics in `hive_state`: `total_findings`, `unresolved_gaps`, `unresolved_conflicts`, `mss_integrity` and `label_skew`. A terminal scan also sets `phase` and `terminal_reason`. A terminal scan does not advance the iteration. No signal rule reads these recorded metrics, so a scan raises the same signals from the same state whether or not an earlier scan recorded them. A scan that is not terminal also records the research state as the pass begins, for the stall rule (§ The hive loop's stopping rule).

With `--apply`, `chb hive next` SHALL also execute the plan's deterministic actions, in plan order: `fix_mss` runs the dreamer's `settle` pass with apply once (`dreamer.SettleInTx`), which demotes each guarantee whose dependencies reach an unknown to an assumption and raises its alarm; `cascade_revert` reverts what rests on the conflict's contradicted finding (`mss.RunCascade`) and closes the conflict its payload names, at the action's wave, with the resolution `loser <finding_id> reverted`; `cap_finding` caps the converged finding a `quorum` signal names; and `adjust_params` applies the parameter changes below. The binaries apply both through `hive.RecordScan` with `Scan.Apply` set, which calls the unexported `applyCaps` and `applyParamAdjustments` in the scan's transaction; `hive.ApplyCaps` makes the same writes as `applyCaps` on the write pool, a test seam for tests outside the package. It also records the tier rule's decision in `hive_tier_log` (§ Gain control). `mss_fixed` lists the guarantees `fix_mss` demoted and `cascades` each cascade (`finding_id`, `reverted`, `conflict_id`). `fix_mss` repairs laundering only: an untraceable guarantee, a dependency cycle or a partition violation stays, and `qmp` fires again at the next scan. Every other action is for the agent executing the plan, and `open_actions` in the output lists them: the plan less those four kinds. `research_actions` are its `dispatch_agent` actions without their `model` field, which nothing reads, and `gate_requested` and `gate_wave` say whether it holds a `run_gate` and for which wave. In a terminal or capped scan's output the lists are `[]`, `gate_requested` is false and `gate_wave` is 0.

`chb hive next` SHALL record everything it writes in one write transaction (`hive.RecordScan`), everything `--apply` writes included: a write that fails rolls back the others, so a scan that errors changes nothing and counts no iteration, and running it again counts the pass once. The plan is generated before the transaction begins, so a plan it refuses leaves nothing behind either.

With `--max-iterations N`, a scan that is not terminal and finds `hive_state.iteration` at `N` or above SHALL record nothing and print `phase` `capped`, with `reason`, `iteration`, `max_iterations`, and empty `actions` and `signals_fired`. The count is read inside the transaction, so two scans cannot both pass the cap. A terminal scan is recorded as terminal whatever the count. `--max-iterations` below 1 is refused before anything changes.

Capping SHALL record the finding in `capped_findings`, once, and change nothing else: the finding keeps its label, an assumption, and its `depends_on_ids`. Convergence SHALL NOT make a guarantee. Agreement among agents is not a derivation, and a guarantee follows only from distinct premises named in its `depends_on_ids` (cde-mss.md). The cap is durable. The `quorum` rule leaves a capped finding out, so the level-triggered signal fires for it once and no later plan acts on it. A capped cell is sealed from recruitment: no `waggle_dance` targets a gap at a capped finding's coordinate, though a dance still recruits to the other gaps that share its `d1`. The seal stops short of gap fill. A critical or important gap at a capped coordinate is still dispatched, because only a finding closes a gap and termination waits for it. `caps` in the output reports each cap, and is `[]` when there is nothing to cap; a finding already capped is reported and not capped again. `capped_findings` is a table of its own. The Comb shows each cap when it renders and changes no digest for it (comb.md § Capped cells).

#### Scenario: A converged finding is capped once
- **WHEN** `--apply` caps a converged assumption, and the next scan finds its convergence unchanged
- **THEN** it is still an assumption with the same `depends_on_ids`, `capped_findings` holds one row for it, and the next plan raises no `quorum` for it

#### Scenario: A scan whose write fails records nothing
- **WHEN** one of the signals a scan fires cannot be written
- **THEN** `chb hive next` exits with an error, and the metrics, the other signals, the iteration and the watermark are as they were before the scan

#### Scenario: An --apply write that fails records nothing
- **WHEN** `chb hive next --apply` settles a laundering guarantee, cascades from a conflict's loser and closes the conflict, caps a finding and raises the batch size, and then one of those writes, or the iteration bump after them, fails
- **THEN** it exits with an error, and the labels, the gaps, the conflict, the cap, the batch size, the signals, the pass's start and the iteration are as they were before the scan

#### Scenario: --apply runs the repairs
- **WHEN** `chb hive next --apply` plans `fix_mss` for a guarantee resting on an unknown, and `cascade_revert` for a conflict whose winner is named
- **THEN** the guarantee is an assumption with no dependencies, what rests on the loser is `unknown` with a critical gap for each, the conflict is resolved at the action's wave, and neither action is in `open_actions`

#### Scenario: A hive at its cap is not scanned
- **WHEN** `chb hive next --max-iterations 2` runs on a hive that is not terminal and has run 2 passes
- **THEN** it prints `phase` `capped` with `iteration` 2, and records no signal, metric or pass

#### Scenario: A capped cell recruits no scout to itself
- **WHEN** a finding at `convergence_level` high is capped, and open minor gaps sit at its coordinate and at another coordinate with the same `d1`
- **THEN** the scan's `waggle_dance` targets the other gap, not the one at the capped coordinate

#### Scenario: The hive terminates after a cap
- **WHEN** the hive runs with `--apply` over a converged assumption, an open important gap at its coordinate, and an open critical gap elsewhere
- **THEN** the finding is capped in the first iteration and no later plan names it, no later `waggle_dance` recruits to its coordinate, gap fill dispatches the important gap there once and it is closed, and the hive reaches terminal once a gate opens

#### Scenario: Without --apply nothing is capped or adjusted
- **WHEN** `chb hive next` runs without `--apply`
- **THEN** no cap is recorded and no gain-control parameter changes

### Requirement: Calibration scores capped findings

Convergence SHALL change no label. Quorum caps a converged assumption, and it stays an assumption (§ The plan is advisory unless applied). Calibration (cde-mss.md § Calibration never writes labels) SHALL NOT promote or demote. It scores a capped finding like any other, under the `subject_label` recorded with each outcome. It takes no confirmation from convergence, so a capped finding is never graded against the converging siblings that capped it. A refutation leaves the cap in place: nothing removes a `capped_findings` row. The signal vocabulary, the cap and gain control are unchanged.

#### Scenario: A capped finding is refuted
- **WHEN** `--apply` caps assumption P, a guarantee Q depends on P, and a human later records P `refuted`
- **THEN** Q reverts to `unknown` through the cascade, P stays a capped assumption, and P's outcome counts toward `label`/`assumption` in its scope

### Requirement: The hive workflow runs its relays without a model

`workflows/hive.yaml` SHALL run its mechanical steps as command nodes (runner.md § Command nodes): `init` (`chb hive init --json`, whose `db` output names the run's database, § One hive per database), `scan` (`chb hive next --apply --max-iterations <max_iterations>`), the gate's `validate-sources` (`chb validate-sources --wave <gate_wave>`), `gate-state` (`chb hive report --wave <gate_wave>`) and `gate` (`chb guard --wave <gate_wave> --eval-stdin --json`), `merge` (`chb swarm-merge --json`), `detect-conflicts` (`chb detect-conflicts --json`), `complete-iteration` (`chb hive complete --max-iterations <max_iterations> --expect-iteration <the scan's iteration> --json`), and after the loop `report` (`chb hive report`), `write-synthesis` (`chb hive write-synthesis`) and `export-graph`. Only `dispatch`, `evaluate` and `final-synthesis` call a model. The scan's `--apply` runs the plan's mechanical actions itself (§ The plan is advisory unless applied), so no model relays a cascade, a settle pass, a cap or a parameter change.

`scan` feeds the decisions `phase`, `research_actions`, `gate_requested` and `gate_wave`. A `terminal` or `capped` phase goes to `report`. A plan with no research action skips `dispatch`, and one with no `run_gate` skips the gate's four nodes. When the gate runs, chb validates the wave's sources, `gate-state` gathers the wave's findings, their sources with each one's validation and the open gaps, `evaluate` scores the wave and names what is missing, and `gate` pipes that to `chb guard --eval-stdin`, which derives the verdict (§ A derived evaluation verdict), records the evaluator's gaps and runs the gate. So no model states the verdict that opens a gate and lets the hive reach `terminal`. A shut gate exits 1, and `gate` lists 1 in `ok_exit`, so a shut gate is an answer the pass records, not a failure. The loop returns to `scan` while `complete-iteration` reports `should_continue` true, and goes to `report` otherwise.

`max_iterations` SHALL mean one thing: the cap on the hive's pass count, `hive_state.iteration`, which persists across runs until `chb hive reset`. That meaning is a definition. A run on a hive below the cap runs passes until the count reaches the cap, the hive is terminal, or it stalls. A run on a hive at the cap runs no pass: its scan reports `capped` and the run goes straight to the report and the synthesis. The workflow's own `scan` never takes the count past the cap. That is a guarantee, resting on the scan's cap check running in the scan's transaction. That the count stays at or below the cap also rests on nothing else running `chb hive next` during the run. `dispatch` has `shell` with the running chb first on `PATH`, and `chb hive next` without `--max-iterations` counts a pass whatever the cap, so `chb hive next`, `complete` and `reset` SHALL refuse, writing nothing, the database `HIVE_AGENT_DB` names: the database of the run whose model drives the process (runner.md § Backends). For the run's own model nodes that is a guarantee, resting on every process a model drives carrying the variable; a database the model's run does not use, such as a replay's scratch one, is not refused. For any other process it is an assumption: such a scan is detected, not prevented (below). `init` records the count the run began at as `start_iteration`, and `report` prints `passes_this_run` beside the total, so a run that did fewer passes than `max_iterations` says so.

Each pass runs `hive next` once, and `complete-iteration` refuses, writing nothing, a pass whose count moved after its scan. What that guarantees, resting on `--expect-iteration` and not on what the dispatch agent does: every pass the workflow completes was counted exactly once, and a scan run by anything else during a pass fails the run at that pass. It does not undo that scan, whose count stays raised, past the cap if it ran on the last pass.

#### Scenario: The dispatch model runs chb hive next
- **WHEN** a pass's `dispatch` model runs `chb hive next --project P` in its `shell`
- **THEN** the command refuses, naming `HIVE_AGENT_DB`, the count stays where the pass's scan left it, and the pass completes

The three model nodes each name `tier: planner`, not a model, so the budget mode and the models config choose the model (runner.md § Model tiers and budget mode). With the shipped models config at the standard budget mode that is `claude-sonnet-4-6`, and `claude-opus-4-8` at premium; on the OpenAI and Gemini providers, each one's own model for `sonnet` or `opus`. Each also names its role, `hive-research` on `dispatch`, `evaluate` on `evaluate` and `hive-synthesis` on `final-synthesis`, so under a routing profile the profile chooses its model, provider and reasoning instead (runner.md § Routing profiles). Each has an `output_schema` with `additionalProperties: false` and one `on_reject` repair, so a reply that is not the object its schema describes, prose included, is rejected and repaired once, then fails the run (workflow.md § Output schema).

`dispatch` reads `research_actions`, the plan's `dispatch_agent` actions, through `{hive_plan}`, and returns `dispatch_results` (a non-empty string), and `findings_written` and `gaps_resolved` (whole numbers, 0 or more). Those counts are its report; `complete-iteration` prints what the pass wrote, read from the database (§ The verbs a command node reads print JSON). `dispatch` keeps every tool, since it writes through `chb`, and declares `min_tool_calls: 1`: a call that made no tool call is sent back to its repair, whatever it reports (runner.md § Minimum tool calls). Its prompt puts the tools first: run each chb command with the shell tool, the report comes after the commands have run, and a report with no tool call is rejected and sent back. It has no `max_retries`, since a retry after a call that did write would write again. What the minimum does not catch is a call that runs a tool and then reports work it did not do. Its one repair keeps the tools and is sent the reason, the rejected answer and the task again, with the plan. The reason decides what the repair does. After a call that made no tool call, the actions were not taken, so it carries them out now, with its tools, then reports; a repair that makes no tool call either is rejected, and so the run fails. After a malformed answer, the actions are done as far as that answer took them, so it runs no command and reports what the answer says was done: the plan again would repeat its writes. That the repair obeys the reason is an assumption about the model; the minimum holds either way, since the dispatch's calls count for its repair.

`evaluate` has `tools: []` and returns `gaps` (at most 3 non-empty strings, listed first so the model names what is missing before it scores) and `coverage`, `depth`, `sources` and `actionability` (whole numbers from 1 to 5), and no verdict. `final-synthesis` has `tools: []`: `report` gives it the state, and it returns `report_markdown` (a non-empty string) and `open_questions` (at most 10 non-empty strings); its prompt keeps the open questions out of the Markdown, since `write-synthesis` adds them under their own heading. `write-synthesis` writes both to `workspace/<project>/final-synthesis.md`, so the file exists exactly when a synthesis that held its schema arrived.

#### Scenario: An offline run to the cap
- **WHEN** the workflow runs with `max_iterations` 2 against a database holding one unresolved critical gap, with the model nodes answered well-formed by a fake local endpoint that serves one model and refuses others, the planner tier mapped to that model, and each dispatch reading a project note with one `read_file` call
- **THEN** `hive_state.iteration` is 2 and equals the scans that counted, every command node that ran shows 0 tokens and an exit code of 0, no gate node runs, `dispatch` runs twice with the gap's id in its plan and tools offered, `final-synthesis` once with its schema sent and no tools, and `final-synthesis.md` holds its report and open questions

#### Scenario: A run on a capped hive
- **WHEN** the same workflow runs again on that database with `max_iterations` 2
- **THEN** the scan reports `capped`, `dispatch` is skipped, the count stays 2, and the synthesis prompt says 0 passes ran in this run

#### Scenario: A dispatch that runs no tool
- **WHEN** a model answers the dispatch, and then its repair, with a well-formed report and no tool call
- **THEN** the repair is sent the shortfall, the report and the plan with the gap's id, with tools offered; `dispatch` is `rejected` after those two calls, `complete-iteration` never runs, the report reaches no state, and no synthesis file is written

#### Scenario: A dispatch that does the work on its repair
- **WHEN** a model answers the dispatch with a well-formed report and no tool call, and answers the repair after one `read_file` call
- **THEN** `dispatch` completes, its `tool_invocations` rows hold the repairs' reads, each pass's repair row passed, every pass completes, and the synthesis file is written

#### Scenario: A dispatch that answers in prose
- **WHEN** a model answers every dispatch call "chb: command not found" in prose, after one `read_file` call
- **THEN** the answer, the finalize call and the one repair, which is sent the prose and the task again and told to run no command, are rejected, `dispatch` is `rejected`, `complete-iteration` never runs, the prose reaches no state, and no synthesis file is written

#### Scenario: A synthesis in prose
- **WHEN** a model answers the synthesis and its repair in prose
- **THEN** `final-synthesis` is `rejected`, `write-synthesis` never runs, and no synthesis file is written

#### Scenario: A gate that stays shut
- **WHEN** the workflow runs with `max_iterations` 5 on a database with one finding at wave 1 and no open gap, and the evaluator names the same gap, with every score 5, each time it is called
- **THEN** each pass runs the gate; `guard --eval-stdin` records `NEEDS_MORE_WORK` and the gap once, and exits 1 with the gate shut; the passes after the first research that gap and change nothing; and the run stops after pass 3, its `stop_reason` the stall

### Requirement: The hive loop's stopping rule

`chb hive complete --max-iterations N --json` SHALL print `should_continue`, computed rather than judged: true while `hive_state.iteration` is below `N` and the hive has not stalled. The hive has stalled when each of its last two passes, up to the current one, ended with the research state as its scan found it. That rule is a definition. The research state is the fingerprint `hive.Progress`: the count of findings, their highest id, their count per label and their latest edit; the count of gaps and of open gaps; the count of conflicts, of open ones and of ones with a winner; the count of sources and of wave gates; and the count of evaluations whose verdict differs from the previous evaluation of the same wave, the first of each wave included. Signals, caps and `hive_state` are left out, since every scan writes them. An evaluation that repeats its wave's verdict is left out too: a pass that gates a blocked wave again records the same verdict, and that is not progress. The scan records the fingerprint as its pass begins and `chb hive complete` as the pass ends, both in `hive_iterations`, a table of its own. A pass with no recorded end, such as one whose run failed before `complete-iteration`, is not a stalled pass. `stop_reason` names what stopped the loop and is empty when it goes on. The rule does not read termination: a terminal state is left to the next scan, the one place the terminal phase is recorded. `chb hive reset` clears the project's pass history with its count.

`chb hive complete` SHALL write in one transaction (`hive.CompleteIteration`): the signals it consumes, the gain-control params, the phase and the pass's end. With `--expect-iteration N` it first refuses, writing nothing, a hive whose `hive_state.iteration` is not `N`.

So a hive whose passes write nothing stops after two passes, not at its cap, and so does one whose passes only gate a wave that stays blocked with the same verdict. A pass that only adds findings that answer nothing still changes the fingerprint, so such a hive runs to its cap.

#### Scenario: Two passes that change nothing
- **WHEN** a pass adds a finding, the next changes nothing, a third is never completed, and the fourth and fifth change nothing, with `--max-iterations` 10
- **THEN** `should_continue` is true after the first four and false after the fifth, whose `stop_reason` says the hive stalled

#### Scenario: A gate that stays blocked
- **WHEN** each pass records an evaluation for wave 1, verdicts `NEEDS_MORE_WORK`, `NEEDS_MORE_WORK`, `COMPLETE`, then for wave 2 `COMPLETE`, `COMPLETE`, then for wave 1 `COMPLETE`, and nothing else changes, with `--max-iterations` 10
- **THEN** `should_continue` is true after the first five passes and false after the sixth

#### Scenario: The loop rule at the cap
- **WHEN** `chb hive complete --max-iterations 2 --json` runs after the second scan
- **THEN** it prints `should_continue` false, `iteration` 2, and a `stop_reason` naming the cap

### Requirement: The verbs a command node reads print JSON

With `--json`, each verb below SHALL print exactly one JSON object on stdout and nothing else there. Without the flag each prints its text report.

| Verb | `--json` object |
|---|---|
| `chb hive init` | `project`, `created`, `phase`, `iteration`, `db` |
| `chb hive complete` | `project`, `action`, `phase`, `iteration`, `signals_consumed`, `db`; for a pass a scan recorded, what the pass wrote, from the database: `findings_added`, `gaps_resolved`, and `changed` (its end differs from its start); with `--max-iterations N`, also `max_iterations`, `should_continue` and `stop_reason` |
| `chb hive status` | the status report's figures, `db`, `is_terminal`, `reason`, and the raw scanned state under `state` |
| `chb swarm-merge` | `total_findings`, `coordinate_groups`, `near_duplicates`, `convergence_updates` (findings per level), `conflicts_detected` |
| `chb detect-conflicts` | `dry_run`, `total`, `new`, `numeric`, `mss_label`, `negation`, `details` |
| `chb guard` | `wave`, `opened`, `errors`, `warnings`, and `verdict` when an evaluation was given |
| `chb validate` | `passed`, `failed`, `warnings`, `failures` (the failing checks' names), `log` and `db` |

`chb hive next` prints one JSON object with or without flags, `db` among its keys; `--apply` adds `mss_fixed`, `cascades`, `caps`, `params_applied`, `open_actions`, `research_actions`, `gate_requested` and `gate_wave`. `chb hive report` prints one JSON object always: `project`, `db`, `phase`, `iteration`, `is_terminal`, `stop_reason` (the terminal reason, else with `--max-iterations` the stopping rule's), `summary` (the `db-read summary` object), `findings_total`, `findings` (at most `--limit`, 100 by default, the most converged first and then the newest, each with its id, wave, agent, label, convergence, text, evidence, sources and dependencies), `open_gaps` and `open_conflicts`, and with `--since-iteration S` also `passes_this_run`. With `--wave W`, `findings` and `findings_total` are that wave's, and it also prints `wave` and `wave_sources`: the sources recorded at the wave, each with its `url`, `validation_status` (`unchecked` until `validate-sources` checks it) and `http_status`. `chb hive write-synthesis --out <path>` reads Markdown from stdin, refuses it when empty, writes it to the path with `--open-questions` (a JSON list) under `## Open questions`, and prints `path` and `bytes`.

`--max-iterations` and `--expect-iteration` below 1 are refused before anything changes. `chb guard --json` and `chb validate --json` still exit non-zero when the gate stays closed or a check fails; a command node that must read their JSON then lists 1 in `ok_exit`.

### Requirement: A derived evaluation verdict

`chb guard --eval-stdin` SHALL read an evaluation from stdin as `{coverage, depth, sources, actionability, gaps}` and derive its verdict. The verdict is `COMPLETE` when `gaps` is empty and each of the four scores is at least 4, and `NEEDS_MORE_WORK` otherwise. That rule is a definition. It refuses, before writing anything, input that states a `verdict`, a score that is missing or not a whole number from 1 to 5, `gaps` missing or not a list of non-empty strings, and `--eval` given as well. It records each gap as an `important` gap at the guarded wave, agent `evaluator`, with no coordinates, unless the wave already holds an open gap with the same text, and then runs the gate with the derived verdict. `gaps_written` counts the gaps it wrote. So an evaluator that names the same gap every pass writes it once, and a pass that only repeats it is not progress (§ The hive loop's stopping rule). The plan's gap-fill step dispatches important gaps once no critical gap is open, so such a gap is researched in a later pass, and it blocks termination until `resolve_gap` closes it.

#### Scenario: A stated verdict is refused
- **WHEN** the evaluation on stdin carries `"verdict": "COMPLETE"`
- **THEN** guard exits with an error, and no evaluation and no gap is written

#### Scenario: Named gaps block completion
- **WHEN** every score is 5 and `gaps` names two questions
- **THEN** the recorded verdict is `NEEDS_MORE_WORK`, two important gaps are written, and the gate stays closed

#### Scenario: A repeated gap is written once
- **WHEN** a later evaluation of the same wave names one of those questions again, word for word, and one new question
- **THEN** only the new question is written, and `gaps_written` is 1

### Requirement: Gain control

The signals SHALL turn metrics into parameter changes, which the plan emits as `adjust_params` actions:
- a `tremble_dance` lowers `batch_size` by 2, so fewer new findings arrive while verifiers catch up;
- a `shaking_signal` for gaps it can dispatch raises `batch_size` by 3;
- the tier rule moves `model_tier` along the ladder (`haiku` → `sonnet` → `opus` on the shipped models config), driven by the `shaking_signal` for a latest wave that unknowns dominate, rousing stronger workers and letting them rest again.

The ladder SHALL come from the models config's `hive_tiers:` (`hive.ModelTiers`, `models.Config.HiveLadder`), cheapest first. Under a routing profile, which `HIVE_PROFILE` names and `agent-run --profile` sets for the commands it runs, it is the profile's `hive_tiers:`, else its `hive-research` model alone, else its default route's model alone, the route a `hive-research` node no role route serves takes, so the plan never moves the hive onto a model the profile does not route; a name the config does not hold leaves the config's ladder, and `agent-run` refuses such a name before any node runs. `hive.InitProject` starts a new hive at the start rung: the ladder's middle, rounding down (`sonnet` on the shipped ladder), unless a profile that keeps the hive on this machine puts that rung outside the range (below). What `model_tier` names is a label: the plan's research actions carry no model (§ The plan is advisory unless applied), and the hive workflow's model nodes take theirs from their tier, or under a profile from their route.

The tier rule (`hive.decideTier`) SHALL move `model_tier` only as follows. A scan *holds* when it raises the `shaking_signal` for a latest wave that unknowns dominate: more than half of a wave of five findings or more. A scan is *clear* when unknowns are at most a quarter of the latest wave, a wave with no findings included. Any other scan is *in the band*.
- On a scan that holds, the tier rises one rung, unless it rose fewer than N scans ago or sits at the ceiling. So a hive rises on the first scan that holds, and then at most once every N scans while the condition holds. A fall does not delay a rise.
- On the M-th clear scan in a row, the tier falls to the next rung of the range below it, unless it is at the start rung or below. A scan that is not clear restarts the count, and so does a fall, so the tier falls one rung per M clear scans, back to the start rung and no further.
- A tier outside the range moves into it on the next scan, however many rungs away it is: one off the ladder to the start rung, any other to the nearest rung of the range below it, else above it.

The range is the ladder's rungs up to the ceiling, less any rung off this machine under a profile that keeps the hive on it (below). The ceiling is the top rung under budget modes `premium` and `standard`, and the start rung under `cheap` and `free`, the downshift modes, and under a name that is no mode. The mode is `HIVE_BUDGET_MODE`, which `agent-run --budget-mode` exports for the commands it runs. `agent-run` refuses a name that is no mode; one that reaches the rule another way, such as a direct `chb hive next`, allows nothing above the start rung, and the reason names it. Only a tier moved from outside the rule, or a change of ladder, profile or mode, leaves the range. So a rise moves one rung. A fall moves one rung, or more only to step over a rung off this machine. A move into the range may cross several: staying in the range outranks moving one rung a scan.

N is 2, M is 3, and the clear share is a quarter. They and the ceilings are choices. N is 2 because the hive loop stops after two passes that change nothing (§ The hive loop's stopping rule): in a running loop, some pass between two rises changed the research state. N does not rest on a wave dispatched at the new rung, since `model_tier` is a label that no dispatch reads. M is 3, above N, so the tier falls more slowly than it rises. The clear share is half the share at which unknowns dominate, so the band between the two is a quarter of the wave wide. One finding is at most a fifth of a wave the signal judges, so relabelling one finding cannot take such a wave from clear to dominated.

The hysteresis follows from the rule. Two rises are at least N scans apart, and two falls at least M. The tier turns from rising to falling only after M clear scans in a row, and from falling to rising only on a scan that holds. So waves that stay in the band, or cross only one of its edges, never turn it. A condition that comes and goes every other scan never lowers the tier, and neither do waves that alternate between dominated and in the band. Waves that swing across the whole band do turn it, and the rule follows them: a dominated wave and then M or more clear ones raise the tier and lower it again each time, a cycle of at least M+1 scans.

Under `standard` the range reaches the ladder's top rung, `opus` on the shipped ladder. The runner's `standard` mode resolves no tier to an opus-grade model; only `premium` does (runner.md § Model tiers and budget mode). The two dials differ by choice: `model_tier` is a label, and a `standard` ceiling at the start rung would leave the rule nothing to raise under the default mode.

Under a routing profile the tier moves along the profile's escalation for `hive-research`, its `hive_tiers:`. A profile that defines none pins `hive-research` to its model, its default route's when it routes no `hive-research`, and a rise is not applicable. A profile that gives `hive-research` no cloud route (runner.md § Routing profiles) keeps the hive on this machine, and the rule SHALL NOT leave `model_tier` on a rung off it: such a rung is outside the range. A rise onto one is not applicable, and the tier holds. A tier already on one moves to the nearest rung of the range, and a fall steps over one. The start rung is then the nearest rung on this machine below the middle, else above it. With no rung of the ladder on this machine the range is empty: the start rung is the middle, and every scan records `not_applicable` and leaves the tier where it is. A rung is off this machine when it is an Ollama cloud tag, or a model the models config lists, by id or alias, in the `anthropic`, `openai` or `google` family (`models.Config.OffMachineModel`). That every model listed in those families is served off this machine is an assumption about how the config is written.

The plan emits a move as one `adjust_params` action whose description gives the reason. Each `chb hive next --apply` pass SHALL record the rule's decision in `hive_tier_log`, a table of its own: the pass, the latest wave, whether the condition held, the tier before and after, the outcome (`hold`, `rise`, `fall`, `clamp` or `not_applicable`), the reason, and the rule's clock (`wait_scans`, `clear_scans`). `hive.ScanState` reads the clock from the latest row, so every scan plans the same move from the same state. A scan without `--apply` records no row and moves no clock; its plan proposes what an applied scan would do. The rule's bounds are a guarantee over the scans `--apply` runs, as the shipped workflow's scans do, and over nothing else. It rests on nothing else writing `model_tier` between them. On the advisory route, where an agent reads the plan from a plain `chb hive next` and passes its `adjust_params` to `chb hive complete` as params, the clock does not run: each plan proposes a rise while unknowns dominate, none proposes a fall, and a tier set that way is not recorded in `hive_tier_log`. The next `--apply` scan brings such a tier into the range, and its clock starts from the last row. `chb hive reset` keeps the log and the tier.

A `tremble_dance` outranks a `shaking_signal`: when both fire in one plan, the plan lowers `batch_size` and does not raise it. Each plan changes `batch_size` at most once. The plan emits no adjustment that would leave its parameter unchanged, such as a raise at 20 or a rise at the ceiling. No signal changes `convergence_threshold`; only params passed to `chb hive complete` do. Parameters reach `hive_state` through `chb hive next --apply`, or through params passed to `chb hive complete --outputs '{"params":{...}}'`. In every case `applyGainControl` (`internal/hive/gain.go`) SHALL clamp `batch_size` to 2–20 and `convergence_threshold` to 2–10, store the start rung (`sonnet` on the shipped ladder) for a `model_tier` off the ladder, and report a failed write as an error. It SHALL refuse a `batch_size` or `convergence_threshold` that is not a JSON number, a numeric string included, and write nothing (`hive.CheckGainParams`). `chb hive complete` exits with an error on such a value or on `--outputs` that is not valid JSON; it refuses before consuming any signal or changing the phase. The shipped `hive` workflow scans with `--apply`.

#### Scenario: A processing backlog damps recruitment
- **WHEN** `batch_size` is 5 and 7 unresolved conflicts have no named winner
- **THEN** the plan dispatches 7 verifiers and lowers `batch_size` to 3

#### Scenario: Idle workers are roused
- **WHEN** `batch_size` is 5 and 9 critical gaps are open
- **THEN** the plan dispatches 5 of them and raises `batch_size` to 8, and the next scan raises it to 11 and then stops

#### Scenario: A wave of unknowns rouses stronger workers
- **WHEN** `model_tier` is `sonnet` on the shipped ladder, the budget mode is `standard`, the tier did not rise on the previous `--apply` scan, and the latest wave holds 5 findings, 3 of them `unknown`
- **THEN** the plan moves `model_tier` to `opus`

#### Scenario: The tier holds while the same wave stands
- **WHEN** `chb hive next --apply` raised `model_tier` to `opus` for a wave of unknowns, and the next two scans find that wave still the latest
- **THEN** both hold `opus`, and each records a `hold` in `hive_tier_log`

#### Scenario: The tier falls back
- **WHEN** `model_tier` rose to `opus`, and then a wave of 5 findings, 1 of them `unknown`, is the latest for three `--apply` scans in a row
- **THEN** the first two hold `opus`, and the third moves `model_tier` back to `sonnet` and records a `fall` whose reason names the three scans

#### Scenario: Waves near the line
- **WHEN** `model_tier` rose to `opus`, and the latest waves then alternate between 3 and 2 unknowns of 5
- **THEN** the tier never falls: a wave with 2 unknowns of 5 is in the band, and each one restarts the count of clear scans

#### Scenario: A climb on a longer ladder
- **WHEN** a ladder of seven rungs starts a hive at its fourth, and every scan finds unknowns dominating
- **THEN** the tier rises on scans 1, 3 and 5, reaching the top, and holds there

#### Scenario: A condition that comes and goes
- **WHEN** unknowns dominate every other scan
- **THEN** the tier never falls

#### Scenario: Waves that swing across the band
- **WHEN** a wave that unknowns dominate is followed by three clear ones, again and again
- **THEN** the tier rises on each dominated wave and falls on the third clear one after it, a cycle of four scans

#### Scenario: A tier above the range
- **WHEN** a ladder of five rungs runs under budget mode `cheap`, and `model_tier` is its top rung
- **THEN** the next `--apply` scan moves it two rungs down to the start rung and records a `clamp`

#### Scenario: Cheap mode caps the climb
- **WHEN** `agent-run --budget-mode cheap` runs `chb hive next --apply` at `sonnet` for a wave of unknowns
- **THEN** `model_tier` stays `sonnet`, and the recorded reason names budget mode cheap

#### Scenario: A profile's one-rung ladder
- **WHEN** a hive runs under `agent-run --profile local-fast` and a wave of unknowns raises a shaking signal
- **THEN** `chb hive init` recorded `model_tier` as local-fast's `hive-research` model, the plan emits no tier change, and the scan records it as `not_applicable`: local-fast pins `hive-research` and defines no `hive_tiers`

#### Scenario: A local-only profile with a cloud rung
- **WHEN** a profile routes `hive-research` to `qwen-mid` on provider `local` with `hive_tiers: [qwen-small, qwen-mid, claude-opus-4-8]`, and a wave of unknowns raises a shaking signal on each of three scans
- **THEN** `model_tier` stays `qwen-mid`, and each scan records `not_applicable` naming `claude-opus-4-8` as off this machine

#### Scenario: A local-only profile with a cloud middle rung
- **WHEN** a profile routes `hive-research` to `qwen-small` on provider `local` with `hive_tiers: [qwen-small, gpt-oss:120b-cloud, claude-opus-4-8]`
- **THEN** `chb hive init` starts the hive at `qwen-small`, a wave of unknowns records the rise onto `gpt-oss:120b-cloud` as `not_applicable`, and a `model_tier` of `claude-opus-4-8` left by another run moves to `qwen-small` on the next `--apply` scan, recorded as a `clamp`

#### Scenario: A wave of assumptions rouses nothing
- **WHEN** the latest wave holds 5 findings, all `assumption`
- **THEN** the scan raises no `shaking_signal` to move `model_tier`

#### Scenario: Stopped gaps rouse no workers
- **WHEN** `batch_size` is 5, `conflict_rate_threshold` is 0.15, 3 unresolved conflicts touch 6 coordinates among 6 findings, and a critical gap sits at each of them
- **THEN** the scan raises no `shaking_signal` to raise `batch_size`, and the plan dispatches no gap fill and leaves `batch_size` at 5

#### Scenario: A tremble dance outranks a shaking signal
- **WHEN** `batch_size` is 5, 7 conflicts wait for a verifier, and 9 critical gaps are open
- **THEN** the plan's only `batch_size` change lowers it to 3

#### Scenario: An out-of-range batch size is clamped
- **WHEN** an adjustment asks for `batch_size` 99
- **THEN** `hive_state.batch_size` becomes 20

#### Scenario: A non-numeric parameter is refused
- **WHEN** `chb hive complete --outputs` carries `{"params":{"batch_size":"7"}}`, or is not valid JSON
- **THEN** it exits with an error, and `batch_size`, the phase and the pending signals are unchanged

### Requirement: Signals are consumed only by a plan that read them

A signal SHALL be marked acted on at most once, and only by an iteration whose plan read it, or retired by `chb hive reset`. `chb hive next` records the highest pending signal id its scan read in `hive_state.signals_through`. `chb hive complete` SHALL set `acted_on=1` only on pending signals at or below that watermark. `chb hive signals --pending` and plan suppression read only `acted_on=0` rows. `chb hive complete` refuses a project with no hive state.

#### Scenario: A signal written during an action survives completion
- **WHEN** an agent writes a signal after `chb hive next` and before `chb hive complete`
- **THEN** the signal is still pending after `complete`, for the next plan to read

### Requirement: Terminal detection

`hive.CheckTermination` SHALL declare a project terminal only when all hold: no critical gaps and no important gaps remain, no conflicts are unresolved, the MSS audit passes, the latest evaluation's verdict is `COMPLETE`, and at least one wave gate is open. `chb hive status` SHALL report `is_terminal` and the first failing reason.

#### Scenario: One open conflict keeps the hive running
- **WHEN** every condition holds except one unresolved conflict
- **THEN** `is_terminal` is false and the reason names the unresolved conflicts

### Requirement: One hive per database

A hive reads findings, gaps, conflicts and signals across its whole database (`hive.ScanState`), so a database SHALL hold at most one hive project, and nothing inside a database is scoped by project. `hive_state.project` labels the run. A second project does not share the database: it gets its own workspace database, by the rule below.

When a hive command names a project X — `--project X` on `chb hive init|next|complete|status|signals|reset|report`, the `project` input of `workflows/hive.yaml`, and the `project` of the MCP tool `chb_status` — the database it uses SHALL be, in this order:

1. the database the caller named (`--db`, else `HIVE_DB_PATH`, else `workspace/hive.db`; a server's own store), when it hosts no hive or hosts X's;
2. otherwise X's workspace database, `<root>/workspace/X/hive.db`, under the named database's workspace root. `chb hive init` and a run of a workflow that drives the hive create it, as `chb init X` does (the directory, the file, the schema). Every other command refuses a missing one, naming the path.

That rule is a definition, and `hive.Resolve` (`internal/hive/workspace.go`) is the one function that applies it; `hive.Init` is `Resolve` with the hive's record. The workspace root is the parent of the `workspace` directory the named database sits in, at either depth chb lays out: `<root>/workspace/hive.db`, the default path, and `<root>/workspace/<name>/hive.db`, what `chb init <name>` makes. So a second project's database lands beside the first one's, in the tree or volume the caller chose by naming the first. A database in neither layout has no `workspace/` directory to anchor on, and its root is the current directory, where `chb init X` would make one. A project that is not a bare directory name has no workspace database and is refused at step 2.

Two refusals remain. A database the caller pinned with `--db` for this one command, and that hosts another project's hive, is refused, naming both projects, the database and the path X uses: a pin is a choice the rule does not override. `HIVE_DB_PATH` and the default path pin nothing; they are the session's database, which is how a container with one volume and one `HIVE_DB_PATH` hosts a hive per project. And X's workspace database is where the rule ends, so one that hosts a third project's hive is refused, naming it.

`hive.InitProject` checks and inserts in one conditional statement, so two inits of different projects at once leave one hive in the named database, and `hive.Init` sends the other on to its workspace database, as if the first had been there all along. Every surface says which database it used: `db` in each `--json` object, in `chb hive next`'s and in `chb hive report`'s; a `Database:` line in each text report; `db` in `chb_status`'s. `chb hive write-synthesis` reads no database.

A run of `workflows/hive.yaml` resolves once, in `chb agent-run`, before the run begins: when the workflow has a command node that runs `chb hive` and the inputs name a `project`, the run's database is the one the rule chooses, created if missing, and `agent-run` logs it. Every node and agent of the run then reads and writes that database — the dispatch agent's `chb db-write`, the gate's `chb guard`, `swarm-merge`, `export-graph` and the run's own rows — because the run's store and every subprocess's `HIVE_DB_PATH` are set from it (runner.md § Command nodes, § Backends). The `init` node's `db` output records it in state. That every node writes there is a guarantee resting on the runner setting both from one path; it does not rest on the dispatch model passing `--db`.

#### Scenario: A second project on a one-volume MCP host
- **WHEN** `HIVE_DB_PATH` is `/app/workspace/hive.db`, it hosts project A, and `chb hive init --project B` or a hive workflow run for B arrives
- **THEN** B's hive is created in `/app/workspace/B/hive.db`, the output names it (`db`), A's database gains no row, and `chb_status {project: B}` reads B's database and names it

#### Scenario: A command before init
- **WHEN** the database hosts project A and `chb hive next --project B` runs with no `--db`, before `chb hive init --project B`
- **THEN** it is refused, naming `workspace/B/hive.db` and A, and creates nothing

#### Scenario: A pinned database that hosts another hive
- **WHEN** `chb hive init --project B --db workspace/hive.db` names a database that hosts project A
- **THEN** it is refused, naming A, B, that database and `workspace/B/hive.db`, and creates nothing; the same command without `--db` creates B's hive there

#### Scenario: The hive workflow for two projects from one directory
- **WHEN** `chb agent-run workflows/hive.yaml` runs for project A on the default database, and then for project B, whose seeded workspace database `chb init B` made
- **THEN** B's run reads and writes `workspace/B/hive.db`: its `init` reports it as `db`, its run rows and its pass are there, and A's database is as A's run left it

#### Scenario: Two projects initialized at once
- **WHEN** inits of projects A and B run concurrently against one empty database
- **THEN** exactly one is created there, and the other is created in its workspace database, named in its answer

#### Scenario: Initializing the same project again
- **WHEN** `chb hive init` names a project already on record
- **THEN** it prints that it is already initialized, naming the database; any other insert failure is an error exit

### Requirement: Reset

`chb hive reset --project <name>` SHALL set `iteration=0` and `phase='scanning'`, clear `terminal_reason`, mark every pending signal acted on and clear the project's pass history in `hive_iterations`, keeping all findings, caps, gaps, conflicts and signal rows. It SHALL write all of it in one transaction, so a reset that fails partway changes nothing. It SHALL refuse a project with no `hive_state` row rather than report a reset that did not happen.

#### Scenario: Reset of an unknown project
- **WHEN** `chb hive reset` names a project with no hive state
- **THEN** it exits with an error and changes nothing

#### Scenario: A reset that fails partway
- **WHEN** clearing the pass history fails after the reset has set the count and retired the pending signals
- **THEN** it exits with the error, and the hive's count, phase, pending signals and pass history are as they were

## Files

- `internal/hive/state.go` — `ScanState`, `SignalsThrough`, `consumeSignals`, `ModelTiers`
- `internal/hive/scan.go` — `RecordScan`, the scan's one write transaction, and `applyRepairs`, its `fix_mss` and `cascade_revert`; `CompleteIteration`, the pass's end
- `internal/dreamer/settle.go` — `SettleInTx`; `internal/db/cascade.go` — `Tx.CascadeRevert`; `internal/db/conflicts.go` — `Tx.ResolveConflict`
- `internal/hive/progress.go` — `Progress`, `Stalled`, `LoopDecision`; `internal/db/schema.go` — `hive_iterations`; `internal/db/tx.go` — `Store.BeginImmediate`
- `internal/hive/signals.go` — `EvaluateSignals`
- `internal/hive/plan.go`, `internal/hive/plan_steps.go` — plan generation and its ordered steps
- `internal/hive/termination.go` — `CheckTermination`
- `internal/hive/caps.go` — `applyCaps`, and `ApplyCaps`, its test seam; `internal/hive/project.go` — `ExistingProject`, `InitProject`; `internal/db/schema.go` — `capped_findings`
- `internal/hive/workspace.go` — `Resolve`, `Init`, `WorkspaceRoot`, `WorkspacePath`: the database a hive command for a project uses; `internal/cli/hive.go` — `hiveStore`, `resolveHiveRun` (`chb agent-run`'s resolution)
- `internal/hive/gain.go` — `applyParamAdjustments`, `applyGainControl`, `CheckGainParams`
- `internal/hive/tier.go` — `decideTier`, the tier rule, and its policy from the ladder, the budget mode and the profile; `internal/hive/scan.go` — `recordTier`; `internal/db/schema.go` — `hive_tier_log`; `internal/models/config.go` — `OffMachineModel`, `OllamaCloudModel`
- `internal/db/conflicts.go` — `ConflictsRepo.SetWinner`, `ConflictsRepo.Resolve`
- `internal/cli/hive.go` — `chb hive` commands; `internal/cli/hive_report.go` — `chb hive report`, `chb hive write-synthesis`; `internal/mcp/tools_status.go` — `chb_status`
- `internal/cli/swarm_merge.go`, `detect_conflicts.go`, `guard.go` — `chb swarm-merge`, `chb detect-conflicts`, `chb guard`; `internal/gate/derive_eval.go` — `DeriveEvaluation`
- `internal/cli/db_write.go` — `chb db-write conflict_winner`, `resolve_conflict`, `cascade_revert`, `resolve_gap`
- `internal/db/record.go` — the `resolve_gap` and `resolve_conflict` kinds of `Store.WriteRecord`, shared by `chb_db_write` in the runner and over MCP and by `chb db-write`
- `agents/hive-scout.md`, `agents/verifier.md` — the protocols the dispatch node follows
- `workflows/hive.yaml` — the `hive` workflow: the command nodes init, scan, validate-sources, gate-state, gate, merge, detect-conflicts, complete-iteration, report, write-synthesis and export-graph; the model nodes dispatch, evaluate and final-synthesis; the loop
