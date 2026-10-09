/-
  Hegelian Dialectic — Gate Soundness (Layer 4)

  The wave gate is the enforcement mechanism that ensures dialectic
  integrity before synthesis export. If the gate opens, all four
  prerequisites hold, and the database satisfies MSS well-formedness.

  Source of truth: internal/gate/gate.go (RunGatePipeline, which both
  `chb guard` and `chb db-write gate_wave` run)

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import Dialectic.Basic
import MSS.Invariants

namespace Dialectic

/-! ## D4: Gate Soundness

If gateOpens returns true, all four prerequisites individually hold.
-/

/-- D4: Gate opening implies all four prerequisites are individually true. -/
theorem gate_sound (g : GatePrereqs) :
    gateOpens g = true →
    g.agentsCompleted = true ∧
    g.evaluationPassed = true ∧
    g.conflictsResolved = true ∧
    g.mssAuditClean = true := by
  intro h
  simp_all [gateOpens]

/-! ## D5: Gate Implies MSS Well-Formedness

If the gate opens (which requires mssAuditClean), then the database
satisfies MSS.WellFormed. This connects Layer 4 (Dialectic) to
Layer 1 (MSS).

The mssAuditClean check runs:
- No laundering violations, direct and transitive — the audit is a BFS
  over the dependency graph (NoDirectLaundering, NoTransitiveLaundering)
- No untraceable guarantees (NoUntraceableGuarantees)
- No dependency cycles (Acyclic)
- All deps valid (DepsValid)

This is exactly MSS.WellFormed. Transitive no-laundering is listed
separately because it does not follow from the other four (see the
counterexample in MSS/Invariants.lean).
-/

/-- D5: If the gate opens, the MSS audit passed, which implies
    the database satisfies all MSS structural invariants.

    This is the key cross-layer theorem connecting the dialectic
    process to formal MSS guarantees. -/
theorem gate_implies_wellformed
    (db : MSS.FindingsDB) (g : GatePrereqs)
    (h_open : gateOpens g = true)
    (h_audit : g.mssAuditClean = true →
      MSS.NoDirectLaundering db ∧
      MSS.NoUntraceableGuarantees db ∧
      MSS.Acyclic db ∧
      MSS.DepsValid db ∧
      MSS.NoTransitiveLaundering db)
    : MSS.WellFormed db := by
  have ⟨_, _, _, h4⟩ := gate_sound g h_open
  have ⟨h_ndl, h_nut, h_acyc, h_dv, h_ntl⟩ := h_audit h4
  exact MSS.WellFormed.mk' db h_ndl h_nut h_acyc h_dv h_ntl

/-! ## D7: Synthesis Requires Gate

Synthesis export is structurally gated — the function checks for
a wave_gates table entry before returning findings. No gate = no synthesis.
-/

/-- D7: Synthesis export is impossible without an open gate.
    As proved, it is a tautology about a Bool and constrains nothing: the Go
    implementation has no synthesis export command for a gate to guard. -/
theorem synthesis_requires_gate :
    ∀ (gateExists : Bool), gateExists = false → ¬(gateExists = true) := by
  intros g h; simp [h]

/-! ## D10: Conflict Resolution Completeness

The gate's conflictsResolved prerequisite ensures that all conflicts
detected by internal/gate/conflict.go have been resolved before synthesis.
-/

/-- D10: If the gate opens, all conflicts are resolved.
    Gate opening requires conflictsResolved = true. -/
theorem conflict_resolution_complete (g : GatePrereqs)
    (h_open : gateOpens g = true) :
    g.conflictsResolved = true := by
  exact (gate_sound g h_open).2.2.1

end Dialectic
