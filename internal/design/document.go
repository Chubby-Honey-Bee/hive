package design

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// Document is what the plan call returns on either arm: the design, its
// labelled claims and the plan the executor follows.
type Document struct {
	Design string  `json:"design"`
	Claims []Claim `json:"claims"`
	Plan   []Step  `json:"plan"`
}

// Claim is one load-bearing claim of a design, with its MSS label and the
// indexes (from 0) of the earlier claims it rests on.
type Claim struct {
	Label   string `json:"label"`
	Claim   string `json:"claim"`
	RestsOn []int  `json:"rests_on"`
}

// Step is one step of the plan.
type Step struct {
	Step   int      `json:"step"`
	Action string   `json:"action"`
	Files  []string `json:"files"`
}

// Report is what the executor returns: the steps it did, the ones it did
// otherwise or not at all, what it did beyond the plan, and a summary.
type Report struct {
	StepsDone  []int       `json:"steps_done"`
	Deviations []Deviation `json:"deviations"`
	Additions  []string    `json:"additions"`
	Summary    string      `json:"summary"`
}

// Deviation is a step the executor did differently or skipped, and why.
type Deviation struct {
	Step int    `json:"step"`
	Why  string `json:"why"`
}

// DocumentSchemaJSON is the plan call's enforced output schema.
const DocumentSchemaJSON = `{"type":"object","required":["design","claims","plan"],"properties":{` +
	`"design":{"type":"string","minLength":1},` +
	`"claims":{"type":"array","minItems":1,"items":{"type":"object","required":["label","claim","rests_on"],"properties":{` +
	`"label":{"type":"string","enum":["definition","guarantee","assumption","unknown"]},` +
	`"claim":{"type":"string","minLength":1},` +
	`"rests_on":{"type":"array","items":{"type":"integer"}}}}},` +
	`"plan":{"type":"array","minItems":1,"items":{"type":"object","required":["step","action","files"],"properties":{` +
	`"step":{"type":"integer"},"action":{"type":"string","minLength":1},"files":{"type":"array","items":{"type":"string"}}}}}}}`

// ReportSchemaJSON is the executor's enforced output schema.
const ReportSchemaJSON = `{"type":"object","required":["steps_done","deviations","additions","summary"],"properties":{` +
	`"steps_done":{"type":"array","items":{"type":"integer"}},` +
	`"deviations":{"type":"array","items":{"type":"object","required":["step","why"],"properties":{"step":{"type":"integer"},"why":{"type":"string"}}}},` +
	`"additions":{"type":"array","items":{"type":"string"}},` +
	`"summary":{"type":"string"}}}`

// ParseDocument reads a node's decoded outputs as a Document. A document
// with no plan step or no claim is an error: the schema requires both, and
// a run that got past it with neither has nothing to execute or to audit.
func ParseDocument(outputs map[string]any) (Document, error) {
	var d Document
	if err := decodeInto(outputs, &d); err != nil {
		return d, err
	}
	if !d.complete() {
		return d, fmt.Errorf("the document lacks a design, a plan step or a claim")
	}
	return d, nil
}

// complete: the document has a design, a plan step and a claim.
func (d Document) complete() bool {
	return strings.TrimSpace(d.Design) != "" && len(d.Plan) > 0 && len(d.Claims) > 0
}

// ParseReport reads a node's decoded outputs as a Report.
func ParseReport(outputs map[string]any) (Report, error) {
	var r Report
	err := decodeInto(outputs, &r)
	return r, err
}

// decodeInto decodes a node's outputs into v by way of JSON.
func decodeInto(outputs map[string]any, v any) error {
	raw, err := json.Marshal(outputs)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// Fidelity is the plan's steps, the deviations the executor recorded, the
// additions it made, and the deviation rate (deviations + additions) ÷
// steps, nil when the plan has no step.
type Fidelity struct {
	Steps      int      `json:"plan_steps"`
	Deviations int      `json:"deviations"`
	Additions  int      `json:"additions"`
	Rate       *float64 `json:"deviation_rate"`
}

// PlanFidelity reads fidelity from the plan and the executor's report. A
// deviation naming a step the plan lacks still counts: the executor
// departed from the plan it was given.
func PlanFidelity(plan []Step, r Report) Fidelity {
	f := Fidelity{Steps: len(plan), Deviations: len(r.Deviations), Additions: len(r.Additions)}
	if f.Steps > 0 {
		rate := float64(f.Deviations+f.Additions) / float64(f.Steps)
		f.Rate = &rate
	}
	return f
}

// Coverage is the files the executor changed, how many a plan step named,
// and the fraction, nil when nothing changed.
type Coverage struct {
	Changed int      `json:"files_changed"`
	Named   int      `json:"files_named"`
	Rate    *float64 `json:"file_coverage"`
}

// FileCoverage compares the paths the executor changed (added, removed or
// edited, relative to the tree, slash-separated) with the files the plan's
// steps name. A step's file matches a changed path when the two are equal
// once cleaned, or when the step names a directory the path is under.
func FileCoverage(plan []Step, changed []string) Coverage {
	c := Coverage{Changed: len(changed)}
	if len(changed) == 0 {
		return c
	}
	c.Named = countNamed(namedFiles(plan), changed)
	rate := float64(c.Named) / float64(c.Changed)
	c.Rate = &rate
	return c
}

// Completeness is the files the reference solution changes, how many of
// them a plan step names, and the share; nil when there is no plan or the
// reference changes nothing. The reference diff is computed by the harness
// (ReferenceChanges), never by a model.
type Completeness struct {
	RefFiles int      `json:"reference_files"`
	RefNamed int      `json:"reference_named"`
	Rate     *float64 `json:"plan_completeness"`
}

// PlanCompleteness compares the files the reference changes with the files
// the plan's steps name, matched as FileCoverage matches them.
func PlanCompleteness(plan []Step, refFiles []string) Completeness {
	c := Completeness{RefFiles: len(refFiles)}
	if len(plan) == 0 || len(refFiles) == 0 {
		return c
	}
	c.RefNamed = countNamed(namedFiles(plan), refFiles)
	rate := float64(c.RefNamed) / float64(c.RefFiles)
	c.Rate = &rate
	return c
}

// namedFiles is every file the plan's steps name, cleaned.
func namedFiles(plan []Step) []string {
	var named []string
	for _, s := range plan {
		for _, f := range s.Files {
			named = append(named, filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(f), "./"))))
		}
	}
	return named
}

// countNamed counts the paths that a named file names.
func countNamed(named, paths []string) int {
	n := 0
	for _, p := range paths {
		if namedBy(named, p) {
			n++
		}
	}
	return n
}

// namedBy reports whether a named file is path itself or a directory path
// is under.
func namedBy(named []string, path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	for _, n := range named {
		if namesPath(n, path) {
			return true
		}
	}
	return false
}

// namesPath: the named file n is path, or a directory path is under.
func namesPath(n, path string) bool {
	return n == path || n != "." && strings.HasPrefix(path, n+"/")
}

// Labels is what the claims say about label honesty.
type Labels struct {
	Claims                 int  `json:"claims"`
	Labelled               int  `json:"labelled"`
	Guarantees             int  `json:"guarantees"`
	GuaranteesWithPremises int  `json:"guarantees_with_premises"`
	AuditPass              bool `json:"audit_pass"`
	// AuditDetail says why the audit failed: the first claim the store
	// refused, or the audit's verdict.
	AuditDetail string `json:"audit_detail,omitempty"`
}

// CheckLabels counts the claims by what they carry, and audits them: each
// is written as a finding into a fresh database at dbPath through the
// store, which refuses a guarantee with no premise or one resting on an
// unknown, and the MSS audit runs over what was written. A claim resting
// on an index the list lacks, or on itself or a later claim, is a claim
// with no traceable premise. A guarantee counts as having premises when
// every index it names is an earlier claim and at least one is named; the
// store then says whether those premises are admissible.
func CheckLabels(claims []Claim, dbPath string) Labels {
	l := Labels{Claims: len(claims)}
	for i, c := range claims {
		l.count(c, i)
	}
	if len(claims) == 0 {
		l.AuditDetail = "no claim to audit"
		return l
	}
	l.AuditPass, l.AuditDetail = auditClaims(claims, dbPath)
	return l
}

// count tallies claim i: whether its label is valid, and for a guarantee
// whether it has premises.
func (l *Labels) count(c Claim, i int) {
	if mss.Label(c.Label).Valid() {
		l.Labelled++
	}
	if c.Label != string(mss.Guarantee) {
		return
	}
	l.Guarantees++
	if hasPremises(c, i) {
		l.GuaranteesWithPremises++
	}
}

// hasPremises: claim i names at least one premise, and every one is an
// earlier claim.
func hasPremises(c Claim, i int) bool {
	return len(c.RestsOn) > 0 && premisesEarlier(c.RestsOn, i)
}

// premisesEarlier: every index in on is a claim before claim i.
func premisesEarlier(on []int, i int) bool {
	for _, j := range on {
		if j < 0 || j >= i {
			return false
		}
	}
	return true
}

// auditClaims writes the claims as findings in order, depends_on_ids the
// ids of the earlier claims each rests on, and runs the audit.
func auditClaims(claims []Claim, dbPath string) (bool, string) {
	store, err := openAuditStore(dbPath)
	if err != nil {
		return false, err.Error()
	}
	defer store.Close()
	if err := writeClaims(store, claims); err != nil {
		return false, err.Error()
	}
	audit, err := store.MSSAudit()
	if err != nil {
		return false, "audit: " + err.Error()
	}
	return audit.Integrity == "PASS", "audit " + audit.Integrity
}

// openAuditStore opens a fresh audit database at dbPath, replacing any
// there, and initialises it.
func openAuditStore(dbPath string) (*db.Store, error) {
	_ = os.Remove(dbPath)
	store, err := db.NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open the audit database: %w", err)
	}
	if err := store.Init(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("init the audit database: %w", err)
	}
	return store, nil
}

// writeClaims writes the claims as findings in order, each depending on
// the findings of the earlier claims it rests on.
func writeClaims(store *db.Store, claims []Claim) error {
	ids := make([]int64, len(claims))
	for i, c := range claims {
		deps, err := claimDeps(c.RestsOn, i, ids)
		if err != nil {
			return err
		}
		id, err := store.Findings().AddFinding(claimFinding(c, deps))
		if err != nil {
			return fmt.Errorf("claim %d (%s) refused: %w", i, c.Label, err)
		}
		ids[i] = id
	}
	return nil
}

// claimDeps is the finding ids of the claims claim i rests on, refusing an
// index that is not an earlier claim.
func claimDeps(restsOn []int, i int, ids []int64) ([]int64, error) {
	var deps []int64
	for _, j := range restsOn {
		if j < 0 || j >= i {
			return nil, fmt.Errorf("claim %d rests on %d, which is not an earlier claim", i, j)
		}
		deps = append(deps, ids[j])
	}
	return deps, nil
}

// claimFinding is claim c as a wave-1 finding of the design agent that
// depends on the findings deps.
func claimFinding(c Claim, deps []int64) *db.Finding {
	// The claim's source is the design document it came from; naming it
	// keeps the store's missing-source warning off the harness's console.
	source := "design document"
	f := &db.Finding{Wave: 1, Agent: "design", MSSLabel: c.Label, Finding: c.Claim, SourceURLs: &source}
	if len(deps) > 0 {
		raw, _ := json.Marshal(deps)
		s := string(raw)
		f.DependsOnIDs = &s
	}
	return f
}
