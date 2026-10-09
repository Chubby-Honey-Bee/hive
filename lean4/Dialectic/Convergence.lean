/-
  Hegelian Dialectic — Convergence Scoring (Layer 4)

  Convergence measures how strongly multiple agents agree.
  From internal/gate/merge.go (`chb swarm-merge` passes 3 and 2):
    effective_agents = min(num_agents, source_diversity), at least 1
    high = effective_agents >= 3
    medium = effective_agents >= 2
    low = otherwise
  The model below takes min(num_agents, max(source_diversity, 1)), which
  differs from the Go only when num_agents is 0 (model 0, Go 1).

  Key theorems:
  - D1: Convergence level is monotone in effective agents
  - D2: Effective agents bounded by num_agents
  - D3: High convergence requires at least 3 effective agents

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import CDE.Basic

namespace Dialectic

/-! ## Convergence Scoring Function

Mirrors the convergence logic in internal/gate/merge.go.
-/

/-- Compute effective agent count.
    Weights by source diversity: 3 agents citing 1 source ≠ true convergence.
    Matches: `effective_agents = min(num_agents, max(source_diversity, 1))` -/
def effectiveAgents (numAgents : Nat) (sourceDiversity : Nat) : Nat :=
  min numAgents (max sourceDiversity 1)

/-- Determine convergence level from effective agent count.
    Matches the thresholds `chb swarm-merge` passes (3, 2). -/
def convergenceFromEffective (effective : Nat) : CDE.ConvergenceLevel :=
  if effective >= 3 then .high
  else if effective >= 2 then .medium
  else .low

/-- Full convergence scoring: agents + source diversity → level. -/
def convergenceLevel (numAgents : Nat) (sourceDiversity : Nat) : CDE.ConvergenceLevel :=
  convergenceFromEffective (effectiveAgents numAgents sourceDiversity)

/-! ## D1: Convergence is Monotone

More effective agents → higher or equal convergence level.
-/

/-- D1: Convergence level is monotone in effective agent count. -/
theorem convergence_monotone (a b : Nat) (h : a ≤ b) :
    (convergenceFromEffective a).toNat ≤ (convergenceFromEffective b).toNat := by
  unfold convergenceFromEffective
  repeat' split
  all_goals (try simp [CDE.ConvergenceLevel.toNat])
  all_goals omega

/-! ## D2: Effective Agents Bounded

The min operation ensures effective_agents ≤ num_agents.
-/

/-- D2: Effective agents never exceeds the number of actual agents. -/
theorem effective_agents_bounded (numAgents sourceDiversity : Nat) :
    effectiveAgents numAgents sourceDiversity ≤ numAgents := by
  simp [effectiveAgents]
  exact Nat.min_le_left numAgents _

/-! ## D3: High Requires Three

High convergence requires at least 3 effective agents.
-/

/-- D3: High convergence implies at least 3 effective agents. -/
theorem high_requires_three (n : Nat) :
    convergenceFromEffective n = .high → n ≥ 3 := by
  unfold convergenceFromEffective
  intro h
  split at h
  · assumption
  · split at h <;> simp at h

end Dialectic
