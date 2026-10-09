/-
  CDE Index Correctness — I(E(r)) contains r (Layer 2)

  The CDE index maps coordinates to finding sets:
    I : Z^k -> 2^D

  Key properties:
  - C3: Every finding appears in its own coordinate's bucket: r in I(E(r))
  - C4: The union of all buckets is the full dataset
  - C5: Buckets are disjoint (a finding appears in exactly one bucket)

  These properties together mean the index is a perfect partition
  of the dataset by coordinate.

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import CDE.Basic
import CDE.Encoding

namespace CDE

/-! ## Index as Coordinate Lookup

The index function I maps a coordinate to the set of findings at that coordinate.
In the database, this is implemented by the SQL query:
  SELECT * FROM findings WHERE d1=? AND d2=? AND ... AND dk=?
-/

/-- Two coordinates match on all active dimensions. -/
def coordMatch (c1 c2 : CDECoord) (k : Nat) : Bool :=
  (List.range k).all fun i =>
    match c1.get i, c2.get i with
    | some v1, some v2 => v1 == v2
    | _, _ => false  -- NULL never matches

/-- Index lookup: all findings at a given coordinate. -/
def CDEDatabase.atCoord (db : CDEDatabase) (c : CDECoord) (k : Nat) : List CDEFinding :=
  db.findings.filter fun f => coordMatch f.coord c k

/-! ## Index Correctness Theorems -/

/-- A fully specified coordinate matches itself on every active dimension. -/
theorem coordMatch_self (c : CDECoord) (k : Nat) (h : c.fullySpecified k = true) :
    coordMatch c c k = true := by
  unfold coordMatch
  unfold CDECoord.fullySpecified at h
  rw [List.all_eq_true] at h ⊢
  intro i hi
  have hs := h i hi
  cases hc : c.get i with
  | none => rw [hc] at hs; simp at hs
  | some v => simp

/-- Two coordinates that both match a third agree on every active dimension. -/
theorem coordMatch_trans (a b c : CDECoord) (k : Nat)
    (h1 : coordMatch a b k = true) (h2 : coordMatch a c k = true) :
    coordMatch b c k = true := by
  unfold coordMatch at h1 h2 ⊢
  rw [List.all_eq_true] at h1 h2 ⊢
  intro i hi
  have e1 := h1 i hi
  have e2 := h2 i hi
  cases ha : a.get i with
  | none => rw [ha] at e1; simp at e1
  | some v =>
    rw [ha] at e1 e2
    cases hb : b.get i with
    | none => rw [hb] at e1; simp at e1
    | some v1 =>
      cases hc : c.get i with
      | none => rw [hc] at e2; simp at e2
      | some v2 =>
        rw [hb] at e1
        rw [hc] at e2
        simp at e1 e2
        simp
        omega

/-- C3: Every finding with fully specified coordinates appears in its own
    coordinate's index bucket: `f ∈ I(E(f))`.
    This is the self-referential property of the index. -/
theorem index_contains_encoded
    (db : CDEDatabase) (reg : DimRegistry) (f : CDEFinding)
    (hf : f ∈ db.findings)
    (h_complete : f.coord.fullySpecified reg.numDims = true)
    : f ∈ db.atCoord f.coord reg.numDims := by
  unfold CDEDatabase.atCoord
  exact List.mem_filter.mpr ⟨hf, coordMatch_self f.coord reg.numDims h_complete⟩

/-- C4: The union of all index buckets covers the full dataset.
    Every finding with valid coordinates is reachable via some probe. -/
theorem index_complete
    (db : CDEDatabase) (reg : DimRegistry)
    (h_complete : EncodingComplete db reg)
    : ∀ f ∈ db.findings, f ∈ db.atCoord f.coord reg.numDims := by
  intro f hf
  exact index_contains_encoded db reg f hf (h_complete f hf)

/-- C5: Index buckets are disjoint — a finding at coordinate c1 is NOT
    at any different coordinate c2.
    Requires: coordinates are fully specified (no NULLs). -/
theorem index_disjoint
    (db : CDEDatabase) (k : Nat) (f : CDEFinding) (c1 c2 : CDECoord)
    (hf1 : f ∈ db.atCoord c1 k)
    (hf2 : f ∈ db.atCoord c2 k)
    (h_spec : f.coord.fullySpecified k = true)
    : coordMatch c1 c2 k = true := by
  unfold CDEDatabase.atCoord at hf1 hf2
  have h1 := (List.mem_filter.mp hf1).2
  have h2 := (List.mem_filter.mp hf2).2
  exact coordMatch_trans f.coord c1 c2 k h1 h2

end CDE
