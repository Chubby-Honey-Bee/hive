/-
  MSS Decidability — Decidable instances for the structural invariants

  These instances let bridge-emitted Lean files close an invariant on a
  concrete database with `by decide`: the kernel evaluates the decision
  procedure on the extracted state.

  Four of the five invariants are decided here: the three one-hop checks
  and acyclicity. `NoTransitiveLaundering` has no instance in this file;
  the gate's BFS audit enforces it in Go.

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic
import MSS.Invariants
import MSS.Paths

namespace MSS

/-! ## Helper: Decidable membership in findings -/

/-- Looking up a finding by ID is decidable (finite list search). -/
instance : DecidableEq Finding := by
  intro a b
  cases a; cases b
  simp [Finding.mk.injEq]
  exact inferInstance

/-! ## NoDirectLaundering Decidability

For a concrete finite database, we can check every guarantee's deps
by iterating the findings list. This is the runtime check that
Store.MSSAudit already performs.
-/

/-- NoDirectLaundering reformulated so every quantifier ranges over a
    finite list: instead of `∀ dep : Finding, db.get depId = some dep → …`
    (a ∀ over the infinite Finding type), fold the lookup into the body
    via db.labelOf. Decidable, and proved equivalent below. -/
def NoDirectLaunderingFin (db : FindingsDB) : Prop :=
  ∀ f ∈ db.findings,
    f.label = MSSLabel.guarantee →
    ∀ depId ∈ f.deps,
      db.labelOf depId ≠ some MSSLabel.unknown

theorem noDirectLaundering_iff_fin (db : FindingsDB) :
    NoDirectLaundering db ↔ NoDirectLaunderingFin db := by
  unfold NoDirectLaundering NoDirectLaunderingFin FindingsDB.labelOf
  constructor
  · intro h f hf hg depId hd
    -- From the get-guarded form, discharge the labelOf form by cases on get.
    cases hget : db.get depId with
    | none => simp [hget]
    | some dep => simpa [hget] using h f hf hg depId hd dep hget
  · intro h f hf hg depId hd dep hget
    have := h f hf hg depId hd
    intro hcontra
    -- dep.label = unknown contradicts labelOf depId ≠ some unknown.
    exact this (by simp [hget, hcontra])

/-- The finite reformulation is a nested bounded ∀ over finite lists with
    a decidable body (Option MSSLabel equality), so Lean derives it. -/
instance dec_no_direct_laundering_fin
    (db : FindingsDB)
    : Decidable (NoDirectLaunderingFin db) := by
  unfold NoDirectLaunderingFin
  infer_instance

/-- M12: NoDirectLaundering is decidable for finite FindingsDB.
    Transferred from the finite reformulation via the iff — the
    bridge-emitted `by decide` obligations depend on this. -/
instance dec_no_direct_laundering_finite
    (db : FindingsDB)
    : Decidable (NoDirectLaundering db) :=
  decidable_of_iff _ (noDirectLaundering_iff_fin db).symm

/-! ## NoUntraceableGuarantees Decidability

Simple: check every guarantee has non-empty deps.
-/

/-- NoUntraceableGuarantees is decidable for finite FindingsDB.
    A bounded ∀ over the finite findings list whose body is an
    implication of decidable props — Lean derives the instance. -/
instance dec_no_untraceable_finite
    (db : FindingsDB)
    : Decidable (NoUntraceableGuarantees db) := by
  unfold NoUntraceableGuarantees
  infer_instance

/-! ## DepsValid Decidability

Check every dep ID appears in the findings list.
-/

/-- DepsValid is decidable for finite FindingsDB.
    Nested bounded ∀ over findings and each finding's deps; the body
    `depId ∈ db.ids` is decidable membership in a finite Nat list. -/
instance dec_deps_valid_finite
    (db : FindingsDB)
    : Decidable (DepsValid db) := by
  unfold DepsValid
  infer_instance

/-! ## Acyclic Decidability

`Acyclic db = ∀ id, ¬ TransitiveDep db id id` quantifies over all of ℕ and
over an inductive closure, so it is not decidable by shape. The decision
procedure is fuel-bounded reachability with fuel = |findings|: `reachN`
asks whether a path of between 1 and n edges joins two nodes. It is
complete because a cycle that visits a node twice can be shortened at
the repeat (`PathList.dedup`), a cycle with no repeat visits at most
|findings| distinct ids (`Nodup.length_le_of_subset`), and nodes outside
`db.ids` have no outgoing edge, so they are on no cycle.
-/

/-- The deps of every finding with id `a`. Under `ids_unique` there is at most one. -/
def succs (db : FindingsDB) (a : Nat) : List Nat :=
  (db.findings.filter (fun f => f.id == a)).flatMap Finding.deps

theorem mem_succs_iff (db : FindingsDB) (a b : Nat) : b ∈ succs db a ↔ Step db a b := by
  unfold succs Step
  simp only [List.mem_flatMap, List.mem_filter, beq_iff_eq]
  constructor
  · intro ⟨f, ⟨hf, hid⟩, hb⟩
    exact ⟨f, hf, hid, hb⟩
  · intro ⟨f, hf, hid, hb⟩
    exact ⟨f, ⟨hf, hid⟩, hb⟩

/-- `reachN db n a b`: is there a path of between 1 and `n` edges from `a` to `b`? -/
def reachN (db : FindingsDB) : Nat → Nat → Nat → Bool
  | 0, _, _ => false
  | n + 1, a, b => (succs db a).any (fun c => c == b || reachN db n c b)

theorem chain_of_reachN (db : FindingsDB) :
    ∀ (n a b : Nat), reachN db n a b = true → Chain (Step db) a b := by
  intro n
  induction n with
  | zero => intro a b h; simp [reachN] at h
  | succ n ih =>
    intro a b h
    simp only [reachN, List.any_eq_true, Bool.or_eq_true, beq_iff_eq] at h
    obtain ⟨c, hc, hcb⟩ := h
    cases hcb with
    | inl hcb => rw [hcb] at hc; exact Chain.single ((mem_succs_iff db a b).mp hc)
    | inr hcb => exact Chain.cons ((mem_succs_iff db a c).mp hc) (ih c b hcb)

theorem reachN_of_pathList (db : FindingsDB) :
    ∀ (cs : List Nat) (n a b : Nat),
      cs.length < n → PathList (Step db) a cs b → reachN db n a b = true := by
  intro cs
  induction cs with
  | nil =>
    intro n a b hn h
    cases n with
    | zero => exact absurd hn (Nat.lt_irrefl 0)
    | succ n =>
      simp only [reachN, List.any_eq_true, Bool.or_eq_true, beq_iff_eq]
      exact ⟨b, (mem_succs_iff db a b).mpr h, Or.inl rfl⟩
  | cons c cs ih =>
    intro n a b hn h
    cases n with
    | zero => exact absurd hn (Nat.not_lt_zero _)
    | succ n =>
      simp only [reachN, List.any_eq_true, Bool.or_eq_true, beq_iff_eq]
      refine ⟨c, (mem_succs_iff db a c).mpr h.1, Or.inr ?_⟩
      exact ih n c b (by simp at hn; omega) h.2

/-- The finite check behind acyclicity: no finding reaches itself within |findings| edges. -/
def AcyclicFin (db : FindingsDB) : Prop :=
  ∀ a ∈ db.ids, reachN db db.findings.length a a = false

theorem acyclic_iff_fin (db : FindingsDB) : Acyclic db ↔ AcyclicFin db := by
  constructor
  · intro h a _
    cases hr : reachN db db.findings.length a a with
    | false => rfl
    | true =>
      exact absurd ((transitiveDep_iff_chain db a a).mpr (chain_of_reachN db _ a a hr)) (h a)
  · intro h a ha
    have hc := (transitiveDep_iff_chain db a a).mp ha
    obtain ⟨cs, hcs⟩ := chain_iff_pathList.mp hc
    obtain ⟨cs', hnd, hcs'⟩ := hcs.dedup
    have hsub := PathList.nodes_mem_ids hcs'
    have hlen := Nodup.length_le_of_subset hnd hsub
    have hids : db.ids.length = db.findings.length := List.length_map _ _
    have ha_ids : a ∈ db.ids := hsub a (List.mem_cons.mpr (Or.inl rfl))
    have hfalse := h a ha_ids
    have htrue := reachN_of_pathList db cs' db.findings.length a a (by simp at hlen; omega) hcs'
    rw [htrue] at hfalse
    exact Bool.noConfusion hfalse

instance dec_acyclic_fin (db : FindingsDB) : Decidable (AcyclicFin db) :=
  inferInstanceAs (Decidable (∀ a ∈ db.ids, reachN db db.findings.length a a = false))

/-- M13: Acyclic is decidable for finite FindingsDB, through `acyclic_iff_fin`.
    A real instance: `by decide` on a concrete database evaluates `reachN`. -/
instance dec_acyclic_finite (db : FindingsDB) : Decidable (Acyclic db) :=
  decidable_of_iff _ (acyclic_iff_fin db).symm

/-- A chain of two findings is acyclic. -/
example : Acyclic
    { findings := [{ id := 1, label := .guarantee, deps := [2] },
                   { id := 2, label := .assumption, deps := [] }]
      ids_unique := by decide } := by decide

/-- A two-cycle is not. -/
example : ¬ Acyclic
    { findings := [{ id := 1, label := .assumption, deps := [2] },
                   { id := 2, label := .assumption, deps := [1] }]
      ids_unique := by decide } := by decide

/-- A self-loop is not. -/
example : ¬ Acyclic
    { findings := [{ id := 7, label := .assumption, deps := [7] }]
      ids_unique := by decide } := by decide

end MSS
