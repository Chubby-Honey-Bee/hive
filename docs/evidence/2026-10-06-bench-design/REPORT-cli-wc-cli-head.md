# Agent harness report

- generated: 2026-10-06T21:33:16Z
- chb: chb version dev
- git: 1fb617a
- suite: `<scratch>/design-live/chunk3.yaml` (sha256 13fe0a2e1d894b00)
- provider: `local` at `http://localhost:11434/v1`; routing profile `local-fast` (`chb preflight --profile local-fast` prints every role); swarm tier `--budget-mode cheap`; lens model `qwen3.6:35b-a3b-q4_K_M`; queen model `qwen3.6:35b-a3b-q4_K_M`; lens reasoning `none`; queen reasoning `none`; persona profile `full`; seed none; bench reps 1; template model `haiku`
- config: `profile local-fast`

What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.

A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.

**Adherence this run: 0/0 checks clean.**

| Case | Kind | Result | Duration | Cost | Artifact sha256 |
|---|---|---|---|---|---|
| design-chunk3 | design | PASS | 22m28s | chunk 3 of the twelve-task run, about 10 min a task on local-fast, $0 |  |

## design-chunk3

2 tasks, one row per task and arm in `<scratch>/design-live/ws-chunk3/design-chunk3/results.jsonl`, for `chb design report`.

| Task | Arm | Tests | Plan names ref. files | Steps | Deviations | Additions | File coverage | Claims | Guarantees w/ premises | Audit | Done | Wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| cli-head | designer | 3/5 | 1/1 | 5 | 0 | 0 | 0.67 | 14 | 0/2 | no | designed only | 608 | 518601 | 26085 |
| cli-head | control | 4/5 | 1/1 | 4 | 0 | 2 | 0.33 | 12 | 2/4 | no | yes | 190 | 182661 | 8018 |
| cli-wc | designer | 3/6 | 1/1 | 6 | 0 | 2 | 0.50 | 12 | 1/2 | no | yes | 342 | 125731 | 14291 |
| cli-wc | control | 2/6 | 1/1 | 3 | 0 | 1 | 0.33 | 13 | 1/1 | yes | yes | 206 | 152715 | 9848 |

| Arm | Rows | Completed | Pass rate | Plan completeness | Deviation rate | File coverage | Audit passed | Guarantees w/ premises | Mean wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|
| designer | 2 | 1 | 0.55 | 1.00 | 0.33 | 0.58 | 0 | 0.25 | 475 | 644332 | 40376 |
| control | 2 | 2 | 0.57 | 1.00 | 0.42 | 0.33 | 1 | 0.60 | 198 | 335376 | 17866 |

**Verdict: not-worth-it** (alpha 0.05; token cost designer ÷ control = 1.94×, executors included)
- hidden tests: below (designer won 1 and lost 1 of 2 non-tied tasks (0 tied), p = 0.750 that it is worse; 2 non-tied tasks can never reach alpha; mean difference -0.017)
- plan completeness: not-below (every one of 2 tasks tied; mean difference +0.000)
- plan fidelity (reported, not in the rule): not-below (every one of 1 tasks tied; mean difference +0.000; 1 task(s) dropped where an arm had no deviation rate)
- token cost: designer 684708 ÷ control 353242 = 1.94× (both arms with their executor)

- ✓ the tasks load — 2 tasks, 1 twin pairs
- ✓ the workflows the runs dispatch are written (chb ask --no-dispatch, the plan call, the executor, --profile)
- ✓ the endpoint serves every model the case sends (model preflight) — http://localhost:11434/v1 serves qwen3.6:35b-a3b-q4_K_M
- ✓ results written to results.jsonl
- ✓ every designer run left its tree unchanged
- ✓ no tracked file changed during the case (a concurrent edit counts too)
- ✓ a run completed (3 of 4)

logs: `<scratch>/design-live/ws-chunk3/design-chunk3`
