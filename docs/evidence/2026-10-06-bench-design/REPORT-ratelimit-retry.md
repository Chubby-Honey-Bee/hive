# Agent harness report

- generated: 2026-10-06T21:49:15Z
- chb: chb version dev
- git: 1fb617a
- suite: `<scratch>/design-live/chunk4.yaml` (sha256 b2d39ee354b5a4aa)
- provider: `local` at `http://localhost:11434/v1`; routing profile `local-fast` (`chb preflight --profile local-fast` prints every role); swarm tier `--budget-mode cheap`; lens model `qwen3.6:35b-a3b-q4_K_M`; queen model `qwen3.6:35b-a3b-q4_K_M`; lens reasoning `none`; queen reasoning `none`; persona profile `full`; seed none; bench reps 1; template model `haiku`
- config: `profile local-fast`

What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.

A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.

**Adherence this run: 0/0 checks clean.**

| Case | Kind | Result | Duration | Cost | Artifact sha256 |
|---|---|---|---|---|---|
| design-chunk4 | design | PASS | 15m49s | chunk 4 of the twelve-task run, about 10 min a task on local-fast, $0 |  |

## design-chunk4

2 tasks, one row per task and arm in `<scratch>/design-live/ws-chunk4/design-chunk4/results.jsonl`, for `chb design report`.

| Task | Arm | Tests | Plan names ref. files | Steps | Deviations | Additions | File coverage | Claims | Guarantees w/ premises | Audit | Done | Wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ratelimit | designer | 5/6 | 1/1 | 7 | 0 | 1 | 0.50 | 11 | 4/4 | yes | yes | 346 | 187195 | 14249 |
| ratelimit | control | 2/6 | 1/1 | 4 | 0 | 1 | 1.00 | 7 | 1/2 | no | yes | 135 | 86386 | 5892 |
| retry | designer | 6/7 | 1/1 | 5 | 0 | 2 | 0.50 | 11 | 6/6 | yes | yes | 327 | 141443 | 12815 |
| retry | control | 7/7 | 1/1 | 7 | 1 | 1 | 1.00 | 8 | 0/2 | no | yes | 139 | 152660 | 6390 |

| Arm | Rows | Completed | Pass rate | Plan completeness | Deviation rate | File coverage | Audit passed | Guarantees w/ premises | Mean wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|
| designer | 2 | 2 | 0.85 | 1.00 | 0.27 | 0.50 | 2 | 1.00 | 337 | 328638 | 27064 |
| control | 2 | 2 | 0.67 | 1.00 | 0.27 | 1.00 | 0 | 0.25 | 137 | 239046 | 12282 |

**Verdict: worth-it** (alpha 0.05; token cost designer ÷ control = 1.42×, executors included)
- hidden tests: not-below (designer won 1 and lost 1 of 2 non-tied tasks (0 tied), p = 0.750 that it is worse; 2 non-tied tasks can never reach alpha; mean difference +0.179)
- plan completeness: not-below (every one of 2 tasks tied; mean difference +0.000)
- plan fidelity (reported, not in the rule): below (designer won 1 and lost 1 of 2 non-tied tasks (0 tied), p = 0.750 that it is worse; 2 non-tied tasks can never reach alpha; mean difference -0.004)
- token cost: designer 355702 ÷ control 251328 = 1.42× (both arms with their executor)

- ✓ the tasks load — 2 tasks, 0 twin pairs
- ✓ the workflows the runs dispatch are written (chb ask --no-dispatch, the plan call, the executor, --profile)
- ✓ the endpoint serves every model the case sends (model preflight) — http://localhost:11434/v1 serves qwen3.6:35b-a3b-q4_K_M
- ✓ results written to results.jsonl
- ✓ every designer run left its tree unchanged
- ✓ no tracked file changed during the case (a concurrent edit counts too)
- ✓ a run completed (4 of 4)

logs: `<scratch>/design-live/ws-chunk4/design-chunk4`
