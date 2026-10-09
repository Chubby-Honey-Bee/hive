# Design principles

What HIVE optimizes for, and the test a proposed feature has to pass. Revise this
when the principle changes, not when a feature ships.

## North star: verifiable convergence over persistent belief

The wrong target is **agent count**. Fan-out is a property of the harness, not a
differentiator. The system's constants are set on that bet — `ConvergenceMax = 10` and
`BatchSizeMax = 20`, roughly $1 of Sonnet per wave
([internal/hive/state.go](../internal/hive/state.go)) — but they are declared
definitions, not measurements: the comment's "an 11th corroborating finding has been
below noise" is an unrecorded observation. The bet is that ten lenses that iterate twice
beat eighty that vote once.

The right target:

> Run N foragers, converge by quorum, and emit a verdict you can **audit** and
> **time-travel**, that **never launders an unknown into a guarantee**.

A stateless swarm produces a pile of text with no memory and no integrity guarantee.
HIVE produces a typed, audited, time-addressable belief state. That is the claim to
lead with — not headcount.

Two parts of that sentence are load-bearing. State them at the strength the code supports:

- **Never launders an unknown into a guarantee.** Enforced at write time and by the
  gate's transitive audit. The decidability of the **one-hop** invariants and of
  acyclicity is proved in Lean 4 (`lean4/MSS/Decidability.lean`): real `Decidable`
  instances that `by decide` evaluates on an extracted database. The write checks are
  proved to preserve those four (`lean4/MSS/Preservation.lean`, D6). The transitive walk
  the gate actually runs is enforced in code and test-covered; Lean states no
  decidability for it, and records why it is independent of the one-hop checks
  (guarantee → assumption → unknown passes them and launders transitively). CI runs
  `lake build` across all five libraries and a gate that fails on any `sorry` and on
  any theorem concluding `True` ([.github/workflows/lean.yml](../.github/workflows/lean.yml));
  no obligation is open.
  Say "the one-hop checks and acyclicity are proved decidable, and the write checks
  are proved to preserve them," not "no-laundering is proved."
- **Time-travel.** `agent-run`, `gate_wave` and `ripen` each open a Time Wheel tick,
  and every Comb revision written inside one is anchored to it, so `comb at --tick` and
  `comb wheel` resolve. The persistence claim covers ticks too.

**Measured 2026-10-01 (Bench-1, four seeds, closed roster questions, local models).** No swarm of a local model answered more accurately than one call of that model (qwen3.6:35b: 0.82 against 0.95). The quorum's own signal is what the swarm adds: on that model, every high-convergence verdict was right (26 of 26) and the errors sat in ties and low convergence; on 4b and 8b lenses the signal is flat because they err together. So the north star above is also what the evidence supports: lead with calibration and the audit trail, never with accuracy against a single call.

**Measured 2026-10-06 (the executability benchmark's first run: twelve Go tasks, qwen3.6:35b in every role, one run per task and arm).** The pre-registered rule returned `not-worth-it` at 1.68× the control's tokens. Hidden tests were `not-below`: the designer won 4 tasks, lost 5 and tied 3 (p = 0.500; mean +0.076, and negative without the one task where the control's executor died on a server error). Plan completeness was `below` on one path-prefix mismatch among eleven ties. The audit passed 6 of 12 designer designs against 5 of 12 control designs. So on these tasks the swarm's research did not make a design and plan that a plain executor carried out better than one call's, and did not make the labels more honest; the table, the reading and the notes for the next run are in [`docs/specs/bench-design.md`](specs/bench-design.md) which holds the rule; the rows and the reading are in [`docs/evidence/2026-10-06-bench-design/`](evidence/2026-10-06-bench-design/README.md).

## Current priorities

This is the ordering.

1. `mcp-2026-07-28` — the move to the current, stateless MCP revision: `initialize`
   removed, the protocol version carried in `_meta` on every request, `server/discover`
   mandatory. A protocol-shape change, not a version bump. `chb-mcp` serves the earlier
   revisions it declares (negotiation over 2025-06-18 / 2024-11-05, `ping`, id
   semantics), and serves requests concurrently: a `notifications/cancelled` cancels the
   request's context, kills any subprocess it is waiting on, and suppresses the
   response. This is the next step, not a prerequisite for release.
2. Coverage rounds until convergence. `chb ask` runs one round by default (`--no-eval`
   turns it off): `swarm-evaluate` scores coverage and names gaps, `swarm-followup`
   sends the first three (`--followups`) to fresh lenses, and Queen synthesizes once
   with those findings and the other gaps by name. Scoping is opt-in (`--scope`). What
   is open is the loop: evaluate again after the follow-ups and keep going until
   coverage stops improving.
3. A decidability instance for `NoTransitiveLaundering`. Every other Lean obligation is
   proved (acyclicity decidability, the write and update preservation theorems, CDE
   index correctness, WASP sufficiency and bounded work over the multi-dimensional
   probe set), no theorem concludes `True`, and the Lean CI job fails on a `sorry` or a
   `True` conclusion.
4. `calibration-outcome-loop` — close
   the loop from "what the hive believed" to "was it right."

## The test a feature has to pass

Does it make the convergence **more verifiable**, or the belief **more persistent and
auditable**? If yes, it deepens what the system is for. If it only adds agents, lenses,
or proof *statements*, skip it.
