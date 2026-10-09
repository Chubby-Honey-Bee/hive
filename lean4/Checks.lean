/-
  Checks — the Lean CI gate

  Run after `lake build`, as `lake env lean Checks.lean` from this
  directory. Not a library target: `lake build` never compiles it, so it
  cannot itself count as a proof.

  It imports every module of the five libraries and walks the environment,
  so it reads elaborated statements and axiom sets rather than source text:

  (a) a theorem declared in one of the five libraries whose conclusion,
      under its binders, is `True` fails the check. Such a theorem proves
      nothing, whatever its body is, and there is no allowlist for it.
  (b) a declaration that depends on `sorryAx` fails the check unless its
      module is named in `LEAN_SORRY_ALLOW` (comma-separated module names,
      set by .github/workflows/lean.yml). That list only shrinks.

  Every `sorry` it finds is printed, allowed or not, so the log is the report.

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import Lean
import Fitch.Basic
import Fitch.Reduction
import Fitch.Soundness
import Fitch.Instances
import MSS.Basic
import MSS.Invariants
import MSS.Paths
import MSS.Decidability
import MSS.Preservation
import MSS.FitchBridge
import CDE.Basic
import CDE.Encoding
import CDE.Index
import WASP.Basic
import WASP.BoundedWork
import WASP.Exactness
import WASP.NullSafety
import WASP.Sufficiency
import Dialectic.Basic
import Dialectic.Convergence
import Dialectic.Gate
import Dialectic.Preservation

open Lean Meta

namespace Checks

/-- The five libraries of lakefile.lean, by module root. -/
def libraries : List String := ["Fitch", "MSS", "CDE", "WASP", "Dialectic"]

def isProjectModule (m : Name) : Bool :=
  libraries.contains m.getRoot.toString

/-- The conclusion of a theorem's type under its binders. -/
def conclusionIsTrue (type : Expr) : MetaM Bool :=
  forallTelescope type fun _ body => pure (body.isConstOf ``True)

def allowlist : IO (List String) := do
  let raw := (← IO.getEnv "LEAN_SORRY_ALLOW").getD ""
  return (raw.splitOn ",").map String.trim |>.filter (· ≠ "")

def run : MetaM Unit := do
  let env ← getEnv
  let allow ← allowlist
  let mut trueConcl : Array (Name × Name) := #[]
  let mut sorried : Array (Name × Name) := #[]
  let mut theorems : Array (Name × Name) := #[]
  let mut count : Nat := 0
  for (n, ci) in env.constants.map₁.toList do
    let some idx := env.getModuleIdxFor? n | continue
    let mod := env.header.moduleNames[idx.toNat]!
    unless isProjectModule mod do continue
    count := count + 1
    if ci matches .thmInfo _ then
      if ← conclusionIsTrue ci.type then
        trueConcl := trueConcl.push (n, mod)
      -- named theorems only: no `proof_n`, `match_n`, equation or private-prefix details
      unless n.isInternalDetail do
        theorems := theorems.push (n, mod)
    let axs ← collectAxioms n
    if axs.contains ``sorryAx then
      sorried := sorried.push (n, mod)
  let byName := fun (a b : Name × Name) => decide (a.1.toString < b.1.toString)
  trueConcl := trueConcl.qsort byName
  sorried := sorried.qsort byName
  let perLib := fun (xs : Array (Name × Name)) =>
    libraries.map fun lib => s!"{lib}={(xs.filter (fun p => p.2.getRoot.toString == lib)).size}"
  IO.println s!"Lean gate: {count} declarations across {libraries}"
  IO.println s!"theorem declarations per library, compiler-derived ones included: {perLib theorems} (total {theorems.size})"
  IO.println s!"sorry per library: {perLib sorried}"
  if allow.isEmpty then
    IO.println "sorry allowlist: empty (a sorry anywhere fails)"
  else
    IO.println s!"sorry allowlist: {allow}"
  for (n, m) in sorried do
    IO.println s!"sorry: {m} {n}"
  let bad := sorried.filter fun (_, m) => !allow.contains m.toString
  for (n, m) in trueConcl do
    IO.println s!"FAIL conclusion is True: {m} {n}"
  for (n, m) in bad do
    IO.println s!"FAIL sorry outside the allowlist: {m} {n}"
  unless trueConcl.isEmpty && bad.isEmpty do
    throwError "Lean gate failed: {trueConcl.size} theorem(s) conclude True, {bad.size} sorry outside the allowlist"
  IO.println "Lean gate passed"

end Checks

#eval Checks.run
