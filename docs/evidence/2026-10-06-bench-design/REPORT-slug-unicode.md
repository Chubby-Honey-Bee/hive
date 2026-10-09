# Agent harness report

- generated: 2026-10-07T00:08:11Z
- chb: chb version dev
- git: 467320b
- suite: `<scratch>/dbr/one-slug-unicode.yaml` (sha256 1daa2f1a9f17ac8f)
- provider: `local` at `http://localhost:11434/v1`; routing profile `local-fast` (`chb preflight --profile local-fast` prints every role); swarm tier `--budget-mode cheap`; lens model `qwen3.6:35b-a3b-q4_K_M`; queen model `qwen3.6:35b-a3b-q4_K_M`; lens reasoning `none`; queen reasoning `none`; persona profile `full`; seed none; bench reps 1; template model `haiku`
- config: `profile local-fast`

What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.

A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.

**Adherence this run: 0/0 checks clean.**

| Case | Kind | Result | Duration | Cost | Artifact sha256 |
|---|---|---|---|---|---|
| design-slug-unicode | design | PASS | 27m6s | one task, both arms, about 10 min on local-fast, $0 |  |

## design-slug-unicode

1 tasks, one row per task and arm in `<scratch>/dbr/ws-slug-unicode/design-slug-unicode/results.jsonl`, for `chb design report`.

| Task | Arm | Tests | Plan names ref. files | Steps | Deviations | Additions | File coverage | Claims | Guarantees w/ premises | Audit | Done | Wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| slug-unicode | designer | 6/6 | 1/1 | 7 | 2 | 2 | 1.00 | 6 | 0/0 | yes | yes | 1335 | 170313 | 13122 |
| slug-unicode | control | 6/6 | 1/1 | 5 | 2 | 2 | 1.00 | 2 | 0/1 | no | yes | 289 | 51300 | 3130 |

| Arm | Rows | Completed | Pass rate | Plan completeness | Deviation rate | File coverage | Audit passed | Guarantees w/ premises | Mean wall s | Tokens in | Tokens out |
|---|---|---|---|---|---|---|---|---|---|---|---|
| designer | 1 | 1 | 1.00 | 1.00 | 0.57 | 1.00 | 1 | 0.00 | 1335 | 170313 | 13122 |
| control | 1 | 1 | 1.00 | 1.00 | 0.80 | 1.00 | 0 | 0.00 | 289 | 51300 | 3130 |

**Verdict: too-few** (alpha 0.05; token cost designer ÷ control = 3.37×, executors included)
- 1 task(s) have both arms; the rule needs two
- hidden tests: not-below (every one of 1 tasks tied; mean difference +0.000)
- plan completeness: not-below (every one of 1 tasks tied; mean difference +0.000)
- plan fidelity (reported, not in the rule): not-below (designer won 1 and lost 0 of 1 non-tied tasks (0 tied), p = 1.000 that it is worse; 1 non-tied tasks can never reach alpha; mean difference +0.229)
- token cost: designer 183435 ÷ control 54430 = 3.37× (both arms with their executor)

- ✓ the tasks load — 1 tasks, 0 twin pairs
- ✓ the workflows the runs dispatch are written (chb ask --no-dispatch, the plan call, the executor, --profile)
- ✓ the endpoint serves every model the case sends (model preflight) — http://localhost:11434/v1 serves qwen3.6:35b-a3b-q4_K_M
- ✓ results written to results.jsonl
- ✓ every designer run left its tree unchanged
- ⚠ no tracked file changed during the case (a concurrent edit counts too) — ?? docs/evidence/2026-10-06-bench-design/
- ✓ a run completed (2 of 2)

logs: `<scratch>/dbr/ws-slug-unicode/design-slug-unicode`
