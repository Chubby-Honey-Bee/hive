/-
  MSS-Fitch Bridge

  What this file proves, within the MSS model:
  - MSS labels form a partial order (epistemic strength)
  - WriteCheckPasses, the model of the guarantee write check, yields its
    three constraints: non-empty deps, every dep exists, no dep is unknown

  What it does not do: it imports nothing from the Fitch library, so the
  reading of the write check as a Fitch reduction is an analogy argued in
  the comments below, not a theorem connecting the two developments.

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic
import MSS.Invariants
import MSS.Preservation  -- for WriteCheckPasses, used by mss_write_is_fitch

namespace MSS

/-! ## MSS Label Partial Order

Epistemic strength ordering: definition is strongest (we chose it),
guarantee is next (provable), assumption is weaker (could be wrong),
unknown is weakest (honest gap).

definition > guarantee > assumption > unknown
-/

/-- Epistemic strength: lower number = stronger label. -/
def MSSLabel.strength : MSSLabel → Nat
  | .definition => 0
  | .guarantee  => 1
  | .assumption => 2
  | .unknown    => 3

/-- M11: MSS labels have a total order by epistemic strength.
    definition < guarantee < assumption < unknown (stronger = lower). -/
theorem mss_label_strength_total (l1 l2 : MSSLabel) :
    l1.strength ≤ l2.strength ∨ l2.strength ≤ l1.strength := by
  cases l1 <;> cases l2 <;> decide

/-- Distinct labels have distinct strengths. -/
theorem mss_label_strength_injective (l1 l2 : MSSLabel) :
    l1.strength = l2.strength → l1 = l2 := by
  cases l1 <;> cases l2 <;> simp [MSSLabel.strength]

/-! ## MSS Write-Check as Fitch Reduction

The guarantee write check (the Go write path in internal/db; modelled here
by WriteCheckPasses) enforces three constraints:
1. deps must be non-empty (guarantee needs a derivation)
2. all dep IDs must exist (valid references)
3. no dep may be labeled unknown (no laundering)

This is structurally a Fitch reduction: the "rule" is "guarantee insertion"
and the "constraints" are the three checks above.

Only the MSS side is formalized below; the Fitch side of the analogy is not
stated in Lean.
-/

/-- A finding is "locally grounded" if it is not labeled unknown.
    The full *transitive* grounding (every dependency, recursively, is
    non-unknown) is exactly `NoTransitiveLaundering` restricted to this
    id — a recursive-over-the-dependency-graph notion that does not admit
    a structural `def` (the recursion follows edges and may cycle) and
    trips the kernel's nested-inductive restriction through `∧`/`∀`. Use
    `NoTransitiveLaundering` (Invariants.lean) for the transitive property;
    this local form is all the Fitch-reduction theorem below needs. -/
def EpistemicallyGrounded (db : FindingsDB) (id : Nat) : Prop :=
  ∀ f : Finding, db.get id = some f → f.label ≠ MSSLabel.unknown

/-- M10: a guarantee that passes WriteCheckPasses (Preservation.lean) has a
    non-empty list of existing, non-unknown dependencies — the constraints
    read as a Fitch reduction's:
    - MinCount(1) on deps
    - All deps exist (valid references)
    - All deps are non-unknown (no laundering)

    One direction only, and close to a restatement of the hypothesis. The
    converse, and any link to NoDirectLaundering's preservation, are not
    proved here. -/
theorem mss_write_is_fitch
    (db : FindingsDB) (f : Finding)
    (h_check : WriteCheckPasses db f)
    : f.label = MSSLabel.guarantee →
      f.deps ≠ [] ∧
      (∀ depId ∈ f.deps, depId ∈ db.ids) ∧
      (∀ depId ∈ f.deps, ∀ dep : Finding,
        db.get depId = some dep → dep.label ≠ MSSLabel.unknown) := by
  intro h_guar
  exact ⟨h_check.1 h_guar, h_check.2.1, h_check.2.2.1 h_guar⟩

end MSS
