# Agent harness report

- generated: 2026-10-06T20:48:30Z
- chb: chb version dev
- git: 1fb617a
- suite: `<scratch>/design-live/chunk1.yaml` (sha256 1748d2950c7fbf48)
- provider: `local` at `http://localhost:11434/v1`; routing profile `local-fast` (`chb preflight --profile local-fast` prints every role); swarm tier `--budget-mode cheap`; lens model `qwen3.6:35b-a3b-q4_K_M`; queen model `qwen3.6:35b-a3b-q4_K_M`; lens reasoning `none`; queen reasoning `none`; persona profile `full`; seed none; bench reps 1; template model `haiku`
- config: `profile local-fast`

What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.

A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.

**Adherence this run: 0/0 checks clean.**

| Case | Kind | Result | Duration | Cost | Artifact sha256 |
|---|---|---|---|---|---|
| design-chunk1 | design | PASS | 17m5s | chunk 1 of the twelve-task run, about 10 min a task on local-fast, $0 |  |

## design-chunk1

2 tasks, one row per task and arm in `<scratch>/design-live/ws-chunk1/design-chunk1/results.jsonl`, for `chb design report`.

| Task | Arm | Tests | Plan names ref. files | Steps | Deviations | Additions | File coverage | Claims | Guarantees w/ premises | Audit | Done | Wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| cache-lru | designer | 6/6 | 1/1 | 7 | 0 | 1 | 0.50 | 9 | 1/3 | no | yes | 307 | 100305 | 12038 |
| cache-lru | control | 6/6 | 1/1 | 7 | 1 | 1 | 1.00 | 11 | 5/5 | yes | yes | 183 | 97239 | 8729 |
| cache-ttl | designer | 6/6 | 1/1 | 3 | 2 | 2 | 0.50 | 10 | 2/2 | yes | yes | 403 | 216769 | 16871 |
| cache-ttl | control | 0/6 | 1/1 | 3 | 0 | 0 | 1.00 | 10 | 6/6 | yes | designed only | 130 | 101443 | 5288 |

| Arm | Rows | Completed | Pass rate | Plan completeness | Deviation rate | File coverage | Audit passed | Guarantees w/ premises | Mean wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|
| designer | 2 | 2 | 1.00 | 1.00 | 0.74 | 0.50 | 1 | 0.60 | 355 | 317074 | 28909 |
| control | 2 | 1 | 0.50 | 1.00 | 0.29 | 1.00 | 2 | 1.00 | 157 | 198682 | 14017 |

**Verdict: worth-it** (alpha 0.05; token cost designer ÷ control = 1.63×, executors included)
- hidden tests: not-below (designer won 1 and lost 0 of 1 non-tied tasks (1 tied), p = 1.000 that it is worse; 1 non-tied tasks can never reach alpha; mean difference +0.500)
- plan completeness: not-below (every one of 2 tasks tied; mean difference +0.000)
- plan fidelity (reported, not in the rule): not-below (designer won 1 and lost 0 of 1 non-tied tasks (0 tied), p = 1.000 that it is worse; 1 non-tied tasks can never reach alpha; mean difference +0.143; 1 task(s) dropped where an arm had no deviation rate)
- token cost: designer 345983 ÷ control 212699 = 1.63× (both arms with their executor)

- ✓ the tasks load — 2 tasks, 1 twin pairs
- ✓ the workflows the runs dispatch are written (chb ask --no-dispatch, the plan call, the executor, --profile)
- ✓ the endpoint serves every model the case sends (model preflight) — http://localhost:11434/v1 serves qwen3.6:35b-a3b-q4_K_M
- ✓ results written to results.jsonl
- ✓ every designer run left its tree unchanged
- ✓ no tracked file changed during the case (a concurrent edit counts too)
- ✓ a run completed (3 of 4)

logs: `<scratch>/design-live/ws-chunk1/design-chunk1`
