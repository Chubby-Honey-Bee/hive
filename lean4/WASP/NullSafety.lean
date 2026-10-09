/-
  WASP Null Safety — NULL coordinates break sufficiency (Layer 3)

  Findings with NULL coordinates are invisible to ALL probes, violating
  WASP sufficiency (Ans(q) ⊆ Cand(q)).

  The theorems here serve two purposes:
  1. Show that failure on a concrete counterexample (W9)
  2. State the precondition that W3 (sufficiency) requires: no NULLs

  In this repository the write path refuses NULLs on registered dimensions
  (`cde.ValidateCoords`).

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import CDE.Basic
import CDE.Encoding
import CDE.Index

namespace WASP

/-! ## W8: NULL Coordinates Make Findings Invisible

A finding with NULL in any active dimension is not returned
by any coordinate probe. The SQL `WHERE d3 = ?` never matches NULL.
-/

/-- W8: A finding with a NULL coordinate in an active dimension
    does not match any coordinate probe (coordMatch returns false).

    This is the formal statement of the hazard: NULL findings are invisible. -/
theorem null_invisible (f : CDE.CDEFinding) (c : CDE.CDECoord) (k : Nat)
    (i : Nat) (h_active : i < k)
    (h_null : f.coord.get i = none)
    : CDE.coordMatch f.coord c k = false := by
  unfold CDE.coordMatch
  rw [List.all_eq_false]
  exact ⟨i, List.mem_range.mpr h_active, by simp [h_null]⟩

/-! ## W9: NULL Breaks Sufficiency — Constructive Counterexample

There exists a database and query where a finding truly matches
the query (it's in Ans(q)) but is NOT in the candidate set (it's
not in Cand(q)), because it has a NULL coordinate.
-/

/-- A finding with NULL d3 that would match query {d1:0, d2:0, d3:0}
    but is invisible to the probe because d3 is NULL. -/
private def null_finding : CDE.CDEFinding :=
  { finding := { id := 1, label := .assumption, deps := [] }
    coord := { d1 := some 0, d2 := some 0, d3 := none,
               d4 := none, d5 := none, d6 := none, d7 := none, d8 := none }
    wave := 1
    convergenceCount := 1
    convergenceLevel := .low }

/-- The probe coordinate that should find the above finding. -/
private def probe_coord : CDE.CDECoord :=
  { d1 := some 0, d2 := some 0, d3 := some 0,
    d4 := none, d5 := none, d6 := none, d7 := none, d8 := none }

/-- W9: Constructive counterexample — NULL breaks sufficiency.
    The null_finding matches the query conceptually (d1=0, d2=0)
    but is NOT returned by a probe that constrains d3, because d3 is NULL.

    This means: ∃ q r, r ∈ Ans(q) ∧ r ∉ Cand(q). Sufficiency is violated. -/
theorem null_breaks_sufficiency :
    CDE.coordMatch null_finding.coord probe_coord 3 = false := by
  -- `decide`, not `native_decide`: the goal is closed and tiny (List.range 3),
  -- so the kernel can evaluate it itself. `native_decide` compiled it to
  -- native code and trusted the result via `Lean.ofReduceBool`, which made
  -- this the one proof in the tree that was compiler-trusted rather than
  -- kernel-checked — while docs/foundations.md called the discharged proofs
  -- "genuinely kernel-checked".
  decide

/-! ## X3: Sufficiency Requires Non-NULL Encoding

The connection between WASP (Layer 3) and CDE (Layer 2):
WASP sufficiency (W3) requires CDE encoding completeness (C1)
as a precondition.
-/

/-- X3: When the encoding is not complete, some finding has a NULL active
    coordinate and is in no index bucket at all: every probe misses it, so
    Ans(q) ⊆ Cand(q) fails for any query it matches. The general form of
    `null_breaks_sufficiency`, through `null_invisible`. -/
theorem wasp_needs_cde_nonnull
    (db : CDE.CDEDatabase) (reg : CDE.DimRegistry)
    (h : ¬ CDE.EncodingComplete db reg) :
    ∃ f ∈ db.findings, (∃ i, i < reg.numDims ∧ f.coord.get i = none) ∧
      ∀ c : CDE.CDECoord, f ∉ db.atCoord c reg.numDims := by
  have hall : db.findings.all (fun f => f.coord.fullySpecified reg.numDims) = false := by
    cases hb : db.findings.all (fun f => f.coord.fullySpecified reg.numDims) with
    | true => exact absurd (fun f hf => List.all_eq_true.mp hb f hf) h
    | false => rfl
  obtain ⟨f, hf, hns⟩ := List.all_eq_false.mp hall
  have hfs : f.coord.fullySpecified reg.numDims = false := by
    cases hb : f.coord.fullySpecified reg.numDims with
    | true => exact absurd hb hns
    | false => rfl
  unfold CDE.CDECoord.fullySpecified at hfs
  obtain ⟨i, hi, hnone⟩ := List.all_eq_false.mp hfs
  rw [List.mem_range] at hi
  have hnull : f.coord.get i = none := by
    cases hg : f.coord.get i with
    | none => rfl
    | some v => rw [hg] at hnone; exact absurd rfl hnone
  refine ⟨f, hf, ⟨i, hi, hnull⟩, ?_⟩
  intro c hc
  unfold CDE.CDEDatabase.atCoord at hc
  have hm := (List.mem_filter.mp hc).2
  rw [null_invisible f c reg.numDims i hi hnull] at hm
  exact Bool.false_ne_true hm

end WASP
