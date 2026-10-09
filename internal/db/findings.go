package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/cde"
	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// Finding represents a CDE-encoded finding with MSS label.
type Finding struct {
	ID               int64
	Wave             int
	Agent            string
	D1, D2, D3, D4   *int
	D5, D6, D7, D8   *int
	MSSLabel         string
	Finding          string
	Evidence         *string
	SourceURLs       *string
	ConvergenceCount int
	ConvergenceLevel string
	DependsOnIDs     *string // JSON array
	CreatedAt        string
}

// CascadeRevert delegates to mss.RunCascade. Lives on Store rather than
// FindingsRepo because the BFS writes to findings + gaps + signals (a
// cross-table operation). The cascade primitives themselves live in
// internal/db/cascade.go.
func (s *Store) CascadeRevert(findingID int64) ([]int64, error) {
	return mss.RunCascade(s, findingID)
}

// FindingsRepo owns CRUD + queries for the findings table.
type FindingsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newFindingsRepo binds the two pools. Production wiring happens inside
// NewStore so the Store-level facade keeps working unchanged.
func newFindingsRepo(writeDB, readDB *sql.DB) *FindingsRepo {
	return &FindingsRepo{writeDB: writeDB, readDB: readDB}
}

// AddFinding writes a CDE-encoded finding with MSS enforcement.
func (r *FindingsRepo) AddFinding(f *Finding) (int64, error) {
	coords := cde.Coords{f.D1, f.D2, f.D3, f.D4, f.D5, f.D6, f.D7, f.D8}
	if err := cde.ValidateCoords(r.readDB, coords); err != nil {
		return 0, err
	}
	label := mss.Label(f.MSSLabel)
	if err := r.checkNewFinding(label, f); err != nil {
		return 0, err
	}
	warnUnsourced(label, f)
	return r.insertFinding(f)
}

// checkNewFinding refuses a label outside the partition and dependencies
// the label cannot rest on.
func (r *FindingsRepo) checkNewFinding(label mss.Label, f *Finding) error {
	if !label.Valid() {
		return fmt.Errorf("invalid MSS label: %s", f.MSSLabel)
	}
	if label == mss.Guarantee {
		return r.checkNewGuaranteeDeps(f.DependsOnIDs)
	}
	// Every label's dependencies must exist, not only a guarantee's
	// (Lean: WriteCheckPasses asks it of every label).
	return r.checkDepsExist(0, label, parseDeps(f.DependsOnIDs))
}

// checkNewGuaranteeDeps refuses a guarantee with no dependencies, and one
// whose dependencies do not validate.
func (r *FindingsRepo) checkNewGuaranteeDeps(deps *string) error {
	if noDeps(deps) {
		return &mss.MSSError{
			Label:  mss.Guarantee,
			Reason: "guarantee requires non-empty depends_on_ids",
			Err:    mss.ErrMissingDeps,
		}
	}
	return mss.ValidateGuaranteeDeps(r.readDB, 0, *deps)
}

// noDeps reports whether depends_on_ids is absent or names no dependency:
// NULL, empty, "null" or "[]".
func noDeps(deps *string) bool {
	return deps == nil || *deps == "" || *deps == "null" || *deps == "[]"
}

// checkDepsExist refuses dependencies that are not findings; no
// dependencies is nothing to check.
func (r *FindingsRepo) checkDepsExist(findingID int64, label mss.Label, deps []int64) error {
	if len(deps) == 0 {
		return nil
	}
	return mss.ValidateDepsExist(r.readDB, findingID, label, deps)
}

// warnUnsourced warns of a guarantee or assumption with no source_urls.
// Stderr — never stdout — because callers like internal/mcp use stdout as
// the JSON-RPC channel.
func warnUnsourced(label mss.Label, f *Finding) {
	if unsourced(f.SourceURLs) && (label == mss.Guarantee || label == mss.Assumption) {
		fmt.Fprintf(os.Stderr, "  WARNING: %s finding has no source_urls: %.60s...\n", f.MSSLabel, f.Finding)
	}
}

// unsourced reports whether source_urls is absent or empty.
func unsourced(sourceURLs *string) bool {
	return sourceURLs == nil || *sourceURLs == ""
}

// insertFinding writes the finding and returns its id.
func (r *FindingsRepo) insertFinding(f *Finding) (int64, error) {
	res, err := r.writeDB.Exec(
		`INSERT INTO findings
		 (wave, agent, d1, d2, d3, d4, d5, d6, d7, d8, mss_label, finding, evidence, source_urls, depends_on_ids)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.Wave, f.Agent, f.D1, f.D2, f.D3, f.D4, f.D5, f.D6, f.D7, f.D8,
		f.MSSLabel, f.Finding, f.Evidence, f.SourceURLs, f.DependsOnIDs,
	)
	if err != nil {
		return 0, fmt.Errorf("insert finding: %w", err)
	}
	return res.LastInsertId()
}

// UpdateFinding updates an existing finding with full MSS validation. A
// finding that is or becomes a guarantee has its dependencies validated and
// cycle-checked; a finding of any label has its new dependencies checked to
// exist; and a relabel to unknown is refused while a guarantee rests on the
// finding, directly or through a chain of any labels, because the write would
// launder those guarantees. The error names them and the cascade that reverts
// them first, by its own writes (`chb db-write cascade_revert <id>`).
//
// Lean4: M8 `update_preserves_no_laundering` takes that last condition as a
// premise, and M9 `update_preserves_acyclic` takes the cycle check's.
func (r *FindingsRepo) UpdateFinding(findingID int64, updates map[string]any) error {
	stored, err := r.storedLabelAndDeps(findingID)
	if err != nil {
		return err
	}
	newLabel, err := updatedLabel(stored.label.String, updates)
	if err != nil {
		return err
	}
	if err := r.checkUpdate(findingID, stored, newLabel, updates); err != nil {
		return err
	}
	return r.writeUpdate(findingID, updates)
}

// storedFinding is the label and dependency list a finding has before an
// update.
type storedFinding struct {
	label, deps sql.NullString
}

// storedLabelAndDeps reads a finding's label and dependency list.
func (r *FindingsRepo) storedLabelAndDeps(findingID int64) (storedFinding, error) {
	var s storedFinding
	err := r.readDB.QueryRow(
		"SELECT mss_label, depends_on_ids FROM findings WHERE id=?", findingID,
	).Scan(&s.label, &s.deps)
	if err != nil {
		return s, fmt.Errorf("finding %d not found: %w", findingID, err)
	}
	return s, nil
}

// updatedLabel is the label an update leaves: the one it gives, which must
// be a string, else the current one.
func updatedLabel(current string, updates map[string]any) (string, error) {
	v, ok := updates["mss_label"]
	if !ok {
		return current, nil
	}
	label, isString := v.(string)
	if !isString {
		return "", fmt.Errorf("updates[\"mss_label\"] must be a string, got %T", v)
	}
	return label, nil
}

// checkUpdate runs the MSS checks an update must pass: no relabel to
// unknown under a guarantee, and dependencies the new label can rest on,
// without a cycle.
func (r *FindingsRepo) checkUpdate(findingID int64, stored storedFinding, newLabel string, updates map[string]any) error {
	if err := r.checkUnknownRelabel(findingID, stored.label.String, newLabel); err != nil {
		return err
	}
	deps, err := updatedDeps(stored.deps.String, updates)
	if err != nil {
		return err
	}
	return r.checkUpdatedDeps(findingID, mss.Label(newLabel), deps)
}

// relabelsToUnknown reports whether an update turns a finding that is not
// unknown into one.
func relabelsToUnknown(oldLabel, newLabel string) bool {
	return newLabel == string(mss.Unknown) && oldLabel != string(mss.Unknown)
}

// checkUnknownRelabel refuses a relabel to unknown while a guarantee rests
// on the finding.
func (r *FindingsRepo) checkUnknownRelabel(findingID int64, oldLabel, newLabel string) error {
	if !relabelsToUnknown(oldLabel, newLabel) {
		return nil
	}
	resting, err := mss.GuaranteesRestingOn(r.readDB, findingID)
	if err != nil {
		return err
	}
	if len(resting) > 0 {
		return &mss.MSSError{
			FindingID: findingID,
			Label:     mss.Unknown,
			Reason:    restingReason(resting, findingID),
			Err:       mss.ErrLaundering,
		}
	}
	return nil
}

// depsUpdate is the dependency list an update leaves a finding with: its
// JSON, the ids it names, and whether the update gave it.
type depsUpdate struct {
	json  string
	ids   []int64
	given bool
}

// updatedDeps is the dependency list an update leaves: the one it gives,
// else the stored one.
func updatedDeps(stored string, updates map[string]any) (depsUpdate, error) {
	depsJSON, given := stored, false
	if v, ok := updates["depends_on_ids"]; ok {
		var err error
		if depsJSON, given, err = depsFromUpdate(stored, v); err != nil {
			return depsUpdate{}, err
		}
	}
	return depsUpdate{json: depsJSON, ids: parseDeps(&depsJSON), given: given}, nil
}

// depsFromUpdate reads a given depends_on_ids: nil clears the list (so the
// empty list is validated, not the old one), a string is the JSON, and
// []int64 is marshalled. Any other value leaves the stored list.
func depsFromUpdate(stored string, v any) (string, bool, error) {
	switch d := v.(type) {
	case nil:
		return "", true, nil
	case string:
		return d, true, nil
	case []int64:
		depsJSON, err := marshalDeps(d)
		return depsJSON, true, err
	}
	return stored, false, nil
}

// marshalDeps is a dependency list as JSON.
func marshalDeps(ids []int64) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("marshal depends_on_ids: %w", err)
	}
	return string(b), nil
}

// checkUpdatedDeps validates the dependencies an update leaves and checks
// them for a cycle.
func (r *FindingsRepo) checkUpdatedDeps(findingID int64, label mss.Label, deps depsUpdate) error {
	if err := r.validateUpdatedDeps(findingID, label, deps); err != nil {
		return err
	}
	if !cycleCheckDue(label, deps) {
		return nil
	}
	return mss.CheckCycle(r.readDB, findingID, label, deps.ids)
}

// validateUpdatedDeps validates a guarantee's dependencies, and checks that
// the dependencies an update gives any other label exist.
func (r *FindingsRepo) validateUpdatedDeps(findingID int64, label mss.Label, deps depsUpdate) error {
	if label == mss.Guarantee {
		return mss.ValidateGuaranteeDeps(r.readDB, findingID, deps.json)
	}
	if !deps.given {
		return nil
	}
	return r.checkDepsExist(findingID, label, deps.ids)
}

// cycleCheckDue reports whether the dependencies must be checked for a
// cycle. M9's premise holds for every label: a changed dependency list may
// not reach the finding. A guarantee is checked on every validation, as its
// dependencies are.
func cycleCheckDue(label mss.Label, deps depsUpdate) bool {
	return len(deps.ids) > 0 && (deps.given || label == mss.Guarantee)
}

// updatableColumns are the findings columns an update may set, in the
// order the UPDATE names them.
var updatableColumns = []string{"mss_label", "finding", "evidence", "source_urls", "depends_on_ids"}

// writeUpdate writes the columns the update names.
func (r *FindingsRepo) writeUpdate(findingID int64, updates map[string]any) error {
	setClauses, args, err := updateSet(updates)
	if err != nil {
		return err
	}
	// No recognised column means nothing was asked for, and stamping
	// updated_at on its own would claim a change that did not happen.
	if len(args) == 0 {
		return nil
	}
	args = append(args, findingID)
	_, err = r.writeDB.Exec(
		fmt.Sprintf("UPDATE findings SET %s WHERE id = ?", setClauses),
		args...,
	)
	return err
}

// updateSet is the UPDATE's SET clause for the columns the update names, and
// their values. updated_at is always stamped, so "has this dependency
// changed since the guarantee was proven?" is answerable.
func updateSet(updates map[string]any) (string, []any, error) {
	setClauses := "updated_at = CURRENT_TIMESTAMP"
	var args []any
	for _, col := range updatableColumns {
		v, ok := updates[col]
		if !ok {
			continue
		}
		arg, err := columnArg(col, v)
		if err != nil {
			return "", nil, err
		}
		setClauses += ", " + col + " = ?"
		args = append(args, arg)
	}
	return setClauses, args, nil
}

// columnArg is the value stored for a column: a []int64 depends_on_ids as
// JSON, anything else as given.
func columnArg(col string, v any) (any, error) {
	ids, isIDs := v.([]int64)
	if col != "depends_on_ids" || !isIDs {
		return v, nil
	}
	return marshalDeps(ids)
}

// parseDeps is the id list a stored depends_on_ids names: none when the
// column is NULL, empty, or not a JSON array, as every reader treats it.
func parseDeps(depsJSON *string) []int64 {
	if depsJSON == nil || *depsJSON == "" {
		return nil
	}
	var deps []int64
	if json.Unmarshal([]byte(*depsJSON), &deps) != nil {
		return nil
	}
	return deps
}

// restingReason names the guarantees a relabel to unknown would launder and
// the command that reverts them first.
func restingReason(resting []int64, findingID int64) string {
	ids := make([]string, len(resting))
	for i, id := range resting {
		ids[i] = fmt.Sprintf("%d", id)
	}
	noun, verb, pronoun := "guarantees", "rest", "them"
	if len(resting) == 1 {
		noun, verb, pronoun = "guarantee", "rests", "it"
	}
	return fmt.Sprintf("%s %s %s on it; revert %s first: chb db-write cascade_revert %d",
		noun, strings.Join(ids, ", "), verb, pronoun, findingID)
}

// promoteFindingMarshal is the json.Marshal function used by PromoteFinding.
// Overridable in tests to exercise the otherwise-unreachable error branch.
var promoteFindingMarshal = json.Marshal

// PromoteFinding relabels a finding a guarantee resting on dependsOnIDs.
// Nothing calls it on convergence: agreement is not a derivation.
func (r *FindingsRepo) PromoteFinding(findingID int64, dependsOnIDs []int64) error {
	b, err := promoteFindingMarshal(dependsOnIDs)
	if err != nil {
		return fmt.Errorf("marshal depends_on_ids: %w", err)
	}
	return r.UpdateFinding(findingID, map[string]any{
		"mss_label":      string(mss.Guarantee),
		"depends_on_ids": string(b),
	})
}
