# Agent harness report

- generated: 2026-10-06T20:06:27Z
- chb: chb version dev
- git: 1fb617a
- suite: `<scratch>/design-live/smoke-suite.yaml` (sha256 4f1df5e24cd56d50)
- provider: `local` at `http://localhost:11434/v1`; routing profile `local-fast` (`chb preflight --profile local-fast` prints every role); swarm tier `--budget-mode cheap`; lens model `qwen3.6:35b-a3b-q4_K_M`; queen model `qwen3.6:35b-a3b-q4_K_M`; lens reasoning `none`; queen reasoning `none`; persona profile `full`; seed none; bench reps 1; template model `haiku`
- config: `profile local-fast`

What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.

A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.

**Adherence this run: 0/0 checks clean.**

| Case | Kind | Result | Duration | Cost | Artifact sha256 |
|---|---|---|---|---|---|
| design-smoke | design | PASS | 6m11s | one task, both arms, about 30 min on local-fast, $0 |  |

## design-smoke

1 tasks, one row per task and arm in `<scratch>/design-live/ws-smoke/design-smoke/results.jsonl`, for `chb design report`.

| Task | Arm | Tests | Plan names ref. files | Steps | Deviations | Additions | File coverage | Claims | Guarantees w/ premises | Audit | Done | Wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| intervals | designer | 6/6 | 1/1 | 2 | 0 | 1 | 1.00 | 7 | 1/1 | yes | yes | 266 | 64769 | 9822 |
| intervals | control | 6/6 | 1/1 | 3 | 0 | 1 | 1.00 | 9 | 2/4 | no | yes | 101 | 71366 | 4433 |

| Arm | Rows | Completed | Pass rate | Plan completeness | Deviation rate | File coverage | Audit passed | Guarantees w/ premises | Mean wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|
| designer | 1 | 1 | 1.00 | 1.00 | 0.50 | 1.00 | 1 | 1.00 | 266 | 64769 | 9822 |
| control | 1 | 1 | 1.00 | 1.00 | 0.33 | 1.00 | 0 | 0.50 | 101 | 71366 | 4433 |

**Verdict: too-few** (alpha 0.05; token cost designer ÷ control = 0.98×, executors included)
- 1 task(s) have both arms; the rule needs two
- hidden tests: not-below (every one of 1 tasks tied; mean difference +0.000)
- plan completeness: not-below (every one of 1 tasks tied; mean difference +0.000)
- plan fidelity (reported, not in the rule): below (designer won 0 and lost 1 of 1 non-tied tasks (0 tied), p = 0.500 that it is worse; 1 non-tied tasks can never reach alpha; mean difference -0.167)
- token cost: designer 74591 ÷ control 75799 = 0.98× (both arms with their executor)

- ✓ the tasks load — 1 tasks, 0 twin pairs
- ✓ the workflows the runs dispatch are written (chb ask --no-dispatch, the plan call, the executor, --profile)
- ✓ the endpoint serves every model the case sends (model preflight) — http://localhost:11434/v1 serves qwen3.6:35b-a3b-q4_K_M
- ✓ results written to results.jsonl
- ✓ every designer run left its tree unchanged
- ✓ no tracked file changed during the case (a concurrent edit counts too)
- ✓ a run completed (2 of 2)

logs: `<scratch>/design-live/ws-smoke/design-smoke`
