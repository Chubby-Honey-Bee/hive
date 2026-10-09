import Lake
open Lake DSL

package hive_proofs where
  leanOptions := #[
    ⟨`autoImplicit, false⟩
  ]

-- Each lib globs its directory's submodules. Module names are relative to
-- the package root (default srcDir "."), so `import MSS.Basic` resolves to
-- MSS/Basic.lean and the lib globs MSS.* — there is no MSS/MSS.lean root.

-- The import graph, as the sources have it: Fitch and MSS import nothing
-- (MSS does not build on Fitch — MSS/FitchBridge.lean argues the
-- correspondence in prose); CDE imports MSS; WASP imports CDE (only
-- WASP.NullSafety); Dialectic imports MSS and CDE.

-- Fitch Reduction — no dependencies; nothing imports it
@[default_target]
lean_lib Fitch where
  globs := #[.submodules `Fitch]

-- MSS — epistemic labels (no dependencies)
@[default_target]
lean_lib MSS where
  globs := #[.submodules `MSS]

-- CDE — dimensional encoding (imports MSS)
@[default_target]
lean_lib CDE where
  globs := #[.submodules `CDE]

-- WASP — query semantics (WASP.NullSafety imports CDE)
@[default_target]
lean_lib WASP where
  globs := #[.submodules `WASP]

-- Dialectic — knowledge production (imports MSS, CDE)
@[default_target]
lean_lib Dialectic where
  globs := #[.submodules `Dialectic]
