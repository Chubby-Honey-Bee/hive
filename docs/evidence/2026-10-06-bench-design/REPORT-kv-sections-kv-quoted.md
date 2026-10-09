# Agent harness report

- generated: 2026-10-06T21:10:18Z
- chb: chb version dev
- git: 1fb617a
- suite: `<scratch>/design-live/chunk2.yaml` (sha256 e02a3971ae692c65)
- provider: `local` at `http://localhost:11434/v1`; routing profile `local-fast` (`chb preflight --profile local-fast` prints every role); swarm tier `--budget-mode cheap`; lens model `qwen3.6:35b-a3b-q4_K_M`; queen model `qwen3.6:35b-a3b-q4_K_M`; lens reasoning `none`; queen reasoning `none`; persona profile `full`; seed none; bench reps 1; template model `haiku`
- config: `profile local-fast`

What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.

A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.

**Adherence this run: 0/0 checks clean.**

| Case | Kind | Result | Duration | Cost | Artifact sha256 |
|---|---|---|---|---|---|
| design-chunk2 | design | PASS | 21m39s | chunk 2 of the twelve-task run, about 10 min a task on local-fast, $0 |  |

## design-chunk2

2 tasks, one row per task and arm in `<scratch>/design-live/ws-chunk2/design-chunk2/results.jsonl`, for `chb design report`.

| Task | Arm | Tests | Plan names ref. files | Steps | Deviations | Additions | File coverage | Claims | Guarantees w/ premises | Audit | Done | Wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| kv-quoted | designer | 5/6 | 1/1 | 3 | 2 | 2 | 1.00 | 12 | 0/1 | no | yes | 576 | 421290 | 22024 |
| kv-quoted | control | 6/6 | 1/1 | 4 | 2 | 1 | 1.00 | 14 | 1/3 | no | yes | 252 | 272109 | 9918 |
| kv-sections | designer | 5/6 | 1/1 | 4 | 2 | 1 | 1.00 | 13 | 3/5 | no | yes | 314 | 119279 | 12756 |
| kv-sections | control | 4/6 | 1/1 | 1 | 1 | 2 | 0.25 | 13 | 5/6 | no | yes | 155 | 126764 | 6652 |

| Arm | Rows | Completed | Pass rate | Plan completeness | Deviation rate | File coverage | Audit passed | Guarantees w/ premises | Mean wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|
| designer | 2 | 2 | 0.83 | 1.00 | 1.04 | 1.00 | 0 | 0.50 | 445 | 540569 | 34780 |
| control | 2 | 2 | 0.83 | 1.00 | 1.88 | 0.62 | 0 | 0.67 | 204 | 398873 | 16570 |

**Verdict: worth-it** (alpha 0.05; token cost designer ÷ control = 1.38×, executors included)
- hidden tests: not-below (designer won 1 and lost 1 of 2 non-tied tasks (0 tied), p = 0.750 that it is worse; 2 non-tied tasks can never reach alpha; mean difference +0.000)
- plan completeness: not-below (every one of 2 tasks tied; mean difference +0.000)
- plan fidelity (reported, not in the rule): not-below (designer won 1 and lost 1 of 2 non-tied tasks (0 tied), p = 0.750 that it is worse; 2 non-tied tasks can never reach alpha; mean difference +0.833)
- token cost: designer 575349 ÷ control 415443 = 1.38× (both arms with their executor)

- ✓ the tasks load — 2 tasks, 1 twin pairs
- ✓ the workflows the runs dispatch are written (chb ask --no-dispatch, the plan call, the executor, --profile)
- ✓ the endpoint serves every model the case sends (model preflight) — http://localhost:11434/v1 serves qwen3.6:35b-a3b-q4_K_M
- ✓ results written to results.jsonl
- ✓ every designer run left its tree unchanged
- ✓ no tracked file changed during the case (a concurrent edit counts too)
- ✓ a run completed (4 of 4)

logs: `<scratch>/design-live/ws-chunk2/design-chunk2`
