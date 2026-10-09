package cli

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var mssLabelMap = map[string]string{
	"definition": ".definition",
	"guarantee":  ".guarantee",
	"assumption": ".assumption",
	"unknown":    ".unknown",
}

type lean4Finding struct {
	ID    int64
	Label string
	Deps  []int64
}

func newLean4ExtractCmd() *cobra.Command {
	var wave int

	cmd := &cobra.Command{
		Use:   "lean4-extract",
		Short: "Extract DB state as Lean 4 terms for formal verification",
		RunE: func(cmd *cobra.Command, args []string) error {
			var wavePtr *int
			if wave >= 0 {
				wavePtr = &wave
			}
			// The global --db names the database, as for every command, so
			// `chb --db X lean4-extract` reads X and labels the output X.
			return extractLean4(dbPath, wavePtr)
		},
	}

	cmd.Flags().IntVar(&wave, "wave", -1, "wave number (omit for all)")
	return cmd
}

// extractLean4 prints the findings of the database at resolved, of one wave
// or of all, as Lean 4 terms.
func extractLean4(resolved string, wave *int) error {
	if err := requireMSSAuditPass(); err != nil {
		return err
	}
	findings, err := getLean4Findings(store.ReadDB, wave)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		return noLean4Findings(resolved, wave)
	}
	emitLean4Output(findings, resolved, wave)
	return nil
}

// requireMSSAuditPass refuses a database the MSS audit rejects. A Lean file
// that typechecks certifies only the obligations it states, and the
// transitive audit (laundering through a chain) is checked by the Go BFS,
// not by Lean. Emitting a "certificate" for a database that audit rejects
// would let a green typecheck stand in for a failed audit.
func requireMSSAuditPass() error {
	audit, err := store.MSSAudit()
	if err != nil {
		return fmt.Errorf("MSS audit did not run: %w", err)
	}
	if audit.Integrity != "PASS" {
		return fmt.Errorf("refusing to extract: MSS audit is %s (%d laundering, %d untraceable, %d cycles, %d partition violations) — fix the database first",
			audit.Integrity, len(audit.LaunderingViolations), len(audit.UntraceableGuarantees), len(audit.DependencyCycles), len(audit.PartitionViolations))
	}
	return nil
}

// noLean4Findings is the error for a database, or a wave of it, with no
// findings to extract.
func noLean4Findings(resolved string, wave *int) error {
	msg := fmt.Sprintf("no findings in %s", resolved)
	if wave != nil {
		msg += fmt.Sprintf(" for wave %d", *wave)
	}
	return errors.New(msg)
}

func getLean4Findings(conn *sql.DB, wave *int) ([]lean4Finding, error) {
	rows, err := queryLean4Findings(conn, wave)
	if err != nil {
		return nil, fmt.Errorf("query findings: %w", err)
	}
	defer rows.Close()

	var findings []lean4Finding
	for rows.Next() {
		f, err := scanLean4Finding(rows)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

// queryLean4Findings queries the findings of one wave, or of all.
func queryLean4Findings(conn *sql.DB, wave *int) (*sql.Rows, error) {
	if wave != nil {
		return conn.Query(
			"SELECT id, mss_label, depends_on_ids FROM findings WHERE wave = ? ORDER BY id",
			*wave,
		)
	}
	return conn.Query(
		"SELECT id, mss_label, depends_on_ids FROM findings ORDER BY id",
	)
}

// scanLean4Finding scans the current row into a finding.
func scanLean4Finding(rows *sql.Rows) (lean4Finding, error) {
	var f lean4Finding
	var depsRaw sql.NullString
	if err := rows.Scan(&f.ID, &f.Label, &depsRaw); err != nil {
		return f, fmt.Errorf("scan finding: %w", err)
	}
	f.Deps = parseLean4Deps(depsRaw)
	return f, nil
}

// parseLean4Deps reads a finding's depends_on_ids: nil when it names no
// dependency or does not parse.
func parseLean4Deps(raw sql.NullString) []int64 {
	if !hasLean4Deps(raw) {
		return nil
	}
	var deps []int64
	if json.Unmarshal([]byte(raw.String), &deps) != nil {
		return nil
	}
	return deps
}

// hasLean4Deps reports whether a depends_on_ids value can name a
// dependency: it is not NULL, empty, null or [].
func hasLean4Deps(raw sql.NullString) bool {
	switch raw.String {
	case "", "null", "[]":
		return false
	}
	return raw.Valid
}

func emitLean4Output(findings []lean4Finding, dbPath string, wave *int) {
	scope, name := lean4Scope(wave)
	emitLean4Header(findings, dbPath, scope)
	for _, f := range findings {
		emitLean4Finding(f)
	}
	emitLean4Obligations(findings, scope, name)
}

// lean4Scope names the extraction's scope in prose and as the prefix of its
// Lean declarations.
func lean4Scope(wave *int) (scope, name string) {
	if wave != nil {
		return fmt.Sprintf("wave %d", *wave), fmt.Sprintf("wave%d", *wave)
	}
	return "all waves", "all"
}

// emitLean4Header prints the file's header comment, its source and its
// imports, and opens the namespace.
func emitLean4Header(findings []lean4Finding, dbPath, scope string) {
	contentBytes, _ := json.Marshal(findings)
	hash := sha256.Sum256(contentBytes)
	contentHash := fmt.Sprintf("%x", hash)[:16]
	timestamp := time.Now().UTC().Format(time.RFC3339)

	fmt.Println("/-")
	fmt.Printf("  Auto-generated MSS state extraction\n")
	fmt.Printf("  Scope: %s\n", scope)
	fmt.Printf("  Findings: %d\n", len(findings))
	fmt.Printf("  Extracted: %s\n", timestamp)
	fmt.Printf("  Content hash: %s\n", contentHash)
	fmt.Println()
	fmt.Println("  TRUST: This file is a direct translation of SQLite state.")
	fmt.Println("  If it typechecks, the four decidable invariants below hold for")
	fmt.Println("  this state. Transitive no-laundering is checked by the Go MSS")
	fmt.Println("  audit (which passed at extraction time); Lean states no decision")
	fmt.Println("  procedure for it.")
	fmt.Println("-/")
	// The path is the one line a user chooses. In a line comment, `/-` and
	// `-/` in it are text; inside the block comment above they would open or
	// close a nested comment and leave the file unparseable.
	fmt.Printf("-- Source: %s\n", dbPath)
	fmt.Println("import MSS.Basic")
	fmt.Println("import MSS.Invariants")
	fmt.Println("import MSS.Decidability")
	fmt.Println()
	fmt.Printf("namespace MSS.Instance\n\n")
}

// emitLean4Finding prints one finding as a Lean definition.
func emitLean4Finding(f lean4Finding) {
	leanLabel := mssLabelMap[f.Label]
	if leanLabel == "" {
		leanLabel = ".unknown"
	}
	depsStrs := make([]string, len(f.Deps))
	for i, d := range f.Deps {
		depsStrs[i] = fmt.Sprintf("%d", d)
	}
	depsStr := strings.Join(depsStrs, ", ")
	fmt.Printf("private def f%d : Finding :=\n", f.ID)
	fmt.Printf("  { id := %d, label := MSSLabel%s, deps := [%s] }\n\n", f.ID, leanLabel, depsStr)
}

// emitLean4Obligations prints the findings list, the database built from
// it and the verification obligations, and closes the namespace.
func emitLean4Obligations(findings []lean4Finding, scope, name string) {
	fNames := make([]string, len(findings))
	for i, f := range findings {
		fNames[i] = fmt.Sprintf("f%d", f.ID)
	}
	findingsList := strings.Join(fNames, ", ")
	fmt.Printf("/-- All findings for %s. -/\n", scope)
	fmt.Printf("def %s_findings : List Finding :=\n", name)
	fmt.Printf("  [%s]\n\n", findingsList)

	fmt.Printf("/-- The findings database for %s. -/\n", scope)
	fmt.Printf("def %s_db : FindingsDB :=\n", name)
	fmt.Printf("  { findings := %s_findings\n", name)
	fmt.Printf("    ids_unique := by decide }\n\n")

	fmt.Println("/-! ## Verification Obligations")
	fmt.Println()
	fmt.Println("If the following declarations typecheck without errors, the")
	fmt.Println("decidable MSS invariants hold for this database state. Each")
	fmt.Println("`by decide` elaborates through an instance in MSS.Decidability.")
	fmt.Println("-/")
	fmt.Println()

	fmt.Printf("/-- Invariant 1: No guarantee directly depends on an unknown. -/\n")
	fmt.Printf("theorem %s_no_laundering : NoDirectLaundering %s_db := by decide\n\n", name, name)
	fmt.Printf("/-- Invariant 2: Every guarantee has at least one dependency. -/\n")
	fmt.Printf("theorem %s_no_untraceable : NoUntraceableGuarantees %s_db := by decide\n\n", name, name)
	fmt.Printf("/-- Invariant 3: The dependency graph is acyclic. -/\n")
	fmt.Printf("theorem %s_acyclic : Acyclic %s_db := by decide\n\n", name, name)
	fmt.Printf("/-- Invariant 5: All dependency references are valid. -/\n")
	fmt.Printf("theorem %s_deps_valid : DepsValid %s_db := by decide\n\n", name, name)

	fmt.Println("end MSS.Instance")
}
