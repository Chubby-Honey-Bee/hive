/-
  WASP (Workload-Aware Sufficient Placement) — Core Types (Layer 3)

  WASP is the query framework built on top of CDE.
  It guarantees three properties for any workload W:
  1. Sufficiency: Ans(q) ⊆ Cand(q)  — no false negatives
  2. Exactness: Ans(q) = {r ∈ Cand(q) : F_q(r) = 1}  — filter removes all FPs
  3. Bounded work: Work(q) ≤ γ|Ans(q)| + β  — cost scales with answer size

  The key mechanism is floor-based binning:
    E(x) = ⌊x / binSize⌋

  Reference implementation: wasp_index.py in the external bloom-prediction-research project (not in this repository)

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/

namespace WASP

/-! ## Floor Binning

The encoding function for a single dimension: x -> floor(x / binSize).
Uses integer division (Nat.div), which floors by definition for natural numbers.
For the general case (negative coordinates), we use Int.ediv.
-/

/-- Floor-bin encoding for a single value.
    Maps a value to its bin index: `floorBin x b = x / b` (integer floor division). -/
def floorBin (x : Nat) (b : Nat) (h : b > 0 := by omega) : Nat :=
  x / b

/-! ## Range Queries

A query specifies a range [min, max] on each active dimension.
The probe set T(q) is the set of bin indices that cover the range.
-/

/-- A range query on a single dimension. -/
structure DimRange where
  min : Nat
  max : Nat
  binSize : Nat
  h_pos : binSize > 0
  h_valid : min ≤ max

/-- The probe set for a single dimension: all bin indices from floor(min/b) to floor(max/b). -/
def DimRange.probeBins (r : DimRange) : List Nat :=
  let minBin := floorBin r.min r.binSize r.h_pos
  let maxBin := floorBin r.max r.binSize r.h_pos
  (List.range (maxBin - minBin + 1)).map (· + minBin)

/-- A multi-dimensional range query. -/
structure WASPQuery where
  ranges : List DimRange

/-! ## Multi-dimensional probe set

For k ranges the probe set T(q) is the Cartesian product of the per-dimension
probe sets, listed as bin vectors. This is what the index query
`WHERE d1 IN (...) AND d2 IN (...)` enumerates.
-/

/-- The probe set T(q): every bin vector whose i-th entry is a probe bin of the i-th range. -/
def probeSet : List DimRange → List (List Nat)
  | [] => [[]]
  | r :: rs => r.probeBins.flatMap (fun b => (probeSet rs).map (fun v => b :: v))

/-- The bin vector of a record's values under a query's ranges, dimension by dimension. -/
def binVector : List DimRange → List Nat → List Nat
  | r :: rs, x :: xs => floorBin x r.binSize r.h_pos :: binVector rs xs
  | _, _ => []

/-- A record matches a query when it has one value per range and each value lies
    in its range: the exact predicate, before any binning. -/
def Matches : List DimRange → List Nat → Prop
  | [], [] => True
  | r :: rs, x :: xs => (r.min ≤ x ∧ x ≤ r.max) ∧ Matches rs xs
  | _, _ => False

/-- The candidate set: all records whose bin indices are in the probe set.
    In the real system, this is the SQL query with WHERE d1 IN (...) AND d2 IN (...).
    Size of the candidate set determines work. -/
structure WorkMetrics where
  probeCount : Nat      -- |T(q)|: number of coordinate tuples probed
  candidateCount : Nat  -- |Cand(q)|: records retrieved from index
  answerCount : Nat     -- |Ans(q)|: records after filtering
  deriving Repr

/-- Work = |T(q)| + |Cand(q)|, as compute_work defines it in the reference implementation. -/
def WorkMetrics.work (m : WorkMetrics) : Nat :=
  m.probeCount + m.candidateCount

/-- The work ratio γ = Work(q) / |Ans(q)| when |Ans(q)| > 0. -/
def WorkMetrics.gamma (m : WorkMetrics) (h : m.answerCount > 0) : Nat :=
  m.work / m.answerCount

end WASP
