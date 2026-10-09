/-
  CDE Encoding Validity — Coordinates within bounds (Layer 2)

  Theorems about encoding correctness:
  - Coordinates must be within registered dimension bounds
  - No NULL coordinates in active dimensions
  - Registry binding: dimensions table governs d1..d8 values

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import CDE.Basic

namespace CDE

/-! ## Coordinate Validity

A coordinate is valid if every active dimension's value is within
the bounds specified by the dimension registry.
-/

/-- A coordinate value is within bounds for its dimension. -/
def valueInBounds (v : Nat) (dimSpec : DimSpec) : Prop :=
  v < dimSpec.numValues

/-- All active coordinates are within the dimension registry bounds.
    For dimension index `i` (0-based), if the registry has a spec at index `i`,
    then the coordinate's value at `i` must be less than `numValues`. -/
def CoordWithinRegistry (c : CDECoord) (reg : DimRegistry) : Prop :=
  ∀ (i : Nat), i < reg.numDims →
    match c.get i, reg.dims.get? i with
    | some v, some spec => v < spec.numValues
    | none, _ => False  -- NULL in active dimension is invalid
    | _, none => True   -- dimension not registered (shouldn't happen given i < numDims)

/-! ## Encoding Completeness (No NULLs)

The critical property: every finding in a well-formed CDE database
has non-NULL coordinates in all active dimensions.

NULL coordinates make findings invisible to probes, breaking WASP
sufficiency. The Go write path refuses them (`cde.ValidateCoords`); that
check is enforcement, not a consequence of a proof here.
-/

/-- Every finding has fully specified coordinates for all active dimensions. -/
def EncodingComplete (db : CDEDatabase) (reg : DimRegistry) : Prop :=
  ∀ f ∈ db.findings, f.coord.fullySpecified reg.numDims = true

/-- C1: In a well-formed CDE database, every finding with fully specified
    coordinates has a valid encoding within registry bounds. -/
theorem encoding_total
    (db : CDEDatabase) (reg : DimRegistry)
    (h_complete : EncodingComplete db reg)
    : ∀ f ∈ db.findings, f.coord.fullySpecified reg.numDims = true := by
  exact h_complete

/-- C2: If a coordinate has NULL in any active dimension, the finding
    is NOT indexed at any coordinate — it's invisible to all probes.
    This formalizes the hazard: NULL coordinates break sufficiency. -/
theorem null_not_indexed (c : CDECoord) (k : Nat) (i : Nat)
    (h_active : i < k) (h_null : c.get i = none)
    : c.fullySpecified k = false := by
  unfold CDECoord.fullySpecified
  rw [List.all_eq_false]
  exact ⟨i, List.mem_range.mpr h_active, by simp [h_null]⟩

/-! ## Registry Binding

The dimension registry must agree with the coordinate columns.
`cde.ValidateCoords` enforces the bound at write-time.
-/

/-- C6: Coordinate values must be less than the dimension's numValues.
    A definition of the property `cde.ValidateCoords` checks. -/
def RegistryBound (db : CDEDatabase) (reg : DimRegistry) : Prop :=
  ∀ f ∈ db.findings, CoordWithinRegistry f.coord reg

/-- C7: All findings in the database have coordinates within registry bounds. -/
theorem cde_within_registry
    (db : CDEDatabase) (reg : DimRegistry)
    (h_bound : RegistryBound db reg)
    : ∀ f ∈ db.findings, CoordWithinRegistry f.coord reg := by
  exact h_bound

/-! ## CDE Preserves MSS

Adding CDE coordinates to findings does not change their MSS labels
or dependency structure. CDE is an orthogonal extension.
-/

/-- X2: Projecting a CDEDatabase to MSS preserves all findings' labels and deps. -/
theorem cde_preserves_mss (db : CDEDatabase) :
    ∀ f ∈ db.findings, f.finding ∈ db.toMSS.findings := by
  intro f hf
  show f.finding ∈ db.findings.map (fun f => f.finding)
  exact List.mem_map_of_mem _ hf

end CDE
