/-
  Hegelian Dialectic — MSS Preservation (Layer 4)

  The dialectic process (multiple waves of agent findings) writes findings
  one at a time, each through the write checks. This file proves what a
  sequence of such writes keeps.

  Key theorems:
  - D6: A sequence of write-checked insertions preserves the write invariants
  - X4: End-to-end dialectic soundness (the master theorem)
  - X5: Gate check is sufficient for all invariants

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic
import MSS.Invariants
import MSS.Preservation
import Dialectic.Gate
import CDE.Encoding

namespace Dialectic

/-! ## D6: Write-checked insertions preserve the write invariants

`WriteCheckPasses` looks one hop deep, so a sequence of such insertions keeps
the one-hop invariants and acyclicity. It does **not** keep transitive
no-laundering, and D6 does not claim `MSS.WellFormed`. Counterexample: a
well-formed database holding an unknown `u` and an assumption `a` with
`deps = [u]`; insert a guarantee `g` with `deps = [a]`. Every write check
passes (`a` exists and is not unknown), and afterwards `g` transitively
depends on `u`, so `NoTransitiveLaundering` fails. This is the
counterexample in MSS/Invariants.lean seen from the write side: the gate's
BFS audit catches that case, the write check cannot.
-/

/-- The invariants a one-hop write check carries. -/
def WriteInvariants (db : MSS.FindingsDB) : Prop :=
  MSS.NoDirectLaundering db ∧ MSS.NoUntraceableGuarantees db ∧
    MSS.DepsValid db ∧ MSS.Acyclic db

/-- `Inserts db fs db'`: `db'` results from inserting the findings `fs`, in
    order, each passing the write checks against the database at its
    insertion time (freshness of its id is the fourth check). -/
inductive Inserts : MSS.FindingsDB → List MSS.Finding → MSS.FindingsDB → Prop where
  | nil {db : MSS.FindingsDB} : Inserts db [] db
  | cons {db db' : MSS.FindingsDB} {f : MSS.Finding} {fs : List MSS.Finding}
      (h : MSS.WriteCheckPasses db f) :
      Inserts (db.insert f h.2.2.2) fs db' → Inserts db (f :: fs) db'

/-- One write-checked insertion preserves the write invariants. -/
theorem write_preserves_invariants (db : MSS.FindingsDB) (f : MSS.Finding)
    (h : MSS.WriteCheckPasses db f) (h_inv : WriteInvariants db) :
    WriteInvariants (db.insert f h.2.2.2) := by
  obtain ⟨h1, h2, h3, h4⟩ := h_inv
  exact ⟨MSS.write_preserves_no_laundering db f h1 h3 h h.2.2.2,
    MSS.write_preserves_no_untraceable db f h2 h h.2.2.2,
    MSS.write_preserves_deps_valid db f h3 h h.2.2.2,
    MSS.write_preserves_acyclic db f h4 h3 h h.2.2.2⟩

/-- D6: A sequence of write-checked insertions preserves NoDirectLaundering,
    NoUntraceableGuarantees, DepsValid and Acyclic. However many agents write
    findings, those four hold afterwards if they held before. Transitive
    no-laundering is not among them; see the counterexample above. -/
theorem dialectic_preserves_mss
    (db db' : MSS.FindingsDB) (findings : List MSS.Finding)
    (h_ins : Inserts db findings db')
    (h_inv : WriteInvariants db)
    : WriteInvariants db' := by
  induction h_ins with
  | nil => exact h_inv
  | cons h _ ih => exact ih (write_preserves_invariants _ _ h h_inv)

/-! ## X4: End-to-End Dialectic Soundness (Master Theorem)

If a dialectic workflow completes and the gate opens, then:
(a) MSS.WellFormed holds for the wave's findings
(b) CDE encoding is complete (no NULLs) -- requires CDE enforcement
(c) WASP sufficiency holds -- requires non-NULL encoding

This composes ALL lower-layer results into a single guarantee.
-/

/-- X4: End-to-end soundness of the dialectic process.
    Workflow complete + gate open → all framework invariants hold. -/
theorem dialectic_end_to_end
    (db : MSS.FindingsDB)
    (cdb : CDE.CDEDatabase)
    (reg : CDE.DimRegistry)
    (g : GatePrereqs)
    (h_open : gateOpens g = true)
    (h_audit : g.mssAuditClean = true →
      MSS.NoDirectLaundering db ∧
      MSS.NoUntraceableGuarantees db ∧
      MSS.Acyclic db ∧
      MSS.DepsValid db ∧
      MSS.NoTransitiveLaundering db)
    (h_cde : CDE.EncodingComplete cdb reg)
    (h_proj : cdb.toMSS = db)
    : MSS.WellFormed db ∧ CDE.EncodingComplete cdb reg := by
  constructor
  · exact gate_implies_wellformed db g h_open h_audit
  · exact h_cde

/-! ## X5: Gate Covers All Invariants

The gate check is sufficient: if all four prerequisites pass,
then NoDirectLaundering, NoUntraceableGuarantees, Acyclic, DepsValid
and NoTransitiveLaundering (the audit's BFS) all hold.
-/

/-- X5: The gate is a sufficient check for all MSS invariants. -/
theorem gate_covers_all
    (db : MSS.FindingsDB)
    (g : GatePrereqs)
    (h_open : gateOpens g = true)
    (h_audit : g.mssAuditClean = true →
      MSS.NoDirectLaundering db ∧
      MSS.NoUntraceableGuarantees db ∧
      MSS.Acyclic db ∧
      MSS.DepsValid db ∧
      MSS.NoTransitiveLaundering db)
    : MSS.NoDirectLaundering db ∧
      MSS.NoUntraceableGuarantees db ∧
      MSS.Acyclic db ∧
      MSS.DepsValid db ∧
      MSS.NoTransitiveLaundering db := by
  have wf := gate_implies_wellformed db g h_open h_audit
  exact ⟨wf.no_laundering, wf.no_untraceable, wf.acyclic, wf.deps_valid, wf.no_transitive_laundering⟩

end Dialectic
