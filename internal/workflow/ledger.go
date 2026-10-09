package workflow

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// mssClaimFields are the typed claim fields a forager may return, each with
// the MSS label its claims carry.
var mssClaimFields = []fieldLabel{
	{"def_claims", "definition"},
	{"gua_claims", "guarantee"},
	{"asm_claims", "assumption"},
	{"unk_claims", "unknown"},
}

// ledgerRowFields are the fields every lens's ledger row carries after its
// verdict and recommendation.
var ledgerRowFields = []fieldLabel{
	{"key_points", "key points"},
	{"uncertainties", "uncertainties"},
}

// ledgerShownKeys are the fields of a verdict the ledger shows outside its
// detail: the forager's name, the verdict, the recommendation, the row
// fields, the typed claims, the evidence (written first in the detail) and a
// final_text fallback.
var ledgerShownKeys = fieldSet([]string{"forager", "final_text", "verdict", "recommendation", "evidence"}, ledgerRowFields, mssClaimFields)

// swarmLedger renders {swarm.ledger}, which the lean Queen and coverage
// evaluator read in place of every full verdict (comb.md § Template tokens).
// It holds, from this run's forager nodes:
//   - each lens's verdict and recommendation, key points and uncertainties,
//     or the line saying why it has no verdict;
//   - the claims the lenses labelled, grouped by MSS label;
//   - the resonates pairs with a forager that has no node in this run;
//   - the rest of each verdict that is not the tally's plurality, evidence
//     first: every verdict when there is no plurality. Queen may depart from
//     the plurality only by naming the evidence that outweighs it, so she
//     gets the evidence of every lens that did not return it.
func swarmLedger(pairs [][2]string, outcomes map[string]foragerOutcome) string {
	names := slices.Sorted(maps.Keys(outcomes))
	var b strings.Builder
	writeLensRows(&b, names, outcomes)
	writeLabelledClaims(&b, names, outcomes)
	b.WriteString("\nResonates pairs with a forager that has no node in this run, so they cannot fire: ")
	b.WriteString(orphanPairs(pairs, outcomes))
	b.WriteString("\n")
	writeDissent(&b, names, outcomes)
	return strings.TrimSpace(b.String())
}

func writeLensRows(b *strings.Builder, names []string, outcomes map[string]foragerOutcome) {
	b.WriteString("Lenses (verdict — recommendation; key points; uncertainties):\n")
	for _, name := range names {
		writeLensRow(b, name, outcomes[name])
	}
}

// writeLensRow writes one lens's row: its verdict and recommendation, key
// points and uncertainties, or the line saying why it has no verdict.
func writeLensRow(b *strings.Builder, name string, o foragerOutcome) {
	if o.outputs == nil {
		fmt.Fprintf(b, "- %s: (no verdict: %s is %s in this run)\n", name, o.node, o.status)
		return
	}
	fmt.Fprintf(b, "- %s: %s%s\n", name, givenVerdict(o), recommendationSuffix(o.outputs))
	for _, f := range ledgerRowFields {
		writeField(b, "  ", f.label, o.outputs[f.key])
	}
}

// givenVerdict is a lens's verdict, or a note that it gave none.
func givenVerdict(o foragerOutcome) string {
	if v := o.verdict(); v != "" {
		return v
	}
	return "(no verdict given)"
}

// recommendationSuffix is " — <recommendation>" when the lens gave one.
func recommendationSuffix(outputs map[string]any) string {
	if rec := outputs["recommendation"]; rec != nil {
		return " — " + verdictItemText(rec)
	}
	return ""
}

// writeLabelledClaims writes the claims the lenses labelled, grouped by MSS
// label, or none.
func writeLabelledClaims(b *strings.Builder, names []string, outcomes map[string]foragerOutcome) {
	b.WriteString("\nThe claims the lenses labelled, by MSS label. Echo each label as given; never raise one:\n")
	claims := 0
	for _, f := range mssClaimFields {
		claims += writeClaimGroup(b, f, labelledClaims(names, outcomes, f.key))
	}
	if claims == 0 {
		b.WriteString("  none\n")
	}
}

// labelledClaims is every lens's claims under one typed claim field, each
// led by its lens's name, lenses in name order.
func labelledClaims(names []string, outcomes map[string]foragerOutcome, key string) []string {
	var items []string
	for _, name := range names {
		for _, it := range ledgerItems(outcomes[name].outputs[key]) {
			items = append(items, name+": "+it)
		}
	}
	return items
}

// writeClaimGroup writes one MSS label's claims, nothing when it has none,
// and returns how many it wrote.
func writeClaimGroup(b *strings.Builder, f fieldLabel, items []string) int {
	if len(items) == 0 {
		return 0
	}
	fmt.Fprintf(b, "  %s (%s):\n", f.label, f.key)
	for _, it := range items {
		fmt.Fprintf(b, "    - %s\n", it)
	}
	return len(items)
}

// writeDissent writes, evidence first, the rest of each verdict that is not
// the tally's plurality, under a heading that says whose it is.
func writeDissent(b *strings.Builder, names []string, outcomes map[string]foragerOutcome) {
	plurality := tallyVerdicts(outcomes).plurality
	detail := dissenters(names, outcomes, plurality)
	b.WriteString(detailHeading(len(detail) > 0, plurality))
	for _, name := range detail {
		writeVerdictDetail(b, name, outcomes[name])
	}
}

// dissenters are the lenses that returned a verdict other than the
// plurality: every lens with a verdict when there is no plurality.
func dissenters(names []string, outcomes map[string]foragerOutcome, plurality string) []string {
	var detail []string
	for _, name := range names {
		if v := outcomes[name].verdict(); v != "" && v != plurality {
			detail = append(detail, name)
		}
	}
	return detail
}

func detailHeading(hasDetail bool, plurality string) string {
	if !hasDetail {
		return noDetailHeading(plurality)
	}
	if plurality == "" {
		return "\nThere is no plurality, so the rest of every verdict follows, evidence first:\n"
	}
	return fmt.Sprintf("\nThe rest of each verdict that is not the plurality (%s), evidence first:\n", plurality)
}

func noDetailHeading(plurality string) string {
	if plurality != "" {
		return fmt.Sprintf("\nEvery lens returned the plurality (%s), so no verdict is given in full.\n", plurality)
	}
	return "\nNo lens returned a verdict, so none is given in full.\n"
}

// writeVerdictDetail writes the rest of one lens's verdict: its evidence,
// then every field the ledger shows nowhere else.
func writeVerdictDetail(b *strings.Builder, name string, o foragerOutcome) {
	fmt.Fprintf(b, "- %s (%s):\n", name, o.verdict())
	n := b.Len()
	writeField(b, "  ", "evidence", o.outputs["evidence"])
	for _, k := range detailKeys(o.outputs) {
		writeField(b, "  ", k, o.outputs[k])
	}
	if b.Len() == n {
		b.WriteString("  (no field beyond those above)\n")
	}
}

// orphanPairs lists the resonates pairs with a forager that has no node in
// this run, each written a↔b in canonical order and the list sorted, or
// "none".
func orphanPairs(pairs [][2]string, outcomes map[string]foragerOutcome) string {
	orphans := map[string]bool{}
	for _, p := range pairs {
		if !bothRan(p, outcomes) {
			orphans[pairKey(p)] = true
		}
	}
	if len(orphans) == 0 {
		return "none"
	}
	return strings.Join(slices.Sorted(maps.Keys(orphans)), "; ")
}

// bothRan reports whether both foragers of a pair have a node in this run.
func bothRan(p [2]string, outcomes map[string]foragerOutcome) bool {
	_, a := outcomes[p[0]]
	_, b := outcomes[p[1]]
	return a && b
}

// pairKey writes a pair a↔b in canonical order.
func pairKey(p [2]string) string {
	x, y := p[0], p[1]
	if x > y {
		x, y = y, x
	}
	return x + "↔" + y
}

// detailKeys are the fields of a verdict the ledger gives only in full, in
// name order: all but ledgerShownKeys.
func detailKeys(outputs map[string]any) []string {
	return otherKeys(outputs, ledgerShownKeys)
}

// ledgerItems is a field's value as ledger items: one per list element, or
// the value itself; none for a missing value or an empty list.
func ledgerItems(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			out = append(out, verdictItemText(it))
		}
		return out
	default:
		return []string{verdictItemText(x)}
	}
}
