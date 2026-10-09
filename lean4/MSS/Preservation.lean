/-
  MSS Preservation Theorems — Write and Update Soundness

  These theorems model the Go enforcement (write-time checks in
  FindingsRepo.AddFinding / mss.ValidateGuaranteeDeps, update-time checks in
  FindingsRepo.UpdateFinding) and prove what that enforcement maintains.
  Where the model needed a premise the Go check does not supply, the
  theorem states it and its comment names the counterexample.

  © 2026 Chubby Honey Bee Inc. — chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic
import MSS.Invariants
import MSS.Paths

namespace MSS

/-! ## Write Operation Model

`FindingsRepo.AddFinding` (internal/db/findings.go) performs these checks
before inserting a new finding:

1. If label = guarantee, deps must be non-empty
2. All dep IDs must exist in the database, whatever the label
   (`mss.ValidateDepsExist`; `mss.ValidateGuaranteeDeps` for a guarantee)
3. No dep may have label = unknown

We model this as a predicate on the new finding + existing DB.
-/

/-- The precondition the Go write path enforces before inserting a finding. -/
def WriteCheckPasses (db : FindingsDB) (f : Finding) : Prop :=
  -- Check 1: guarantees must have deps
  (f.label = MSSLabel.guarantee → f.deps ≠ []) ∧
  -- Check 2: all dep IDs must exist
  (∀ depId ∈ f.deps, depId ∈ db.ids) ∧
  -- Check 3: no dep is labeled unknown
  (f.label = MSSLabel.guarantee →
    ∀ depId ∈ f.deps,
      ∀ dep : Finding, db.get depId = some dep →
        dep.label ≠ MSSLabel.unknown) ∧
  -- Implicit: new ID is fresh (not already in DB)
  (f.id ∉ db.ids)

/-- Insert a finding into the database, producing a new DB.
    Assumes the new finding's ID is fresh (not already present). -/
def FindingsDB.insert (db : FindingsDB) (f : Finding) (h_fresh : f.id ∉ db.ids) : FindingsDB :=
  { findings := f :: db.findings
    ids_unique := by
      have hu : (db.findings.map Finding.id).Nodup := db.ids_unique
      have hf : f.id ∉ db.findings.map Finding.id := by
        simpa [FindingsDB.ids] using h_fresh
      simpa [List.map_cons] using List.nodup_cons.mpr ⟨hf, hu⟩ }

/-- Looking up an existing id after an insert sees the old finding: the new
    id is fresh, so it is not the one looked up. -/
theorem insert_get_of_mem (db : FindingsDB) (f : Finding) (h_fresh : f.id ∉ db.ids)
    {x : Nat} (hx : x ∈ db.ids) : (db.insert f h_fresh).get x = db.get x := by
  have hne : f.id ≠ x := fun h => h_fresh (h ▸ hx)
  show List.find? (fun g => g.id == x) (f :: db.findings) = db.get x
  rw [List.find?_cons_of_neg]
  · rfl
  · simp [hne]

/-- One edge of the inserted database is an old edge or an edge out of the new finding. -/
theorem step_insert (db : FindingsDB) (f : Finding) (h_fresh : f.id ∉ db.ids) {a b : Nat}
    (h : Step (db.insert f h_fresh) a b) : Step db a b ∨ (a = f.id ∧ b ∈ f.deps) := by
  obtain ⟨g, hg, hid, hb⟩ := h
  have hg' : g = f ∨ g ∈ db.findings := List.mem_cons.mp hg
  cases hg' with
  | inl hgf => rw [hgf] at hid hb; exact Or.inr ⟨hid.symm, hb⟩
  | inr hg => exact Or.inl ⟨g, hg, hid, hb⟩

/-! ## Write Preservation Theorems

If the database satisfies an invariant and the write checks pass, the
database after insertion satisfies it too.
-/

/-- Writing a finding that passes all checks preserves NoDirectLaundering.

    `DepsValid db` is a premise. Without it the statement is false: take a
    database holding one guarantee `g` with `deps = [7]` and no finding 7.
    `NoDirectLaundering` holds (the lookup of 7 fails), and inserting
    `f = { id := 7, label := unknown, deps := [] }` passes every write check
    (none of them looks at `f`'s dependents). Afterwards `g` depends directly
    on the unknown `f`. The Go write path keeps `DepsValid` for every label
    (`mss.ValidateDepsExist` checks existence), so the premise is the
    invariant the model carries, not a new check. -/
theorem write_preserves_no_laundering
    (db : FindingsDB) (f : Finding)
    (h_wf : NoDirectLaundering db)
    (h_dv : DepsValid db)
    (h_check : WriteCheckPasses db f)
    (h_fresh : f.id ∉ db.ids)
    : NoDirectLaundering (db.insert f h_fresh) := by
  obtain ⟨_, h_exist, h_unk, _⟩ := h_check
  intro g hg hguar depId hdep dep hget
  have hg' : g = f ∨ g ∈ db.findings := List.mem_cons.mp hg
  cases hg' with
  | inl hgf =>
    rw [hgf] at hguar hdep
    have hx : depId ∈ db.ids := h_exist depId hdep
    rw [insert_get_of_mem db f h_fresh hx] at hget
    exact h_unk hguar depId hdep dep hget
  | inr hg =>
    have hx : depId ∈ db.ids := h_dv g hg depId hdep
    rw [insert_get_of_mem db f h_fresh hx] at hget
    exact h_wf g hg hguar depId hdep dep hget

/-- Writing a finding that passes all checks preserves NoUntraceableGuarantees. -/
theorem write_preserves_no_untraceable
    (db : FindingsDB) (f : Finding)
    (h_wf : NoUntraceableGuarantees db)
    (h_check : WriteCheckPasses db f)
    (h_fresh : f.id ∉ db.ids)
    : NoUntraceableGuarantees (db.insert f h_fresh) := by
  intro g hg hguar
  have hg' : g = f ∨ g ∈ db.findings := List.mem_cons.mp hg
  cases hg' with
  | inl hgf => rw [hgf] at hguar ⊢; exact h_check.1 hguar
  | inr hg => exact h_wf g hg hguar

/-- Writing a finding that passes all checks preserves DepsValid. -/
theorem write_preserves_deps_valid
    (db : FindingsDB) (f : Finding)
    (h_wf : DepsValid db)
    (h_check : WriteCheckPasses db f)
    (h_fresh : f.id ∉ db.ids)
    : DepsValid (db.insert f h_fresh) := by
  intro g hg depId hdep
  have hg' : g = f ∨ g ∈ db.findings := List.mem_cons.mp hg
  show depId ∈ f.id :: db.ids
  cases hg' with
  | inl hgf => rw [hgf] at hdep; exact List.mem_cons.mpr (Or.inr (h_check.2.1 depId hdep))
  | inr hg => exact List.mem_cons.mpr (Or.inr (h_wf g hg depId hdep))

/-- Nothing points at a fresh id: old deps are valid, so they name no fresh id,
    and the new finding's deps exist, so they do not name it either. -/
theorem no_step_into_fresh (db : FindingsDB) (f : Finding) (h_dv : DepsValid db)
    (h_exist : ∀ depId ∈ f.deps, depId ∈ db.ids) (h_fresh : f.id ∉ db.ids) (a : Nat) :
    ¬ Step (db.insert f h_fresh) a f.id := by
  intro h
  cases step_insert db f h_fresh h with
  | inl h => obtain ⟨g, hg, _, hb⟩ := h; exact h_fresh (h_dv g hg _ hb)
  | inr h => exact h_fresh (h_exist _ h.2)

/-- In a graph whose edges are old edges or edges leaving `n`, and which has no
    edge into `n`, a path either starts at `n` or is a path of the old graph. -/
theorem Chain.old_or_starts_at {r s : Nat → Nat → Prop} {n : Nat}
    (hrs : ∀ a b, s a b → r a b ∨ a = n) (hn : ∀ a, ¬ s a n) {a b : Nat}
    (h : Chain s a b) : a = n ∨ Chain r a b := by
  induction h with
  | single h =>
    cases hrs _ _ h with
    | inl h => exact Or.inr (Chain.single h)
    | inr h => exact Or.inl h
  | cons h _ ih =>
    cases ih with
    | inl hc => subst hc; exact absurd h (hn _)
    | inr hcb =>
      cases hrs _ _ h with
      | inl h' => exact Or.inr (Chain.cons h' hcb)
      | inr ha => exact Or.inl ha

/-- Writing a finding that passes all checks preserves Acyclic. The new finding
    has no dependents (its id is fresh and `DepsValid` holds), so no cycle can
    pass through it, and a cycle avoiding it is a cycle of the old graph. -/
theorem write_preserves_acyclic
    (db : FindingsDB) (f : Finding)
    (h_wf : Acyclic db)
    (h_dv : DepsValid db)
    (h_check : WriteCheckPasses db f)
    (h_fresh : f.id ∉ db.ids)
    : Acyclic (db.insert f h_fresh) := by
  intro x hx
  have hc := (transitiveDep_iff_chain _ x x).mp hx
  have hno := no_step_into_fresh db f h_dv h_check.2.1 h_fresh
  have hrs : ∀ a b, Step (db.insert f h_fresh) a b → Step db a b ∨ a = f.id := by
    intro a b h
    cases step_insert db f h_fresh h with
    | inl h => exact Or.inl h
    | inr h => exact Or.inr h.1
  cases Chain.old_or_starts_at hrs hno hc with
  | inl hxf =>
    obtain ⟨c, hcx⟩ := hc.last
    rw [hxf] at hcx
    exact hno c hcx
  | inr hc' => exact h_wf x ((transitiveDep_iff_chain db x x).mpr hc')

/-! ## Gate soundness — not modelled

The Go gate (`internal/gate/gate.go`) is scoped to a single wave, and
`MSS.Finding` has no `wave` field, so nothing here models it. A
`GateCheckPasses db wave` that ignored its wave would be `NoDirectLaundering`
written out again, and a soundness theorem over it an identity.

Modelling the gate faithfully needs a `wave` field on `MSS.Finding`, and with
one, `GateCheckPasses db wave → NoDirectLaundering db` is false: a wave-scoped
check does not imply full-database `NoDirectLaundering`. That guarantee is
carried by the full MSS audit (`store.MSSAudit`), which the gate also runs —
not by the wave query. Stating it here is open work, and left open rather than
restated as a tautology.
-/

/-! ## Update Operation Model

`FindingsRepo.UpdateFinding` (internal/db/findings.go) validates a
label or dependency change before writing it. The theorems below state
what an update must check to preserve the invariants.
-/

/-- Precondition for updating a finding's label or deps in-place.
    The finding already exists; we're changing its label or deps. -/
def UpdateCheckPasses (db : FindingsDB) (id : Nat) (newLabel : MSSLabel) (newDeps : List Nat) : Prop :=
  -- The finding must exist
  (id ∈ db.ids) ∧
  -- If new label is guarantee, new deps must be non-empty
  (newLabel = MSSLabel.guarantee → newDeps ≠ []) ∧
  -- All new dep IDs must exist
  (∀ depId ∈ newDeps, depId ∈ db.ids) ∧
  -- If new label is guarantee, no new dep may be unknown
  (newLabel = MSSLabel.guarantee →
    ∀ depId ∈ newDeps,
      ∀ dep : Finding, db.get depId = some dep →
        dep.label ≠ MSSLabel.unknown) ∧
  -- No self-dependency (would create a trivial cycle)
  (id ∉ newDeps)

/-- The in-place change of one finding: the one with id `id` gets the new
    label and deps, every other finding is returned unchanged. -/
def updateFn (id : Nat) (newLabel : MSSLabel) (newDeps : List Nat) (f : Finding) : Finding :=
  if f.id = id then { f with label := newLabel, deps := newDeps } else f

@[simp] theorem updateFn_id (id : Nat) (newLabel : MSSLabel) (newDeps : List Nat) (f : Finding) :
    (updateFn id newLabel newDeps f).id = f.id := by
  unfold updateFn
  split <;> rfl

/-- Update the finding with id `id` in place. Ids are untouched, so uniqueness carries over. -/
def FindingsDB.update (db : FindingsDB) (id : Nat) (newLabel : MSSLabel) (newDeps : List Nat) :
    FindingsDB :=
  { findings := db.findings.map (updateFn id newLabel newDeps)
    ids_unique := by
      rw [List.map_map]
      have h : Finding.id ∘ updateFn id newLabel newDeps = Finding.id := by
        funext f
        exact updateFn_id id newLabel newDeps f
      rw [h]
      exact db.ids_unique }

/-- A lookup in the updated database is the old lookup, passed through the update. -/
theorem update_get (db : FindingsDB) (id : Nat) (l : MSSLabel) (d : List Nat) (x : Nat) :
    (db.update id l d).get x = (db.get x).map (updateFn id l d) := by
  have hp : ((fun g : Finding => g.id == x) ∘ updateFn id l d) = (fun g => g.id == x) := by
    funext g
    simp [Function.comp, updateFn_id]
  show List.find? (fun g => g.id == x) (db.findings.map (updateFn id l d)) =
    (db.findings.find? (fun g => g.id == x)).map (updateFn id l d)
  rw [List.find?_map, hp]

theorem get_id (db : FindingsDB) {x : Nat} {f : Finding} (h : db.get x = some f) : f.id = x := by
  have hp := List.find?_some h
  simpa using hp

/-- M8: Updating a finding's label and deps preserves NoDirectLaundering,
    provided the update checks pass and, when the new label is `unknown`,
    no guarantee depends on the finding.

    The last premise is not among the update checks, and without it the
    statement is false: a guarantee `g` with `deps = [a]` over an assumption
    `a`, and the update of `a` to `unknown` with empty deps, passes every
    check in `UpdateCheckPasses` (they all look at the new label and the new
    deps, not at dependents), and afterwards `g` depends directly on an
    unknown. The Go `UpdateFinding` supplies the premise: it refuses the
    relabel to `unknown` while a guarantee rests on the finding, directly or
    through a chain of any labels (`mss.GuaranteesRestingOn`), and names the
    cascade that reverts them first. -/
theorem update_preserves_no_laundering
    (db : FindingsDB) (id : Nat) (newLabel : MSSLabel) (newDeps : List Nat)
    (h_wf : NoDirectLaundering db)
    (h_check : UpdateCheckPasses db id newLabel newDeps)
    (h_dependents : newLabel = MSSLabel.unknown →
      ∀ g ∈ db.findings, g.label = MSSLabel.guarantee → id ∉ g.deps)
    : NoDirectLaundering (db.update id newLabel newDeps) := by
  obtain ⟨_, _, _, h_unk, h_self⟩ := h_check
  intro g hg hguar depId hdep dep hget
  rw [update_get, Option.map_eq_some'] at hget
  obtain ⟨d, hd, rfl⟩ := hget
  have hdid : d.id = depId := get_id db hd
  obtain ⟨f, hf, rfl⟩ := List.mem_map.mp hg
  by_cases hfid : f.id = id
  · simp [updateFn, hfid] at hguar hdep
    have hne : depId ≠ id := fun h => h_self (h ▸ hdep)
    have hdne : ¬ d.id = id := by rw [hdid]; exact hne
    simp [updateFn, hdne]
    exact h_unk hguar depId hdep d hd
  · simp [updateFn, hfid] at hguar hdep
    by_cases hdeq : depId = id
    · have hdi : d.id = id := by rw [hdid, hdeq]
      simp [updateFn, hdi]
      intro hnl
      exact h_dependents hnl f hf hguar (hdeq ▸ hdep)
    · have hdne : ¬ d.id = id := by rw [hdid]; exact hdeq
      simp [updateFn, hdne]
      exact h_wf f hf hguar depId hdep d hd

/-- One edge of the updated database: an old edge not leaving `id`, or an
    edge from `id` into the new deps. -/
theorem step_update (db : FindingsDB) (id : Nat) (l : MSSLabel) (d : List Nat) {a b : Nat}
    (h : Step (db.update id l d) a b) : (a ≠ id ∧ Step db a b) ∨ (a = id ∧ b ∈ d) := by
  obtain ⟨g, hg, hid, hb⟩ := h
  obtain ⟨f, hf, rfl⟩ := List.mem_map.mp hg
  by_cases hfid : f.id = id
  · simp [updateFn, hfid] at hid hb
    exact Or.inr ⟨hid.symm, hb⟩
  · simp [updateFn, hfid] at hid hb
    exact Or.inl ⟨fun h => hfid (hid.trans h), ⟨f, hf, hid, hb⟩⟩

/-- An old edge that does not leave `id` survives the update. -/
theorem step_update_of_old (db : FindingsDB) (id : Nat) (l : MSSLabel) (d : List Nat) {a b : Nat}
    (hne : a ≠ id) (h : Step db a b) : Step (db.update id l d) a b := by
  obtain ⟨f, hf, hid, hb⟩ := h
  refine ⟨updateFn id l d f, List.mem_map_of_mem _ hf, ?_, ?_⟩
  · rw [updateFn_id]; exact hid
  · have hfid : ¬ f.id = id := by rw [hid]; exact hne
    simp [updateFn, hfid, hb]

/-- A path of the updated graph either uses no edge out of `id`, or it reaches
    `id` without one and then takes an edge into the new deps. -/
theorem chain_update_split (db : FindingsDB) (id : Nat) (l : MSSLabel) (d : List Nat) {a b : Nat}
    (h : Chain (Step (db.update id l d)) a b) :
    Chain (fun x y => x ≠ id ∧ Step db x y) a b ∨
      (ReflChain (fun x y => x ≠ id ∧ Step db x y) a id ∧
        ∃ c ∈ d, ReflChain (Step (db.update id l d)) c b) := by
  induction h with
  | single h =>
    cases step_update db id l d h with
    | inl h => exact Or.inl (Chain.single h)
    | inr h => exact Or.inr ⟨Or.inl h.1, _, h.2, Or.inl rfl⟩
  | cons h hcb ih =>
    cases step_update db id l d h with
    | inl h' =>
      cases ih with
      | inl hc => exact Or.inl (Chain.cons h' hc)
      | inr hc =>
        obtain ⟨hcid, c, hcd, hcb'⟩ := hc
        exact Or.inr ⟨Or.inr ((Chain.single h').trans_refl hcid), c, hcd, hcb'⟩
    | inr h' => exact Or.inr ⟨Or.inl h'.1, _, h'.2, Or.inr hcb⟩

/-- M9: Updating a finding preserves Acyclic when the update checks pass and
    no new dep reaches the finding in the old graph. A cycle of the updated
    graph that uses no edge out of `id` is a cycle of the old graph; one that
    does yields, from its first such edge, a new dep that reaches `id` on old
    edges, or `id ∈ newDeps`. -/
theorem update_preserves_acyclic
    (db : FindingsDB) (id : Nat) (newLabel : MSSLabel) (newDeps : List Nat)
    (h_wf : Acyclic db)
    (h_check : UpdateCheckPasses db id newLabel newDeps)
    (h_no_back : ∀ depId ∈ newDeps, ¬ TransitiveDep db depId id)
    : Acyclic (db.update id newLabel newDeps) := by
  have h_self : id ∉ newDeps := h_check.2.2.2.2
  have hmono : ∀ {x y : Nat}, Chain (fun p q => p ≠ id ∧ Step db p q) x y → TransitiveDep db x y :=
    fun h => (transitiveDep_iff_chain db _ _).mpr (h.mono (fun _ _ hp => hp.2))
  have hback : ∀ c ∈ newDeps, ReflChain (fun p q => p ≠ id ∧ Step db p q) c id → False := by
    intro c hc hr
    cases hr with
    | inl hce => exact h_self (hce ▸ hc)
    | inr hr => exact h_no_back c hc (hmono hr)
  have hR_U : ∀ p q, (p ≠ id ∧ Step db p q) → Step (db.update id newLabel newDeps) p q :=
    fun p q hpq => step_update_of_old db id newLabel newDeps hpq.1 hpq.2
  intro x hx
  have hc := (transitiveDep_iff_chain _ x x).mp hx
  cases chain_update_split db id newLabel newDeps hc with
  | inl hR => exact h_wf x (hmono hR)
  | inr h =>
    obtain ⟨hxid, c, hcd, hcx⟩ := h
    have hcid : ReflChain (Step (db.update id newLabel newDeps)) c id :=
      hcx.trans (hxid.mono hR_U)
    cases hcid with
    | inl hce => exact h_self (hce ▸ hcd)
    | inr hcid =>
      cases chain_update_split db id newLabel newDeps hcid with
      | inl hR => exact hback c hcd (Or.inr hR)
      | inr h' => exact hback c hcd h'.1

end MSS
