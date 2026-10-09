/-
  Fitch Reduction — Core Algorithm (Layer 0)

  The `fitch_reduction` function: conjunction of all constraints.
  Mirrors Rust `FitchChecker::fitch_reduction` and `verify_constraint` exactly.

  WASP properties of this encoding:
  - Total: all pattern matches are exhaustive (Lean guarantees this)
  - Deterministic: same inputs -> same output (no IO, no randomness)
  - Decidable: returns Bool, not Prop

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import Fitch.Basic

namespace Fitch

/-! ## Constraint Satisfaction

`satisfies items c` returns `true` iff items satisfy constraint `c`.
Mirrors Rust `FitchChecker::verify_constraint` match arms.
-/

/-- Evaluate a single constraint against a list of items.
    Total, decidable, deterministic. Mirrors Rust `verify_constraint`. -/
def satisfies (items : List Item) (c : CompositionConstraint) : Bool :=
  match c with
  | .minCount n => items.length >= n
  | .maxCount n => items.length <= n
  | .dimensionRange dim min max =>
      items.all fun item =>
        let v := item.dims.get dim
        min <= v && v <= max
  | .stabilityRequired =>
      items.all fun item => item.stable
  | .minDimensionSum min =>
      let total := items.foldl (fun acc item => acc + item.dims.sum) 0
      total >= min

/-! ## Fitch Reduction

The core function: returns `true` iff ALL constraints in the rule are satisfied.
This is the conjunction semantics of Fitch Natural Deduction.

Mirrors Rust `FitchChecker::fitch_reduction`.
-/

/-- Fitch reduction: conjunction of all constraints in the rule.
    Returns `true` iff every constraint (premise) is satisfied by the items. -/
def fitch_reduction (items : List Item) (rule : CompositionRule) : Bool :=
  rule.constraints.all (satisfies items)

/-! ## Definitional Theorem

`fitch_reduction` IS `List.all` over constraints — this is definitional,
not a proof obligation. But we state it explicitly for documentation.
-/

/-- F1: `fitch_reduction` is the conjunction of `satisfies` over all constraints.
    This is true by definition. -/
theorem fitch_is_conjunction (items : List Item) (rule : CompositionRule) :
    fitch_reduction items rule = rule.constraints.all (satisfies items) := by
  rfl

end Fitch
