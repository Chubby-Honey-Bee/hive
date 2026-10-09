/-
  MSS Invariants — Formal Specifications

  These are the structural properties that the WASP/CDE/MSS framework
  must satisfy. Enforced by the Go runtime in:
  - internal/db/findings.go AddFinding and internal/mss
    ValidateGuaranteeDeps (write-time)
  - internal/gate/gate.go RunGatePipeline (gate-time)
  - internal/db/mss_audit.go, behind Store.MSSAudit (audit-time)

  This file states them as Lean 4 propositions so they can be
  formally verified by Leanstral or any Lean 4 proof assistant.

  © 2026 Chubby Honey Bee Inc. — chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic

namespace MSS

/-! ## Invariant 1: No Direct Laundering

A guarantee may never directly depend on an unknown.
This is the primary MSS invariant, enforced at write-time
by `mss.ValidateGuaranteeDeps`.
-/

/-- No finding labeled `guarantee` has a direct dependency on a finding labeled `unknown`. -/
def NoDirectLaundering (db : FindingsDB) : Prop :=
  ∀ f ∈ db.findings,
    f.label = MSSLabel.guarantee →
    ∀ depId ∈ f.deps,
      ∀ dep : Finding, db.get depId = some dep →
        dep.label ≠ MSSLabel.unknown

/-! ## Invariant 2: No Untraceable Guarantees

Every guarantee must have at least one dependency.
A guarantee with no `depends_on_ids` is "untraceable" —
it claims provability but provides no derivation chain.
Refused at write-time by `FindingsRepo.AddFinding` and detected at
audit-time by `auditUntraceableGuarantees` (internal/db/mss_audit.go).
-/

/-- Every finding labeled `guarantee` has a non-empty dependency list. -/
def NoUntraceableGuarantees (db : FindingsDB) : Prop :=
  ∀ f ∈ db.findings,
    f.label = MSSLabel.guarantee → f.deps ≠ []

/-! ## Invariant 3: Dependency Chain Acyclicity

The dependency graph must be a DAG (directed acyclic graph).
If finding A depends on B, and B depends on C, then C must
not depend on A (directly or transitively).

The Go audit detects cycles at audit-time (`auditDependencyCycles`, a DFS
in internal/db/mss_audit.go), and a cycle fails the gate. This file states
the property; it is not proved here.
-/

/-- Transitive closure of the dependency relation.
    `TransitiveDep db a b` means `a` transitively depends on `b`. -/
inductive TransitiveDep (db : FindingsDB) : Nat → Nat → Prop where
  | direct : ∀ {a b}, ∀ f ∈ db.findings, f.id = a → b ∈ f.deps → TransitiveDep db a b
  | trans : ∀ {a b c}, TransitiveDep db a b → TransitiveDep db b c → TransitiveDep db a c

/-- The dependency graph is acyclic: no finding transitively depends on itself. -/
def Acyclic (db : FindingsDB) : Prop :=
  ∀ id, ¬ TransitiveDep db id id

/-! ## Invariant 4: No Transitive Laundering

The stronger form of Invariant 1: no guarantee may depend
on an unknown even *transitively* through a chain of other findings.

Write-time checks cover direct dependencies only (`ValidateGuaranteeDeps`);
the audit walks the whole chain (`auditLaundering`, a BFS in
internal/db/mss_audit.go), and the gate runs that audit. The transitive
property following from acyclicity plus no direct laundering is not proved
here.
-/

/-- No finding labeled `guarantee` transitively depends on any finding labeled `unknown`. -/
def NoTransitiveLaundering (db : FindingsDB) : Prop :=
  ∀ f ∈ db.findings,
    f.label = MSSLabel.guarantee →
    ∀ depId, TransitiveDep db f.id depId →
      ∀ dep : Finding, db.get depId = some dep →
        dep.label ≠ MSSLabel.unknown

/-! ## Not a theorem: Acyclicity + Direct No-Laundering ⟹ Transitive No-Laundering

The implication is false, so this file does not state it, not even as a
`sorry` that a downstream theorem could lean on.

Counterexample: a `guarantee` g with `deps = [a]`, an `assumption` a with
`deps = [u]`, and an `unknown` u. The graph is acyclic; no guarantee depends
*directly* on an unknown, so `NoDirectLaundering` holds; but
`TransitiveDep db g u` holds via `a`, so `NoTransitiveLaundering` fails.

Consequence: `NoTransitiveLaundering` is an independent invariant. It is not
derivable from the one-hop check and must be checked directly, which is what
the Go audit does (`internal/db` `MSSAudit`: a BFS over the dependency graph).
`WellFormed` carries it as its own field and `WellFormed.mk'` requires it as
a hypothesis.
-/

/-! ## Invariant 5: Dependency References Are Valid

Every ID in a finding's `deps` list must refer to an existing finding
in the database. Enforced at write-time by `ValidateGuaranteeDeps`.
-/

/-- All dependency references point to existing findings. -/
def DepsValid (db : FindingsDB) : Prop :=
  ∀ f ∈ db.findings,
    ∀ depId ∈ f.deps,
      depId ∈ db.ids

/-! ## Combined Well-Formedness

A database satisfies all MSS invariants simultaneously.
This is what `Store.MSSAudit` checks. No command runs Lean against a live
database; `lean4/bridge/verify-state.sh` does, by hand, for the decidable
invariants.
-/

/-- A database satisfies all MSS structural invariants. -/
structure WellFormed (db : FindingsDB) : Prop where
  no_laundering : NoDirectLaundering db
  no_untraceable : NoUntraceableGuarantees db
  acyclic : Acyclic db
  deps_valid : DepsValid db
  -- Independent of the four above (see § Not a theorem): checked
  -- directly by the audit's transitive BFS.
  no_transitive_laundering : NoTransitiveLaundering db

/-- A well-formed database is constructed from all five invariants. Transitive
    no-laundering must be supplied; it does not follow from the others. -/
theorem WellFormed.mk'
    (db : FindingsDB)
    (h1 : NoDirectLaundering db)
    (h2 : NoUntraceableGuarantees db)
    (h3 : Acyclic db)
    (h4 : DepsValid db)
    (h5 : NoTransitiveLaundering db)
    : WellFormed db :=
  { no_laundering := h1
    no_untraceable := h2
    acyclic := h3
    deps_valid := h4
    no_transitive_laundering := h5 }

end MSS
