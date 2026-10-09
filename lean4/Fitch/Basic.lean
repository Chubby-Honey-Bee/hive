/-
  Fitch Reduction — Core Types (Layer 0)

  Formal encoding of the Fitch Natural Deduction checker from Chronoengine.
  Source of truth: chronoengine/crates/chronoengine_mud/src/composition/

  This is the logical foundation of the framework. All other layers build on
  the principle that a conclusion follows from its premises iff ALL premises hold.

  Types mirror the Rust implementation exactly:
  - Dimensions: 5D CDE vector (form/density/surface/energy/information), [0, 1000]
  - CompositionConstraint: premises in the Fitch proof
  - Item: carries dimensions + stability flag
  - CompositionRule: named collection of constraints

  Values are 10x quantized (Nat, not Float) to keep everything decidable.
  Rust f32 75.0 → Lean Nat 750. This matches the MSS signature quantization.

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/

namespace Fitch

/-! ## Dimensions: 5D CDE Vector

Each dimension is a natural number in [0, 1000] (representing 0.0-100.0 at 10x).
Index mapping (matches Rust `Dimensions::get`):
  0 -> form, 1 -> density, 2 -> surface, 3 -> energy, 4 -> information
-/

/-- 5D CDE dimensional vector. Values are 10x quantized: Rust 75.0 -> Lean 750. -/
structure Dimensions where
  form : Nat
  density : Nat
  surface : Nat
  energy : Nat
  information : Nat
  deriving DecidableEq, Repr

/-- Index-based accessor matching Rust `Dimensions::get(index)`.
    Returns 0 for out-of-range indices (Rust panics; Lean must be total). -/
def Dimensions.get (d : Dimensions) (index : Nat) : Nat :=
  match index with
  | 0 => d.form
  | 1 => d.density
  | 2 => d.surface
  | 3 => d.energy
  | 4 => d.information
  | _ => 0

/-- Sum of all 5 dimensions. -/
def Dimensions.sum (d : Dimensions) : Nat :=
  d.form + d.density + d.surface + d.energy + d.information

/-! ## Items

An item carries dimensions and a stability flag.
Matches Rust `Item` (omitting uuid and name, which are irrelevant to verification).
-/

/-- An item with 5D dimensions and stability. -/
structure Item where
  dims : Dimensions
  stable : Bool
  deriving DecidableEq, Repr

/-! ## Composition Constraints (Fitch Premises)

Each constraint is a premise in the Fitch proof.
Matches Rust `CompositionConstraint` enum exactly.
-/

/-- A constraint (premise) that items must satisfy for composition.
    Mirrors Rust `CompositionConstraint` with 5 variants. -/
inductive CompositionConstraint where
  /-- Must have at least `n` items. -/
  | minCount (n : Nat)
  /-- Must have at most `n` items. -/
  | maxCount (n : Nat)
  /-- All items must have dimension `dim` in `[min, max]` (10x quantized). -/
  | dimensionRange (dim : Nat) (min max : Nat)
  /-- All items must be stable (not corrupted). -/
  | stabilityRequired
  /-- Sum of all dimensions across all items must be >= `min`. -/
  | minDimensionSum (min : Nat)
  deriving DecidableEq, Repr

/-! ## Composition Rules

A rule is a named collection of constraints.
Matches Rust `CompositionRule`.
-/

/-- A composition rule: a named set of constraints (Fitch premises). -/
structure CompositionRule where
  name : String
  constraints : List CompositionConstraint
  deriving Repr

end Fitch
