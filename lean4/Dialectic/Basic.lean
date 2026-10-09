/-
  Hegelian Dialectic — Core Types (Layer 4)

  The dialectic layer formalizes the knowledge production process:
  thesis (agent finding) → antithesis (contradiction/refinement) → synthesis.

  This is implemented operationally by:
  - Forager swarm (`chb ask`): lenses → queen synthesis → coverage evaluation
  - Scouts workflow (workflows/scouts.yaml): batch → variance-pass → merge →
    evaluate → gap-fill
  - Conflict detection: numeric divergence, MSS label mismatch, negation

  This file defines the types. Theorems are in Convergence, Gate, Preservation.

  Source of truth (the Go implementation this models):
  - internal/foragers/workflow.go (swarm workflow generation)
  - internal/gate/merge.go (convergence merge)
  - internal/gate/conflict.go (conflict detection)
  - internal/gate/gate.go (the wave gate)

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import MSS.Basic
import CDE.Basic

namespace Dialectic

/-! ## Evaluation Verdicts

The evaluator agent produces a verdict determining whether
the dialectic cycle continues or terminates.
-/

/-- Evaluation verdict from the evaluator agent. -/
inductive EvalVerdict where
  | complete           -- all questions answered, ready for synthesis
  | needsMoreWork      -- significant gaps remain, spawn another wave
  | needsMinorFollowup -- minor gaps, conditional proceed
  deriving DecidableEq, Repr

/-! ## Conflict Types

From internal/gate/conflict.go: three types of dialectic opposition.
-/

/-- Types of conflict detected between findings. -/
inductive ConflictType where
  | numericDivergence   -- values differ by >20% in same context
  | mssLabelMismatch    -- guarantee vs assumption at same coordinate
  | negationConflict    -- one asserts, other negates same subject
  deriving DecidableEq, Repr

/-- A conflict between two findings. -/
structure Conflict where
  findingA : Nat  -- ID of first finding
  findingB : Nat  -- ID of second finding
  conflictType : ConflictType
  resolved : Bool
  deriving DecidableEq, Repr

/-! ## Wave Gate Prerequisites

The wave gate (internal/gate/gate.go) checks these four before a wave
opens, plus a source check this model leaves out.
-/

/-- Prerequisites for opening a wave gate (allowing synthesis export). -/
structure GatePrereqs where
  agentsCompleted : Bool      -- all agent_runs have status='completed'
  evaluationPassed : Bool     -- evaluation verdict is not NEEDS_MORE_WORK
  conflictsResolved : Bool    -- no unresolved conflicts remain
  mssAuditClean : Bool        -- mss_audit() returns integrity='PASS'
  deriving DecidableEq, Repr

/-- Gate opens iff ALL four prerequisites are met. -/
def gateOpens (g : GatePrereqs) : Bool :=
  g.agentsCompleted && g.evaluationPassed && g.conflictsResolved && g.mssAuditClean

/-! ## Workflow Structure

Workflows are DAGs of nodes. The forager swarm and the scouts
workflow are specific workflow topologies.
-/

/-- Workflow node types, as internal/workflow/validate.go validates them. -/
inductive NodeType where
  | agent        -- runs an AI agent (researcher, analyst, evaluator)
  | decision     -- conditional branch based on state
  | parallelFan  -- spawns N parallel agents
  | humanReview  -- pauses for human input
  | command      -- runs a program, with no model
  | calibrate    -- recomputes the calibration scores, with no model
  deriving DecidableEq, Repr

/-- Node status in the workflow state machine. -/
inductive NodeStatus where
  | pending | running | completed | failed | skipped | waitingHuman | rejected
  deriving DecidableEq, Repr

/-- A directed edge in the workflow graph. -/
structure WorkflowEdge where
  from_ : String  -- source node name
  to_ : String    -- target node name
  deriving DecidableEq, Repr

/-- A workflow graph: nodes and edges forming a DAG. -/
structure WorkflowGraph where
  nodeNames : List String
  edges : List WorkflowEdge
  deriving Repr

end Dialectic
