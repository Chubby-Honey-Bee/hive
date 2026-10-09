/-
  MSS (Minimally Sufficient Statistics) — Core Types

  The WASP/CDE/MSS framework labels every statement with one of four types:
  - definition:  a choice we made (threshold, convention, schema decision)
  - guarantee:   provable from definitions + assumptions upstream
  - assumption:  an explicit bet that could be wrong
  - unknown:     honest gap — must NOT be used as if it were a guarantee

  The fundamental invariant: no guarantee may reference an unknown,
  either directly or transitively. This ensures the provenance chain
  is structurally sound — if you trace any guarantee back to its roots,
  you reach only definitions and assumptions, never unknowns.

  © 2026 Chubby Honey Bee Inc. — chronomancy.io WASP/CDE/MSS framework
-/

namespace MSS

/-- The four MSS labels. Ordered by epistemic strength. -/
inductive MSSLabel where
  | definition
  | guarantee
  | assumption
  | unknown
  deriving DecidableEq, Repr

/-- A finding in the workspace database (`hive.db`). -/
structure Finding where
  /-- Unique identifier (matches SQLite rowid) -/
  id : Nat
  /-- MSS label classifying this finding -/
  label : MSSLabel
  /-- IDs of findings this one depends on (JSON array in SQLite) -/
  deps : List Nat
  deriving Repr

/-- A findings database: a finite list of findings with unique IDs. -/
structure FindingsDB where
  findings : List Finding
  /-- All IDs are unique -/
  ids_unique : findings.map Finding.id |>.Nodup

/-- Look up a finding by ID. -/
def FindingsDB.get (db : FindingsDB) (id : Nat) : Option Finding :=
  db.findings.find? (fun f => f.id == id)

/-- Get the label of a finding by ID, if it exists. -/
def FindingsDB.labelOf (db : FindingsDB) (id : Nat) : Option MSSLabel :=
  (db.get id).map Finding.label

/-- All dependency IDs referenced by a finding. -/
def FindingsDB.depsOf (db : FindingsDB) (id : Nat) : List Nat :=
  match db.get id with
  | some f => f.deps
  | none => []

/-- The set of all finding IDs in the database. -/
def FindingsDB.ids (db : FindingsDB) : List Nat :=
  db.findings.map Finding.id

end MSS
