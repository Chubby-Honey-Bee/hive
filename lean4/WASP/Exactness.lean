/-
  WASP Exactness — Filter Removes All False Positives (Layer 3)

  After retrieving candidates from the index, the local filter F_q
  produces exactly the true answer set:
    Ans(q) = {r ∈ Cand(q) : F_q(r) = 1}

  This file also formalizes why a result limit breaks exactness. In this
  repository `FindingsRepo.Probe` applies a limit of 500 when none is given
  and refuses a larger one, so a probe matching more rows than its limit
  returns a truncated answer set — the case W5 describes.

  Reference implementation: verify_exactness and filter_exact in
  the external bloom-prediction-research project (not in this repository)

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import WASP.Basic

namespace WASP

/-! ## Local Filter

The filter F_q checks whether a candidate actually satisfies
the exact query predicate (e.g., latitude within exact bounds,
not just within the bin that contains the bounds).
-/

/-- A local filter: checks whether a value is in the exact query range.
    The index uses bins (approximate), the filter uses exact bounds. -/
def exactFilter (value : Nat) (queryMin queryMax : Nat) : Bool :=
  queryMin ≤ value && value ≤ queryMax

/-! ## W4: Exactness

After filtering, the result set IS the true answer set.
This follows from filter correctness: the filter accepts exactly
those candidates that satisfy the query predicate.
-/

/-- W4: The exact filter correctly classifies candidates.
    A candidate passes the filter iff it truly matches the query. -/
theorem filter_exact_correct (value queryMin queryMax : Nat) :
    exactFilter value queryMin queryMax = true ↔
    queryMin ≤ value ∧ value ≤ queryMax := by
  simp [exactFilter, Bool.and_eq_true]

/-! ## W5: Truncation Breaks Exactness

A LIMIT truncates the candidate set: if |Cand(q)| exceeds it, some
candidates are dropped, and they may include true answers, violating
exactness. `FindingsRepo.Probe` limits at 500.
-/

/-- An element of the first `n` entries of a list sits at an index below `n`. -/
theorem indexOf_lt_of_mem_take (l : List Nat) (n : Nat) (a : Nat) (h : a ∈ l.take n) :
    l.indexOf a < n := by
  induction l generalizing n with
  | nil => simp at h
  | cons x xs ih =>
    cases n with
    | zero => simp at h
    | succ n =>
      rw [List.take_succ_cons, List.mem_cons] at h
      rw [List.indexOf_cons]
      by_cases hx : x = a
      · simp [hx]
      · have hb : (x == a) = false := beq_false_of_ne hx
        simp only [hb, cond_false]
        have hmem : a ∈ xs.take n := by
          cases h with
          | inl h => exact absurd h.symm hx
          | inr h => exact h
        have := ih n hmem
        omega

/-- W5: Truncating the candidate set can drop true answers.
    If the candidate set has more than `limit` elements and the
    true answer appears after position `limit`, exactness fails.

    This is a statement of the problem, not a proof that it always fails —
    truncation only violates exactness when true answers are beyond the limit. -/
theorem limit_breaks_exactness
    (candidates : List Nat) (limit : Nat) (answer : Nat)
    (h_in : answer ∈ candidates)
    (h_beyond : candidates.indexOf answer ≥ limit)
    (h_trunc : limit < candidates.length)
    : answer ∉ candidates.take limit := by
  intro h
  have := indexOf_lt_of_mem_take candidates limit answer h
  omega

end WASP
