# Design Bench Specification

## Purpose

The design bench measures whether HIVE's research produces a design and a plan that someone else can execute. Bench-1 (bench.md) grades closed roster questions. This bench grades code: HIVE researches a task and writes a design document and an execution plan; a plain executor who saw neither the task nor the research carries the plan out; hidden tests grade what it built. The same executor carries out a plan written by one model call from the same inputs, so the comparison isolates what the swarm's research adds. `chb design report` applies a decision rule fixed before the first run.

## Requirements

### Requirement: A task

A task SHALL be a directory under `fixtures/design/<task>/` holding `task.yaml` (its `family`, and `twin` naming its twin when it has one), `statement.md` (the problem, which fixes the package, the public names and signatures the hidden tests call, and leaves the design open), `tree/` (a Go module: the repository the designer reads and the executor edits), `hidden/` (test files, mirroring `tree/`'s layout, that neither arm nor the executor sees), `reference/` (one correct solution's files, mirroring `tree/`, overlaid on the tree to check the hidden tests) and `note.md` (what a correct design must decide, for the reader of a run; no model sees it). `design.LoadTasks` reads them in name order and refuses a task missing any of these, a twin that does not name it back, or a `hidden/` test whose name does not begin with `TestHidden`, the prefix that keeps a test the executor writes from colliding with one. `fixtures/design/go.mod` fences the fixtures off the repository's module, so `go vet ./...` and `go test ./...` at the root do not compile them; each task's `tree/go.mod` fences the tree in turn. The hidden tests use the standard library only, so the harness needs `go` and nothing to install.

The tree a model sees is `design.CopyTree`: `tree/` alone. The statement reaches the designer and the control in their prompts; the note, the hidden tests and the reference reach no model. The twelve shipped tasks and their twins are listed in `fixtures/design/README.md`.

A hidden suite SHALL discriminate: run on the reference solution every hidden test passes, and run on the unmodified tree at least one fails or the package does not build. Twin tasks SHALL share a byte-identical `tree/` and require different designs: each twin's reference solution fails at least one of the other twin's hidden tests. A designer that answers from the tree alone, ignoring the statement, therefore fails one twin of every pair. `TestShippedTasks` recomputes all of this from the fixtures on every run.

#### Scenario: A twin pair
- **WHEN** `cache-lru` and `cache-ttl` are loaded
- **THEN** their trees hash the same, each names the other as its twin, and `cache-lru`'s reference fails `cache-ttl`'s hidden tests and the reverse

### Requirement: The pack, the arms and the executor

The pack SHALL be the statement followed by every file of the tree, each under its path in a fenced block. Both arms read it. The engine substitutes a prompt's placeholders in one pass and never reads a value's own braces (workflow.md), so the Go code in the pack is safe in an input.

The **designer arm** SHALL be two stages. First `chb ask` over the pack: the question asks for the design the task needs and what a correct design must decide; the pack is `--context-file`; the lenses get `--lens-tools read` with the tree as their working directory, so they can read it; the case's preset (default `minimal`); `--no-eval`. Its research is Queen's report, verdict and recommendation from the artifact. Second the plan call: one node, role `implement-plan`, no tools, the plan prompt over the pack with the research appended under a `## Research` heading, its output schema enforced (`design.DocumentSchemaJSON`), one repair. The **control arm** SHALL be the plan call alone: the same prompt without the research block, the same model, schema and repair. So the control is the designer with the swarm removed, and the two differ in nothing else. This is a choice: the review and implement workflows audit a codebase and fix findings, neither writes a design from a statement, and `chb ask` is the research path Bench-1 measured, so its result composes with Bench-1's.

The plan call SHALL return the design document (`design`, Markdown: the decisions, the public API with exact signatures, behaviours and edge cases), its claims (`claims`: each with a `label` in the MSS partition and `rests_on`, the indexes of the earlier claims it follows from) and the plan (`plan`: steps with `step`, `action` and `files`). Both arms are told that the executor sees neither the statement nor the research, so the design must carry every name, signature and behaviour the executor needs.

The **executor** SHALL be one agent node under the plain coder persona (`agents/coder.md`), role `implement-fix`, with `shell`, `read_file`, `write_file`, `edit_file`, `glob` and `grep`, working in a fresh copy of the tree, given the design document and the plan and nothing else: not the statement, not the research, not the designer's run and not the hidden tests. Its report schema is enforced (`design.ReportSchemaJSON`): `steps_done`, `deviations` (each a step and why), `additions` (what it did that no step asked for) and `summary`. Its turns are bounded by the backend's cap, 30 on the OpenAI-compatible and local backends, and its wall time by the case's `executor_minutes` (default 10): past it the run is killed, recorded as not executed, and its tree is graded as it stands. The same executor runs on both arms' plans, so only the plan differs.

#### Scenario: What the executor is given
- **WHEN** the executor lists every file under its working directory
- **THEN** it finds the tree's files and nothing under `hidden/`, no `note.md`, no `reference/`, and no file of the designer's run

#### Scenario: The arms' inputs
- **WHEN** the harness writes the control's and the designer's plan workflows
- **THEN** the designer's prompt with its `## Research` block removed is the control's prompt, and the two nodes name the same model, role, schema and repair

### Requirement: The grade

Each arm's executed tree SHALL be graded in a copy with the hidden test files dropped in at their mirrored paths, by `go test -json -count=1 ./...` under `GOTOOLCHAIN=local`, with a deadline. The total is the number of `TestHidden*` functions in the hidden files, counted by parsing them (`design.CountHiddenTests`), so a tree that does not build scores 0 of the total rather than 0 of 0. Passed is the number of those top-level tests whose action was `pass`. A subtest counts through its parent. The pass rate is passed ÷ total.

Plan completeness SHALL be the share of the files the reference solution changes that the plan names. The reference's changes are the files under `reference/` that `tree/` lacks or holds with other bytes (`design.ReferenceChanges`), computed by the harness from the fixture and never by a model; `LoadTasks` refuses a reference that changes nothing. A plan step names a file when its `files` holds the path or a directory it is under. A run with no plan has no completeness. This is the rule's second metric.

Plan fidelity SHALL be read from the executor's own report: the plan's steps, the deviations it recorded and the additions it made, and the deviation rate (deviations + additions) ÷ steps, absent when the plan has no step. It is self-reported: a model that never records a deviation scores perfect fidelity. So the row also holds file coverage, computed from the tree: the files the executor changed, added or removed (the manifest before and after the run), how many of them a plan step named, and the fraction. Both are reported, with their own sign test, so the live run shows whether the self-report agrees with the diff; the decision rule reads neither. A run with no plan, or whose executor left no report, has steps at most and no deviation rate.

Label honesty SHALL be computed from the claims: how many there are, how many carry a label in the partition, how many are guarantees, how many of those rest on at least one earlier claim, and whether the audit passes. The audit writes the claims as findings into a fresh database through `db.FindingsRepo.AddFinding`, with `depends_on_ids` the ids of the claims each rests on, and runs `db.RunAudit`. The store refuses a guarantee with no premise or one resting on an unknown, and the audit fails on laundering, an untraceable guarantee, a cycle or a label outside the partition; either failure is an audit failure. Under an enforced schema every claim carries a label, so the figure that varies is the guarantees with premises and the audit.

A row SHALL hold the configuration, provider, models, task, family, twin, arm, tests passed and total, pass rate, the reference's files and how many the plan names and the completeness, plan steps, deviations, additions, deviation rate, files changed and named and the coverage, claims, labelled, guarantees, guarantees with premises, audit pass, whether the design was produced, whether the executor completed, whether the designer's tree was unchanged, wall time in all and per stage, tokens, and every node's model, status, tokens and wall time. Rows go to `<workspace>/<case>/results.jsonl`, one per task and arm.

#### Scenario: A tree that does not build
- **WHEN** the executor leaves a syntax error in the package
- **THEN** the row has `tests_passed` 0 and `tests_total` the hidden suite's count, and the arm's pass rate is 0

#### Scenario: A dishonest guarantee
- **WHEN** a design's claims hold a guarantee with an empty `rests_on`, or one resting on an unknown
- **THEN** the store refuses it, `audit_pass` is false, and the guarantee is not counted among those with premises

### Requirement: An outage is not a result

Before the first run of a design case the harness SHALL ask the endpoint whether it serves every model the case's runs will send, with the runner's model preflight over the workflows the runs dispatch: the swarm's from `chb ask --no-dispatch`, both plan workflows and the executor's, routed as their agent-run routes them. When the check fails the case fails with the preflight's message, runs no task and writes no `results.jsonl`, and the workspace keeps an earlier run's results. A stage that reached no model (left no `workflow_runs` row), or that did not complete and after which the endpoint fails the preflight, SHALL stop the case at that run. A case that stopped, or in which no run completed, writes its rows under a first line `{"not_measured": why}`, which `design.ReadRows` refuses, so `chb design report` refuses the file and says why. A stage that did not complete while the endpoint still passes, an executor out of turns or out of time, a plan the schema rejected after its repair, a tree that does not build: these are data, and the row records them. The harness also requires `go` on `PATH` before the first run; without it the case fails under its own check, since nothing could be graded.

#### Scenario: The endpoint goes down mid-case
- **WHEN** the server stops answering after N runs
- **THEN** the case stops at run N+1, naming it and the refused connection, no later run starts, and `results.jsonl` holds the rows so far under a `not_measured` line, which `chb design report` refuses

### Requirement: The decision rule

`chb design report [--alpha A] <results.jsonl>...` SHALL print the per-task table (both arms' tests, the reference files the plan names, steps, deviations, coverage, labels, audit, wall and tokens), the per-arm means, and the verdict of `design.Decide` with the token-cost ratio beside it. The rule, fixed before the first live run, in three lines:

1. Pair the tasks by name; on each paired task take the designer's hidden-test pass rate minus the control's (an arm with no executed result scores 0) and the designer's plan completeness minus the control's (tasks where an arm has no plan are dropped from this metric and counted); on each metric drop the ties and sign-test the losses L among the n non-tied tasks, p = P(Bin(n, ½) ≥ L): `worse` when p ≤ α (default 0.05), else `not-below` when the mean difference over all paired tasks is at least 0, else `below`.
2. The verdict is `too-few` under two paired tasks; `no-difference` when every paired task ties on hidden tests, whatever completeness says; `worth-it` when hidden tests are `not-below` with at least one task the designer won outright and completeness is `not-below`; otherwise `not-worth-it`.
3. Beside every verdict the report prints the token-cost ratio: the designer arm's tokens, in and out, executor included, over the control's, summed over the paired tasks; a tie is reported with what it cost.

The self-reported deviation rate (control minus designer) is sign-tested and printed the same way, marked as reported and not in the rule. `inconclusive` is not an outcome here: with these sample sizes the test cannot show equivalence, and the rule says so instead of pretending.

What the sample size allows, stated honestly. With twelve tasks the sign test can call the designer `worse` only when it loses at least ten of twelve non-tied tasks (p = 0.019), nine of ten, eight of nine, seven of eight, or every one of five to seven; with fewer than five non-tied tasks it can never reject. A designer that truly loses 80% of the time escapes it 44% of the time at n = 12. `not-below` is therefore a screen on the mean and the absence of a lopsided loss, not a non-inferiority result at any stated power. `TestDecide_SignTestLimits` computes these counts from the binomial and fails if the rule's critical counts differ. The ties matter: two arms that both pass every test on a task tie, and a run where every task ties leaves n = 0, which the report says.

#### Scenario: A designer that loses every task
- **WHEN** the control's pass rate exceeds the designer's on all twelve tasks
- **THEN** the test metric is `worse` with p = 2⁻¹² and the verdict is `not-worth-it`

#### Scenario: Equal arms
- **WHEN** both arms pass every hidden test on every task
- **THEN** the hidden-test metric has n = 0, the verdict is `no-difference`, and the report prints the token-cost ratio beside it, so a reader sees what the tie cost

#### Scenario: One task won
- **WHEN** the designer passes more hidden tests than the control on one task, ties the other eleven, and its plans name every reference file the control's do
- **THEN** hidden tests are `not-below` with one win, completeness is `not-below`, and the verdict is `worth-it`

#### Scenario: A plan that misses the file
- **WHEN** the arms tie on hidden tests except one task the designer wins, and the designer's plans name the reference's files on fewer tasks than the control's
- **THEN** completeness is `below` and the verdict is `not-worth-it`

### Requirement: What the bench does not claim

The tasks are small, in Go, and fixed to a public API the hidden tests call. Passing them does not show that HIVE designs large systems. A plan can be correct and the executor fail it; the executor is held constant across arms so that this cost falls on both. Fidelity is self-reported, and file coverage is its only check. The twelve tasks were written by one author with their reference solutions, so a hidden test may encode that author's reading of a statement; the note of each task says what it decides. A run's cost: the designer arm makes the swarm's calls (bench.md § What each case costs) plus the plan call and the executor's turns; the control makes the plan call and the executor's turns. The first run's result, `not-worth-it` at 1.68× (2026-10-06), with its table and its reading, is in `docs/evidence/2026-10-06-bench-design/`.

## Files

- `internal/design/task.go` — `Task`, `LoadTasks`, `CopyTree`, `Overlay`, `Pack`, `ReferenceChanges`, `HiddenPrefix`
- `internal/design/grade.go` — `HiddenTestNames`, `CountHiddenTests`, `RunHiddenTests`, `ReadTestEvents`
- `internal/design/document.go` — `Document`, `Report`, `DocumentSchemaJSON`, `ReportSchemaJSON`, `PlanFidelity`, `FileCoverage`, `PlanCompleteness`, `CheckLabels` and its audit
- `internal/design/rows.go` — `Row`, `WriteRows`, `WriteNotMeasured`, `ReadRows`, `ErrNotMeasured`
- `internal/design/decide.go` — `Decide`, `BinomialTail`, `CriticalLosses`, `Summarize`
- `internal/harness/design.go` — the `design` case kind: `runDesignCase`, `runDesignTask`, `planWorkflow`, `executorWorkflow`, `designWorkflows`, the report section and its tables (`WriteDesignTables`), which `chb design report` prints too
- `internal/harness/design_task.go` — one run of a task on an arm, stage by stage (`designTaskRun`): the pack, the designer's swarm research and plan call or the control's plan call, the executor in a fresh copy of the tree under `executor_minutes`, its file coverage and its report, and the hidden tests
- `internal/harness/sweep.go` — what the design case shares with the bench case (bench.md): the routing its runs resolve under, the model preflight before the first run, the sweep that stops the case at an outage, and `results.jsonl` written under `not_measured`
- `internal/cli/design.go` — `chb design report`; `internal/harness/harness.go` — the case fields `tasks`, `task_names`, `executor_minutes`
- `fixtures/design/` — `go.mod`, `suite.yaml`, `README.md` and the twelve tasks
- `internal/design/*_test.go`, `internal/harness/design_test.go` — the shipped tasks discriminate and the twins cross-fail; the grader against `go test -v`; fidelity from a fixed report; completeness from a fixture's reference diff; the audit; the rule's critical counts, the tie, the one win and the cost ratio; the case end to end against a fake server whose executor applies the plan's files, the executor's view of its tree, the arms' inputs, and an outage
