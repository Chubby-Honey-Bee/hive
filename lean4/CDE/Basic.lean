/-
  Coleman Dimensional Encoding — Core Types (Layer 2)

  CDE encodes each finding as a coordinate in a k-dimensional integer space.
  The encoding function E: D -> Z^k maps findings to coordinates.
  The index function I: Z^k -> 2^D maps coordinates back to finding sets.

  This file defines:
  - CDECoord: a coordinate vector with up to 8 dimensions (matching db schema d1..d8)
  - DimSpec: a registered dimension with bounded values
  - DimRegistry: ordered list of dimension specifications
  - CDEFinding: an MSS Finding extended with CDE coordinates

  Source of truth: internal/db/schema.go (the d1..d8 columns) and
  internal/db/dimensions.go (dimension registration).

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic

namespace CDE

/-! ## Coordinate Vectors

Up to 8 dimensions. Each is `Option Nat` — `none` means the dimension
is not specified for this finding. The self-audit found that NULL coordinates
break WASP sufficiency (findings become invisible to probes).
-/

/-- A coordinate in the CDE space. Up to 8 dimensions, each optional.
    Matches the d1..d8 columns in the findings table. -/
structure CDECoord where
  d1 : Option Nat
  d2 : Option Nat
  d3 : Option Nat
  d4 : Option Nat
  d5 : Option Nat
  d6 : Option Nat
  d7 : Option Nat
  d8 : Option Nat
  deriving DecidableEq, Repr

/-- Get the value at dimension index (0-based). Returns `none` for out of range. -/
def CDECoord.get (c : CDECoord) (dim : Nat) : Option Nat :=
  match dim with
  | 0 => c.d1 | 1 => c.d2 | 2 => c.d3 | 3 => c.d4
  | 4 => c.d5 | 5 => c.d6 | 6 => c.d7 | 7 => c.d8
  | _ => none

/-- Check if a coordinate has no NULL values in the first `k` dimensions. -/
def CDECoord.fullySpecified (c : CDECoord) (k : Nat) : Bool :=
  (List.range k).all fun i => (c.get i).isSome

/-! ## Dimension Registry

Each registered dimension has a name and a number of valid values.
Coordinate values must be less than `numValues` for their dimension.
-/

/-- Specification of a single dimension. -/
structure DimSpec where
  name : String
  numValues : Nat  -- how many distinct values this dimension has
  deriving Repr

/-- A dimension registry: ordered list of dimension specifications.
    The registry length determines how many of d1..d8 are "active". -/
structure DimRegistry where
  dims : List DimSpec
  h_bound : dims.length ≤ 8  -- can't have more than 8 dimensions

/-- Number of active dimensions. -/
def DimRegistry.numDims (r : DimRegistry) : Nat := r.dims.length

/-! ## Convergence Levels

From internal/gate/merge.go: convergence scoring for findings at the same coordinate.
-/

/-- Convergence level assigned by swarm-merge. -/
inductive ConvergenceLevel where
  | high | medium | low
  deriving DecidableEq, Repr

/-- Numeric encoding for ordering. -/
def ConvergenceLevel.toNat : ConvergenceLevel → Nat
  | .high => 2
  | .medium => 1
  | .low => 0

instance : LE ConvergenceLevel where
  le a b := a.toNat ≤ b.toNat

instance (a b : ConvergenceLevel) : Decidable (a ≤ b) :=
  inferInstanceAs (Decidable (a.toNat ≤ b.toNat))

/-! ## CDE-Encoded Findings

An MSS Finding extended with spatial coordinates and convergence metadata.
-/

/-- A finding with MSS label + CDE coordinates + convergence. -/
structure CDEFinding where
  finding : MSS.Finding    -- id, label, deps
  coord : CDECoord         -- spatial coordinates
  wave : Nat               -- which research wave
  convergenceCount : Nat   -- number of agents that found this
  convergenceLevel : ConvergenceLevel
  deriving Repr

/-- A CDE-encoded database: list of CDE findings with unique IDs. -/
structure CDEDatabase where
  findings : List CDEFinding
  ids_unique : (findings.map (fun f => f.finding.id)).Nodup

/-- Project a CDEDatabase down to an MSS FindingsDB (drop coordinates). -/
def CDEDatabase.toMSS (db : CDEDatabase) : MSS.FindingsDB :=
  { findings := db.findings.map (fun f => f.finding)
    ids_unique := by
      simp [List.map_map]
      exact db.ids_unique }

end CDE
