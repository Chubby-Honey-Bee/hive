/-
  WASP Bounded Work — Work(q) ≤ γ|Ans(q)| + β (Layer 3)

  The third WASP property: query cost scales with answer size,
  not total dataset size. This is what makes CDE probes efficient.

  Work(q) = |T(q)| + |Cand(q)|
  where T(q) is the probe set (coordinates to check)
  and Cand(q) is the candidate set (records retrieved).

  The probe set size is bounded by the product of per-dimension
  range sizes (in bins), which depends on the query but NOT on |D|.

  Reference implementation: compute_work and verify_bounded_work in
  the external bloom-prediction-research project (not in this repository)

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import WASP.Basic

namespace WASP

/-! ## W6: Probe Set Size Bound

The probe set T(q) for a single dimension has at most
  ceil((max - min) / binSize) + 1
elements. This is the number of bins that the range [min, max] spans.
-/

/-- Number of bins that a range [min, max] spans with bin size b. -/
def numBins (min max b : Nat) (h : b > 0) : Nat :=
  (max / b) - (min / b) + 1

/-- W6: The probe set for a single dimension has at most numBins elements.
    `probeBins` is `List.range (maxBin - minBin + 1)` mapped, so the two
    sides are the same number. -/
theorem probe_set_bound_1d (r : DimRange) :
    r.probeBins.length ≤ numBins r.min r.max r.binSize r.h_pos := by
  unfold DimRange.probeBins numBins floorBin
  simp

/-! ## W7: Work Definition

Work is the sum of probe count and candidate count.
This is definitional — it matches the reference implementation's compute_work.
-/

/-- W7: Work is defined as probeCount + candidateCount. -/
theorem work_definition (m : WorkMetrics) :
    m.work = m.probeCount + m.candidateCount := by
  rfl

/-! ## Multi-Dimensional Probe Set Bound

For k dimensions, the probe set is the Cartesian product of
per-dimension probe sets. Its size is the product of per-dimension sizes.

|T(q)| = ∏ᵢ numBins(rangeᵢ)

This is bounded and does NOT depend on the total dataset size |D|.
-/

/-- Product of a list of natural numbers. -/
def listProduct : List Nat → Nat
  | [] => 1
  | n :: ns => n * listProduct ns

theorem sum_replicate (n c : Nat) : (List.replicate n c).sum = n * c := by
  induction n with
  | zero => simp
  | succ n ih => rw [List.replicate_succ, List.sum_cons, ih, Nat.succ_mul, Nat.add_comm]

/-- The multi-dimensional probe set bound: |T(q)| is at most the product of
    the per-dimension bin counts. Query cost depends on the QUERY SHAPE,
    not the DATASET SIZE. -/
theorem probe_set_bound_multi :
    ∀ (ranges : List DimRange),
      (probeSet ranges).length ≤
        listProduct (ranges.map fun r => numBins r.min r.max r.binSize r.h_pos) := by
  intro ranges
  induction ranges with
  | nil => simp [probeSet, listProduct]
  | cons r rs ih =>
    simp only [probeSet, List.map_cons, listProduct]
    rw [List.length_flatMap]
    have hconst : r.probeBins.map (List.length ∘ fun b => (probeSet rs).map (fun v => b :: v)) =
        r.probeBins.map (fun _ => (probeSet rs).length) := by
      apply List.map_congr_left
      intro b _
      simp
    rw [hconst, List.map_const', sum_replicate]
    exact Nat.mul_le_mul (probe_set_bound_1d r) ih

end WASP
