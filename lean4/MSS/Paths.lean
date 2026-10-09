/-
  MSS Dependency Paths — the transitive closure as explicit paths

  `TransitiveDep` (Invariants.lean) is the inductive transitive closure of
  the one-step dependency relation. The proofs that cut a cycle short
  (acyclicity decidability) or follow a path edge by edge (preservation
  under insert and update) want paths that grow from the head, with their
  nodes listed. This file gives that form and proves it equivalent.

  `Chain r` is the transitive closure of any relation `r` on identifiers;
  nothing in this section is specific to findings.

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic
import MSS.Invariants

namespace MSS

/-! ## One dependency edge -/

/-- `Step db a b`: some finding with id `a` lists `b` among its deps. -/
def Step (db : FindingsDB) (a b : Nat) : Prop :=
  ∃ f ∈ db.findings, f.id = a ∧ b ∈ f.deps

/-- A node with an outgoing edge is a finding of the database. -/
theorem Step.mem_ids {db : FindingsDB} {a b : Nat} (h : Step db a b) : a ∈ db.ids := by
  obtain ⟨f, hf, hid, _⟩ := h
  rw [← hid]
  exact List.mem_map_of_mem Finding.id hf

/-! ## Paths over an arbitrary edge relation -/

/-- `Chain r a b`: a path of one or more `r`-edges from `a` to `b`, built from the head. -/
inductive Chain (r : Nat → Nat → Prop) : Nat → Nat → Prop where
  | single {a b : Nat} : r a b → Chain r a b
  | cons {a b c : Nat} : r a b → Chain r b c → Chain r a c

/-- A path of zero or more edges. -/
def ReflChain (r : Nat → Nat → Prop) (a b : Nat) : Prop := a = b ∨ Chain r a b

theorem Chain.trans {r : Nat → Nat → Prop} {a b c : Nat}
    (h₁ : Chain r a b) (h₂ : Chain r b c) : Chain r a c := by
  induction h₁ with
  | single h => exact Chain.cons h h₂
  | cons h _ ih => exact Chain.cons h (ih h₂)

theorem Chain.mono {r s : Nat → Nat → Prop} (hrs : ∀ a b, r a b → s a b) {a b : Nat}
    (h : Chain r a b) : Chain s a b := by
  induction h with
  | single h => exact Chain.single (hrs _ _ h)
  | cons h _ ih => exact Chain.cons (hrs _ _ h) ih

/-- Every path ends with an edge into its last node. -/
theorem Chain.last {r : Nat → Nat → Prop} {a b : Nat} (h : Chain r a b) : ∃ c, r c b := by
  induction h with
  | single h => exact ⟨_, h⟩
  | cons _ _ ih => exact ih

theorem Chain.trans_refl {r : Nat → Nat → Prop} {a b c : Nat}
    (h₁ : Chain r a b) (h₂ : ReflChain r b c) : Chain r a c := by
  cases h₂ with
  | inl h => exact h ▸ h₁
  | inr h => exact h₁.trans h

theorem ReflChain.trans {r : Nat → Nat → Prop} {a b c : Nat}
    (h₁ : ReflChain r a b) (h₂ : ReflChain r b c) : ReflChain r a c := by
  cases h₁ with
  | inl h => exact h ▸ h₂
  | inr h => exact Or.inr (h.trans_refl h₂)

theorem ReflChain.mono {r s : Nat → Nat → Prop} (hrs : ∀ a b, r a b → s a b) {a b : Nat}
    (h : ReflChain r a b) : ReflChain s a b := by
  cases h with
  | inl h => exact Or.inl h
  | inr h => exact Or.inr (h.mono hrs)

/-! ## `TransitiveDep` is `Chain Step` -/

theorem transitiveDep_iff_chain (db : FindingsDB) (a b : Nat) :
    TransitiveDep db a b ↔ Chain (Step db) a b := by
  constructor
  · intro h
    induction h with
    | direct f hf hid hb => exact Chain.single ⟨f, hf, hid, hb⟩
    | trans _ _ ih₁ ih₂ => exact ih₁.trans ih₂
  · intro h
    induction h with
    | single h =>
      obtain ⟨f, hf, hid, hb⟩ := h
      exact TransitiveDep.direct f hf hid hb
    | cons h _ ih =>
      obtain ⟨f, hf, hid, hb⟩ := h
      exact TransitiveDep.trans (TransitiveDep.direct f hf hid hb) ih

/-! ## Paths as node lists

`PathList r a cs b` is a path from `a` to `b` whose intermediate nodes are
`cs`, in order; it has `cs.length + 1` edges. -/

def PathList (r : Nat → Nat → Prop) (a : Nat) : List Nat → Nat → Prop
  | [], b => r a b
  | c :: cs, b => r a c ∧ PathList r c cs b

theorem chain_iff_pathList {r : Nat → Nat → Prop} {a b : Nat} :
    Chain r a b ↔ ∃ cs, PathList r a cs b := by
  constructor
  · intro h
    induction h with
    | single h => exact ⟨[], h⟩
    | cons h _ ih =>
      obtain ⟨cs, hcs⟩ := ih
      exact ⟨_ :: cs, h, hcs⟩
  · intro ⟨cs, hcs⟩
    induction cs generalizing a with
    | nil => exact Chain.single hcs
    | cons c cs ih => exact Chain.cons hcs.1 (ih hcs.2)

/-- Dropping a prefix of a path that reaches `c` leaves a path from `c`. -/
theorem PathList.drop_prefix {r : Nat → Nat → Prop} {a b c : Nat} {s t : List Nat}
    (h : PathList r a (s ++ (c :: t)) b) : PathList r c t b := by
  induction s generalizing a with
  | nil => exact h.2
  | cons _ s ih => exact ih h.2

/-- Cutting the loop between two visits of `c` leaves a path. -/
theorem PathList.cut_loop {r : Nat → Nat → Prop} {a b c : Nat} {s t u : List Nat}
    (h : PathList r a (s ++ (c :: (t ++ (c :: u)))) b) : PathList r a (s ++ (c :: u)) b := by
  induction s generalizing a with
  | nil => exact ⟨h.1, PathList.drop_prefix (s := t) h.2⟩
  | cons _ s ih => exact ⟨h.1, ih h.2⟩

/-- A list that is not `Nodup` repeats some element, with the list around it. -/
theorem exists_dup_of_not_nodup {l : List Nat} (h : ¬ l.Nodup) :
    ∃ c s t u, l = s ++ (c :: (t ++ (c :: u))) := by
  induction l with
  | nil => exact absurd List.nodup_nil h
  | cons x xs ih =>
    rw [List.nodup_cons] at h
    by_cases hx : x ∈ xs
    · obtain ⟨t, u, rfl⟩ := List.append_of_mem hx
      exact ⟨x, [], t, u, rfl⟩
    · have hxs : ¬ xs.Nodup := fun hn => h ⟨hx, hn⟩
      obtain ⟨c, s, t, u, rfl⟩ := ih hxs
      exact ⟨c, x :: s, t, u, rfl⟩

theorem PathList.dedup_aux {r : Nat → Nat → Prop} {a b : Nat} :
    ∀ (n : Nat) (cs : List Nat), cs.length ≤ n → PathList r a cs b →
      ∃ cs', (a :: cs').Nodup ∧ PathList r a cs' b := by
  intro n
  induction n with
  | zero =>
    intro cs hlen h
    have hnil : cs = [] := List.eq_nil_of_length_eq_zero (Nat.le_zero.mp hlen)
    subst hnil
    exact ⟨[], by simp, h⟩
  | succ n ih =>
    intro cs hlen h
    by_cases hn : (a :: cs).Nodup
    · exact ⟨cs, hn, h⟩
    · rw [List.nodup_cons] at hn
      by_cases ha : a ∈ cs
      · obtain ⟨s, t, rfl⟩ := List.append_of_mem ha
        have h' : PathList r a t b := PathList.drop_prefix h
        apply ih t _ h'
        simp at hlen
        omega
      · have hcs : ¬ cs.Nodup := fun hc => hn ⟨ha, hc⟩
        obtain ⟨c, s, t, u, rfl⟩ := exists_dup_of_not_nodup hcs
        have h' : PathList r a (s ++ (c :: u)) b := PathList.cut_loop h
        apply ih _ _ h'
        simp at hlen ⊢
        omega

/-- Every path can be shortened to one that visits no node twice, the start included. -/
theorem PathList.dedup {r : Nat → Nat → Prop} {a b : Nat} {cs : List Nat}
    (h : PathList r a cs b) : ∃ cs', (a :: cs').Nodup ∧ PathList r a cs' b :=
  PathList.dedup_aux cs.length cs (Nat.le_refl _) h

/-- Every node a dependency path leaves is a finding of the database. -/
theorem PathList.nodes_mem_ids {db : FindingsDB} {a b : Nat} :
    ∀ {cs : List Nat}, PathList (Step db) a cs b → ∀ x ∈ a :: cs, x ∈ db.ids := by
  intro cs
  induction cs generalizing a with
  | nil =>
    intro h x hx
    rw [List.mem_singleton] at hx
    rw [hx]
    exact h.mem_ids
  | cons c cs ih =>
    intro h x hx
    rw [List.mem_cons] at hx
    cases hx with
    | inl hx => rw [hx]; exact h.1.mem_ids
    | inr hx => exact ih h.2 x hx

/-- Pigeonhole: a duplicate-free list drawn from `m` is no longer than `m`. -/
theorem Nodup.length_le_of_subset :
    ∀ {l m : List Nat}, l.Nodup → (∀ x ∈ l, x ∈ m) → l.length ≤ m.length := by
  intro l
  induction l with
  | nil => intro m _ _; exact Nat.zero_le _
  | cons x xs ih =>
    intro m hn hsub
    rw [List.nodup_cons] at hn
    have hx : x ∈ m := hsub x (List.mem_cons.mpr (Or.inl rfl))
    have hsub' : ∀ y ∈ xs, y ∈ m.erase x := by
      intro y hy
      have hne : y ≠ x := fun h => hn.1 (h ▸ hy)
      exact (List.mem_erase_of_ne hne).mpr (hsub y (List.mem_cons.mpr (Or.inr hy)))
    have hle := ih hn.2 hsub'
    rw [List.length_erase_of_mem hx] at hle
    have hm : 0 < m.length := List.length_pos_of_mem hx
    simp only [List.length_cons]
    omega

end MSS
