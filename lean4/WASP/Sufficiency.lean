/-
  WASP Sufficiency — No False Negatives (Layer 3)

  The central WASP theorem: floor-based binning covers ranges.

  Core lemma (W1): For a ≤ x ≤ c and b > 0:
    ⌊a/b⌋ ≤ ⌊x/b⌋ ≤ ⌊c/b⌋

  This means: if a record's value x is within the query range [a, c],
  then its bin index floor(x/b) is within the probe set [floor(a/b), floor(c/b)].
  Therefore the probe set always includes the record's bin — no false negatives.

  Reference implementation: verify_no_false_negatives in
  the external bloom-prediction-research project (not in this repository)

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import WASP.Basic

namespace WASP

/-! ## W1: Floor-Bin Monotonicity (Core Lemma)

The mathematical heart of WASP sufficiency.
For natural numbers: a ≤ x → a/b ≤ x/b (integer division is monotone).
-/

/-- W1: Integer floor division is monotone.
    If a ≤ x, then ⌊a/b⌋ ≤ ⌊x/b⌋ for b > 0. -/
theorem floor_bin_monotone (a x b : Nat) (h_pos : b > 0) (h_le : a ≤ x) :
    a / b ≤ x / b := by
  -- a / b ≤ x / b  ⟺  (a / b) * b ≤ x, and (a / b) * b ≤ a ≤ x.
  exact (Nat.le_div_iff_mul_le h_pos).mpr (Nat.le_trans (Nat.div_mul_le_self a b) h_le)

/-- W1 corollary: if a ≤ x ≤ c, then ⌊a/b⌋ ≤ ⌊x/b⌋ ≤ ⌊c/b⌋. -/
theorem floor_bin_range (a x c b : Nat) (h_pos : b > 0)
    (h_lo : a ≤ x) (h_hi : x ≤ c) :
    a / b ≤ x / b ∧ x / b ≤ c / b := by
  exact ⟨floor_bin_monotone a x b h_pos h_lo, floor_bin_monotone x c b h_pos h_hi⟩

/-! ## W2: Probe Set Covers Range

If a record's value is in [min, max], its bin index is in the probe set.
-/

/-- W2: A value in the query range has its bin index in the probe set.
    If min ≤ x ≤ max, then floorBin x b is in [floorBin min b, floorBin max b]. -/
theorem probe_covers_range (r : DimRange) (x : Nat)
    (h_lo : r.min ≤ x) (h_hi : x ≤ r.max) :
    floorBin r.min r.binSize r.h_pos ≤ floorBin x r.binSize r.h_pos ∧
    floorBin x r.binSize r.h_pos ≤ floorBin r.max r.binSize r.h_pos := by
  exact floor_bin_range r.min x r.max r.binSize r.h_pos h_lo h_hi

/-- The per-dimension probe set is exactly the bins between the range's end bins. -/
theorem mem_probeBins (r : DimRange) (b : Nat) :
    b ∈ r.probeBins ↔
      floorBin r.min r.binSize r.h_pos ≤ b ∧ b ≤ floorBin r.max r.binSize r.h_pos := by
  -- the range's end bins are ordered (W1 on `h_valid`); without it the
  -- truncated subtraction in `probeBins` would make the bound meaningless
  have hmm : floorBin r.min r.binSize r.h_pos ≤ floorBin r.max r.binSize r.h_pos :=
    floor_bin_monotone r.min r.max r.binSize r.h_pos r.h_valid
  unfold DimRange.probeBins
  simp only [List.mem_map, List.mem_range]
  constructor
  · intro ⟨i, hi, hb⟩
    omega
  · intro ⟨h1, h2⟩
    exact ⟨b - floorBin r.min r.binSize r.h_pos, by omega, by omega⟩

/-! ## W3: No False Negatives (Sufficiency)

The full sufficiency theorem: Ans(q) ⊆ Cand(q).
Every record that truly matches the query has its bin vector in the probe
set, so the index probe retrieves it.

This composes W1+W2 across all dimensions.
-/

/-- W3: No false negatives in single-dimension WASP queries.
    If a record's value is in the query range, the probe retrieves
    the record's bin. Combined with CDE C3 (index contains encoded),
    this means the record is in the candidate set. -/
theorem no_false_negatives_1d (r : DimRange) (x : Nat)
    (h_match : r.min ≤ x ∧ x ≤ r.max) :
    let xBin := floorBin x r.binSize r.h_pos
    let minBin := floorBin r.min r.binSize r.h_pos
    let maxBin := floorBin r.max r.binSize r.h_pos
    minBin ≤ xBin ∧ xBin ≤ maxBin := by
  exact probe_covers_range r x h_match.1 h_match.2

/-- W3 (full, multi-dimensional): a record that matches the query has its bin
    vector in the probe set T(q). Dimension by dimension this is W2; the
    composition is induction over the ranges. With CDE C3 (a finding is in its
    own coordinate's bucket) the record is therefore in the candidate set. -/
theorem no_false_negatives_multi :
    ∀ (ranges : List DimRange) (values : List Nat),
      Matches ranges values → binVector ranges values ∈ probeSet ranges := by
  intro ranges
  induction ranges with
  | nil =>
    intro values h
    cases values with
    | nil => simp [binVector, probeSet]
    | cons x xs => exact (h : False).elim
  | cons r rs ih =>
    intro values h
    cases values with
    | nil => exact (h : False).elim
    | cons x xs =>
      obtain ⟨hx, hrest⟩ := h
      simp only [binVector, probeSet, List.mem_flatMap, List.mem_map]
      refine ⟨floorBin x r.binSize r.h_pos, ?_, binVector rs xs, ih xs hrest, rfl⟩
      exact (mem_probeBins r _).mpr (probe_covers_range r x hx.1 hx.2)

end WASP
