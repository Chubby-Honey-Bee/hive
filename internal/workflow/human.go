package workflow

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// PauseForHuman parks a human_review node and pauses its run. The engine
// treats the node as unfinished, so successors stay pending and the
// dispatcher stops; ResumeHumanReview completes the node and returns the
// run to `running`.
func PauseForHuman(repo Store, runID int64, nodeName string) error {
	if err := repo.MarkNodeWaitingHuman(runID, nodeName, timestamp()); err != nil {
		return err
	}
	return repo.MarkRunPaused(runID)
}

// AnswerHumanReview records a person's answer to a parked human_review node
// in one write transaction: `human_decision` and `human_feedback` merge into
// state and become the node's outputs, the node's state_updates apply when
// status is completed, and the node takes status (completed or failed).
func AnswerHumanReview(repo Store, runID int64, nodeName, status, decision, feedback string) error {
	answer := map[string]any{"human_decision": decision, "human_feedback": feedback}
	return repo.FinishNodeInTx(runID, nodeName, status, func(run *db.WorkflowRun) (string, string, error) {
		defn, err := runDefinition(run, "state_updates")
		if err != nil {
			return "", "", err
		}
		state := decodeState(run.StateJSON)
		maps.Copy(state, answer)
		if status == "completed" {
			applyStateUpdatesEngine(defn, nodeName, state)
		}
		o, _ := json.Marshal(answer)
		st, _ := json.Marshal(state)
		return string(o), string(st), nil
	})
}

// ResumeHumanReview answers the node of a run that waits for a human
// decision, as `chb workflow resume` does: approve and redirect complete it,
// reject fails it. The answer merges into state as human_decision and
// human_feedback in the same transaction (AnswerHumanReview), and the run
// goes back to running. Any other decision is refused before anything is
// written, since a failed node cannot be undone.
func ResumeHumanReview(repo Store, runID int64, decision, feedback string) error {
	outcome, err := humanReviewOutcome(decision)
	if err != nil {
		return err
	}
	nodeName, err := waitingHumanNode(repo, runID)
	if err != nil {
		return err
	}
	if err := AnswerHumanReview(repo, runID, nodeName, outcome, decision, feedback); err != nil {
		return err
	}
	return repo.MarkRunRunning(runID)
}

// humanReviewOutcome is the node outcome a human decision answers with:
// approve and redirect complete the node, reject fails it, and any other
// decision is refused.
func humanReviewOutcome(decision string) (string, error) {
	switch decision {
	case "approve", "redirect":
		return "completed", nil
	case "reject":
		return "failed", nil
	}
	return "", fmt.Errorf("decision must be approve, reject or redirect, got %q", decision)
}

// waitingHumanNode is the node of the run that waits for a human decision.
func waitingHumanNode(repo Store, runID int64) (string, error) {
	nodeName, err := repo.FindWaitingHumanNode(runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no nodes waiting for human review on run %d", runID)
	}
	return nodeName, err
}
