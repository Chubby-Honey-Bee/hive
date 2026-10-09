# The executability benchmark's first live run (2026-10-06)

The rows, the harness reports, the design report and the progress log of the first run of `docs/specs/bench-design.md`: twelve tasks, both arms each, one model, one run per task and arm. The verdict and its reading are in § What the first run found below; the other files of this directory are the evidence they rest on.

## Configuration

- `--profile local-fast`: provider `local` at `http://localhost:11434/v1`, Ollama 0.35.0 on the development machine (`/api/version`), `qwen3.6:35b-a3b-q4_K_M` in every role: the seven lenses of the `minimal` preset, Queen, the plan call of both arms, and the executor. Lens and Queen reasoning `none`; swarm tier `--budget-mode cheap`; persona profile `full`; no sampling seed pinned. Each case's REPORT.md repeats this in its header.
- Each case: `kind: design`, `tasks: fixtures/design`, `foragers: minimal`, `executor_minutes: 10`; the executor's turn cap is the local backend's 30. Each case's suite file was a two-line restriction of `fixtures/design/suite.yaml` by `task_names`, written to a scratch directory and not kept, so the sha256 in each REPORT header is over an uncommitted file.
- Command, from the repository root: `env -u OPENAI_BASE_URL -u OPENAI_API_KEY chb agent-harness --suite <suite> --only <case> --profile local-fast --workspace <workspace>`. The two environment variables were unset so the harness's own `local` provider route is the only one in play.

## Revisions before measurement

2026-10-06, before any live run, on the coordinator's reading of the offline fake run:

1. **A tie is not worth it.** The rule as first written called a designer that tied every task `worth-it` while the fake run showed it costing about 3× the control's tokens. Now `worth-it` needs at least one task won outright on hidden tests, a run where every task ties is `no-difference`, and the token-cost ratio is printed beside every verdict.
2. **Plan completeness replaces self-reported fidelity as the second metric.** Fidelity comes from the executor's own report, and a model that never writes a deviation scores perfect. Completeness is computed by the harness from the reference diff against the plan's named files. The deviation rate and file coverage stay as reported columns, each with its sign test, so the live run shows whether the self-report agrees with the diff.

## What the first run found

2026-10-06, `--profile local-fast`: `qwen3.6:35b-a3b-q4_K_M` in every role on Ollama 0.35.0, the `minimal` preset, `executor_minutes` 10, the local backend's 30-turn cap, one run per task and arm, no sampling seed pinned. The rows, each case's harness report, `chb design report`'s output and the launcher's log are the files of this directory; § Dates and how the run was split says how the run was split into eight cases and why, and what the two killed attempts had produced.

| Task | Arm | Hidden tests | Plan names ref. files | Deviations / additions | Audit | Wall s |
|---|---|---|---|---|---|---|
| intervals | designer | 6/6 | 1/1 | 0 / 1 | pass | 266 |
| intervals | control | 6/6 | 1/1 | 0 / 1 | fail | 101 |
| cache-lru | designer | 6/6 | 1/1 | 0 / 1 | fail | 307 |
| cache-lru | control | 6/6 | 1/1 | 1 / 1 | pass | 183 |
| cache-ttl | designer | 6/6 | 1/1 | 2 / 2 | pass | 403 |
| cache-ttl | control | 0/6¹ | 1/1 | no report | pass | 130 |
| kv-quoted | designer | 5/6 | 1/1 | 2 / 2 | fail | 576 |
| kv-quoted | control | 6/6 | 1/1 | 2 / 1 | fail | 252 |
| kv-sections | designer | 5/6 | 1/1 | 2 / 1 | fail | 314 |
| kv-sections | control | 4/6 | 1/1 | 1 / 2 | fail | 155 |
| cli-head | designer | 3/5² | 1/1 | no report | fail | 608 |
| cli-head | control | 4/5 | 1/1 | 0 / 2 | fail | 190 |
| cli-wc | designer | 3/6 | 1/1 | 0 / 2 | fail | 342 |
| cli-wc | control | 2/6 | 1/1 | 0 / 1 | pass | 206 |
| ratelimit | designer | 5/6 | 1/1 | 0 / 1 | pass | 346 |
| ratelimit | control | 2/6 | 1/1 | 0 / 1 | fail | 135 |
| retry | designer | 6/7 | 1/1 | 0 / 2 | pass | 327 |
| retry | control | 7/7 | 1/1 | 1 / 1 | fail | 139 |
| slug-ascii | designer | 5/6 | 0/1 | 1 / 1 | fail | 885 |
| slug-ascii | control | 6/6 | 1/1 | 0 / 1 | pass | 666 |
| slug-unicode | designer | 6/6 | 1/1 | 2 / 2 | pass | 1335 |
| slug-unicode | control | 6/6 | 1/1 | 2 / 2 | fail | 289 |
| table | designer | 3/4 | 1/1 | 1 / 1 | pass | 992 |
| table | control | 4/4³ | 1/1 | no report | pass | 747 |

The three runs whose executor did not complete, which the report's Done column prints as "designed only" and whose row's `error` field holds the reason: ¹ cache-ttl's control, where Ollama answered one of the executor's tool calls with HTTP 500 ("XML syntax error … element <function> closed by </parameter>", on its wording the server failing to parse a tool call the model had written), the endpoint stayed up, so the row is data and the tree as left did not build; ² cli-head's designer, the 30-turn cap, the tree graded as it stood; ³ table's control, the 10-minute wall, the tree graded as it stood and passed every hidden test.

**Verdict: not-worth-it** (alpha 0.05; token cost designer ÷ control = 1.68×, executors included). This is what the rule (bench-design.md § The decision rule) returns on these rows, a guarantee given them:

- hidden tests: `not-below`. The designer won 4 (cache-ttl +1.000, ratelimit +0.500, cli-wc +0.167, kv-sections +0.167), lost 5 (table −0.250, cli-head −0.200, kv-quoted −0.167, slug-ascii −0.167, retry −0.143) and tied 3 (intervals, cache-lru, slug-unicode); p = 0.500 that it is worse, `worse` at 8 losses of 9; mean difference +0.076.
- plan completeness: `below`. Eleven ties and one loss, slug-ascii, where the designer's plan named `slug/slug.go` and `slug/slug_test.go` while the reference's file is `slug.go` at the tree's root; mean difference −0.083. This clause decided the verdict: hidden tests were `not-below` with wins, and completeness was not.
- plan fidelity (reported, not in the rule): `not-below`, 4 won, 4 lost, 1 tied, 3 dropped where an arm had no rate; mean difference +0.189.
- token cost: designer 2,527,599 ÷ control 1,504,647 = 1.68×. The designer's mean wall was 558 s against the control's 266 s.

The honest reading. Twelve tasks, one model, one run each. The sign test could have called the designer `worse` only at eight losses of nine non-tied tasks; it saw five, and p = 0.500 is the least informative value the test returns. `not-below` on hidden tests says the mean was positive and the loss not lopsided, and the positive mean rests on one row: cache-ttl's +1.000 is the control's executor dying on the server's 500, and without that task the mean over the other eleven is −0.008. The one loss on completeness is a path prefix the executor ignored. Each half of the verdict therefore turns on a single row, which is what a sample of twelve with ties looks like, and the spec's § What the bench does not claim said so before the run. What the run supports is narrower than its verdict: on these tasks and this model, the swarm's research did not make the plan measurably more executable than one call's, and it cost 1.68× the tokens and 2.1× the wall. The ratio is the upper bound the rows support: the wall-killed executor of table's control left its node `running` with no token count, so its tokens are missing from the control's sum; had it cost what the designer's executor did on the same task, the ratio would be about 1.56×. Two repeats that happened by accident bound how much one run moves: chunk 5, killed before it wrote rows, had slug-ascii's designer at 6/6 with its plan naming the reference file, against 5/6 and `slug/slug.go` in the recorded run; the abandoned twelve-task case had cache-ttl's control at 3/6 against the recorded 0/6. These are log lines, not rows, and they say that one run of one task moves by about as much as the differences the rule read. All of this paragraph past the first sentence is a reading, not a finding of the rule.

Two side findings the rows show:

1. **The audit.** The designer's design passed the MSS audit on 6 of 12 tasks, the control's on 5 of 12; by task, both passed on 2, the designer alone on 4, the control alone on 3, neither on 3. Guarantees with at least one premise: designer 19 of 29, control 27 of 38. One of the designer's passes (slug-unicode) had no guarantee at all. The designer's six failures were four guarantees with no premise and two claims resting on themselves or on a later claim; the control's seven were two and five. The research does not make the plan call's labels more honest in any way twelve tasks can show.
2. **The self-report against the diff.** Every one of the 21 executor reports recorded at least one deviation or addition: all 10 whose diff touched a file no step named, and all 11 whose diff stayed inside the plan's files. So the self-reported deviation rate carries no information about whether the executor left the plan. By task, among the nine where both arms had a rate, the deviation-rate and file-coverage differences pointed the same way on 3 (kv-sections, retry, slug-ascii), opposite ways on 2 (cache-lru, ratelimit), and on 4 one of them tied. Revision 2 above, which took fidelity out of the rule, was right; the rate stays a reported column.

Notes for the next run, not changes to the rule:

- Completeness matched the plan's paths literally. A plan that prefixed the package directory (`slug/slug.go` for `slug.go`) scored 0 while its executor edited the right file, and that one row decided the verdict. Next time, match a plan's path by suffix against the reference's path, or strip the tree's root directory from both, and print the literal match beside it.
- Every shipped reference changes exactly one file, the one the statement fixes, so completeness is 0 or 1 per arm and tied on eleven of twelve tasks. A second metric needs resolution: tasks whose reference touches several files, or a finer unit such as the public names the hidden tests call, checked against the design document.
- A wall-killed executor leaves its node `running` with no tokens or wall, so the cost ratio omits it. Read the stage's partial counts from its database before the kill is recorded.
- The report's Done column says "designed only" for a run whose executor ran out of turns, out of time, or into a server error and whose tree was graded. Print the stop reason, which the row's `error` already holds.
- The executor's 10-minute wall is in the rule's path (it ended table's control with a passing tree) and depends on the machine's load: the designer stage took 193 to 251 s on the early cases and 507 to 862 s on the late ones, under other work. A turn budget is load-independent; the 30-turn cap bound once (cli-head's designer, 477k tokens in).

## Build

- The first nine tasks (`intervals`, `cache-lru`, `cache-ttl`, `kv-sections`, `kv-quoted`, `cli-wc`, `cli-head`, `ratelimit`, `retry`) ran with `chb` built from `./cmd/chb` at f00f806, in a checkout at 1fb617a. The last three (`slug-ascii`, `slug-unicode`, `table`) ran with `chb` built at 467320b, in a checkout at 467320b.
- The two binaries are the same program: between f00f806 and 467320b, 16 files changed, all under `docs/evidence/`, `CHANGELOG.md` and `docs/specs/swarm.md`, no Go source. Nothing the design case reads from disk (`fixtures/`, `agents/`, `foragers/`, `workflows/`) changed between 1fb617a and 467320b.
- The commit hashes here and in the REPORT headers are the build identity the harness recorded. They name the development history, which the public repository does not carry.

## Dates and how the run was split

All times 2026-10-06, local (UTC−4; the REPORT.md headers carry UTC).

1. 16:00:15 to 16:06:27, the smoke case: `intervals`, both arms (`results-intervals.jsonl`, `REPORT-intervals.md`).
2. 16:06:27, one case over all twelve tasks (`design-twelve`). The operator's tooling that supervised the run, not chb, stops a background command after some minutes, and this case had completed both arms of `cache-lru` (6/6, 6/6) and of `cache-ttl` (designer 6/6, control 3/6) when it was killed; the design case writes `results.jsonl` only when the case ends, so a killed case leaves no rows. Those four runs are not in this directory; they are superseded by chunk 1.
3. 16:31:25 to 17:49:15, four cases of two tasks each, run one after another on the one GPU: chunk 1 `cache-lru`, `cache-ttl`; chunk 2 `kv-sections`, `kv-quoted`; chunk 3 `cli-wc`, `cli-head`; chunk 4 `ratelimit`, `retry`. Each wrote its rows (`results-<task>-<task>.jsonl`, `REPORT-<task>-<task>.md`).
4. 17:49:23, chunk 5 (`slug-ascii`, `slug-unicode`) was killed the same way. Its log shows it had completed both arms of `slug-ascii` (designer 6/6, plan names 1/1, audit false, 441 s; control 6/6, plan names 1/1, audit false, 211 s) and was inside `slug-unicode`. No rows.
5. 18:25:40, a one-task case for `slug-ascii` was killed after printing its header. No rows, no model output of note.
6. 19:14:46 onward, the last three tasks, one case per task, run from a checkout of 467320b by a supervising process that polled its own jobs: `slug-ascii` (19:14:46 to 19:40:39), `slug-unicode` (19:41:05 to 20:08:11), `table` (20:08:26 to 20:52:54) (`results-<task>.jsonl`, `REPORT-<task>.md`). The `slug-unicode` case's report carries one adherence warning, "no tracked file changed during the case — ?? docs/evidence/2026-10-06-bench-design/": this README, drafted into the checkout while that case ran. No run reads it, and the `table` case, during which nothing was written, carries no warning.

Splitting changes nothing about a task's measurement: a task's two arms run in sequence inside one case, in that case's own workspace, and the only inputs that cross cases are the fixtures and the binary, which were the same throughout. What splitting does change is wall time: the later cases ran slower (slug-ascii's recorded run took 885 s and 666 s against the killed chunk 5's 441 s and 211 s for the same task); the machine was carrying other work by then, which is the likely reason and not a measured one. Wall time is reported and is not in the rule; the executor's 10-minute wall is, and it ended one run (table's control).

The two killed attempts at `slug-ascii` give one repeat of a task under the same configuration, logged but not rowed: the designer passed 6/6 in chunk 5 and 5/6 in the recorded run, and its plan named the reference file in chunk 5 and not in the recorded run. The abandoned twelve-task case gives another: `cache-ttl`'s control passed 3/6 there and 0/6 in chunk 1. These are log lines, not rows, and the rule reads only rows; they are here because they show how much one run of one task can move.

## Files

- `results-*.jsonl`: the rows, one per task and arm, as the harness wrote them (`internal/design.Row`), renamed by the tasks they hold.
- `REPORT-*.md`: each case's harness report.
- `design-report.txt`: `chb design report` over all eight results files, the per-task table, the per-arm means and the verdict.
- `progress.log`: the launcher's log for the whole day, both halves.

Every local path in these files is replaced by `<scratch>`, `<repo>` or `<home>`.
