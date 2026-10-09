package calibration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Input is the JSON shape every ingestion surface reads: `chb
// outcome-record` and `outcome-import`, and the MCP tool
// `chb_outcome_record`. A key outside it is refused. The source is the
// surface's, never the body's.
type Input struct {
	SubjectKind      string   `json:"subject_kind"`
	FindingID        int64    `json:"finding_id"`
	Lens             string   `json:"lens"`
	RunID            int64    `json:"run_id"`
	BeliefTickID     int64    `json:"belief_tick_id"`
	D1               *int     `json:"d1"`
	D2               *int     `json:"d2"`
	D3               *int     `json:"d3"`
	D4               *int     `json:"d4"`
	Resolution       string   `json:"resolution"`
	StatedConfidence *int     `json:"stated_confidence"`
	PredictedValue   *float64 `json:"predicted_value"`
	ActualValue      *float64 `json:"actual_value"`
	Rationale        string   `json:"rationale"`
	EvidenceURLs     string   `json:"evidence_urls"`
	ResolvedTickID   int64    `json:"resolved_tick_id"`
}

// Outcome is the input as Record takes it, with the surface's source.
func (in Input) Outcome(source string) Outcome {
	return Outcome{
		SubjectKind: in.SubjectKind, FindingID: in.FindingID, Lens: in.Lens, RunID: in.RunID,
		BeliefTickID: in.BeliefTickID, D1: in.D1, D2: in.D2, D3: in.D3, D4: in.D4,
		Resolution: in.Resolution, StatedConfidence: in.StatedConfidence,
		PredictedValue: in.PredictedValue, ActualValue: in.ActualValue,
		Source: source, Rationale: in.Rationale, EvidenceURLs: in.EvidenceURLs,
		ResolvedTickID: in.ResolvedTickID,
	}
}

// DecodeInputs reads one outcome object or an array of them. A key outside
// Input is an error that names it.
func DecodeInputs(data []byte) ([]Input, error) {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("[")) {
		var list []Input
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&list); err != nil {
			return nil, fmt.Errorf("parse outcomes: %w (accepted keys: %s)", err, inputKeys())
		}
		return list, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var one Input
	if err := dec.Decode(&one); err != nil {
		return nil, fmt.Errorf("parse outcome: %w (accepted keys: %s)", err, inputKeys())
	}
	return []Input{one}, nil
}

// inputKeys lists Input's JSON keys, in declaration order, for the refusal
// of a key outside the shape.
func inputKeys() string {
	t := reflect.TypeOf(Input{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return strings.Join(keys, ", ")
}
