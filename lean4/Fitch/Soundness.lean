/-
  Fitch Reduction — Soundness and Completeness (Layer 0)

  Key theorems:
  - Soundness: if `fitch_reduction` returns `true`, every constraint holds individually
  - Completeness: if every constraint holds individually, `fitch_reduction` returns `true`
  - Individual constraint correctness: each constraint variant is faithful to its semantics

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import Fitch.Basic
import Fitch.Reduction

namespace Fitch

/-! ## Soundness

If Fitch reduction passes, every individual premise holds.
This is the direction: fitch_reduction = true -> forall c in constraints, satisfies c = true
-/

/-- F2: Soundness — if `fitch_reduction` returns `true`, then every individual
    constraint in the rule is satisfied. -/
theorem fitch_sound (items : List Item) (rule : CompositionRule) :
    fitch_reduction items rule = true →
    ∀ c ∈ rule.constraints, satisfies items c = true := by
  intro h c hc
  simp [fitch_reduction] at h
  exact h c hc

/-! ## Completeness

If every individual premise holds, Fitch reduction passes.
This is the direction: (forall c, satisfies c = true) -> fitch_reduction = true
-/

/-- F3: Completeness — if every constraint is individually satisfied,
    then `fitch_reduction` returns `true`. -/
theorem fitch_complete (items : List Item) (rule : CompositionRule) :
    (∀ c ∈ rule.constraints, satisfies items c = true) →
    fitch_reduction items rule = true := by
  intro h
  simp [fitch_reduction]
  exact h

/-! ## Individual Constraint Correctness

Each constraint variant faithfully encodes its intended semantics.
-/

/-- F5: MinCount is correct: it checks `items.length >= n`. -/
theorem satisfies_mincount (items : List Item) (n : Nat) :
    satisfies items (.minCount n) = decide (items.length >= n) := by
  simp [satisfies]

/-- F6: MaxCount is correct: it checks `items.length <= n`. -/
theorem satisfies_maxcount (items : List Item) (n : Nat) :
    satisfies items (.maxCount n) = decide (items.length <= n) := by
  simp [satisfies]

/-- F8: StabilityRequired is correct: it checks all items are stable. -/
theorem satisfies_stability (items : List Item) :
    satisfies items .stabilityRequired = items.all (fun i => i.stable) := by
  rfl

/-- F7: DimensionRange checks all items have dimension `dim` in `[min, max]`. -/
theorem satisfies_dimrange (items : List Item) (dim min max : Nat) :
    satisfies items (.dimensionRange dim min max) =
    items.all (fun item => min <= item.dims.get dim && item.dims.get dim <= max) := by
  rfl

/-- F10: MSS signature (Dimensions -> Nat hash) is deterministic.
    Same dimensions always produce the same result. -/
theorem mss_sig_deterministic (d1 d2 : Dimensions) (h : d1 = d2) :
    d1.sum = d2.sum := by
  subst h; rfl

end Fitch
