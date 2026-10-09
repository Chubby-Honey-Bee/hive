// Package mss keeps the MSS truth labels — definition, guarantee,
// assumption, unknown — honest: the write-time invariants (traceability, no
// laundering, acyclicity), the audit-time detectors (partition and
// independence) and the alarm-pheromone cascade.
package mss

import (
	"errors"
	"fmt"
)

// Label represents an MSS truth label.
type Label string

// The four MSS truth labels.
const (
	Definition Label = "definition"
	Guarantee  Label = "guarantee"
	Assumption Label = "assumption"
	Unknown    Label = "unknown"
)

// Valid returns true if l is one of the four MSS labels.
func (l Label) Valid() bool {
	switch l {
	case Definition, Guarantee, Assumption, Unknown:
		return true
	}
	return false
}

// Sentinel errors for MSS violations.
var (
	ErrLaundering    = errors.New("MSS laundering violation: guarantee depends on unknown")
	ErrMissingDeps   = errors.New("MSS traceability violation: guarantee has no depends_on_ids")
	ErrCycleDetected = errors.New("MSS acyclicity violation: dependency cycle detected")
)

// MSSError wraps a sentinel with finding-specific context.
type MSSError struct {
	FindingID    int64
	DependsOnIDs []int64
	Label        Label
	Reason       string
	Err          error // sentinel
}

// Error names the finding, its label and the reason.
func (e *MSSError) Error() string {
	return fmt.Sprintf("MSS error (finding %d, label %s): %s", e.FindingID, e.Label, e.Reason)
}

// Unwrap returns the sentinel, so errors.Is matches the violation.
func (e *MSSError) Unwrap() error { return e.Err }
