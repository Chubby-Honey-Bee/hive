package harness

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The contracts a forager's verdict declares in its persona's frontmatter,
// and the checks the swarm case holds a verdict to.

// loadPersonaContract returns the persona's frontmatter as a generic map.
func loadPersonaContract(name string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join("foragers", name+".md"))
	if err != nil {
		return nil, err
	}
	front, err := personaFrontmatter(name, string(raw))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(front), &m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

// personaFrontmatter is the YAML between a persona file's opening --- line
// and the next.
func personaFrontmatter(name, s string) (string, error) {
	if !strings.HasPrefix(s, "---\n") {
		return "", fmt.Errorf("%s: no frontmatter", name)
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", fmt.Errorf("%s: unterminated frontmatter", name)
	}
	return rest[:end], nil
}

// checkVerdictContract checks a forager's verdict against its persona's
// contract.
//
// Two classes of check live here. A contract check fails the case: it is
// deterministic given correct code, so a failure means the code is wrong.
// An adherence check reports: whether a model at a given tier honours a
// style rule varies between runs of a correct system, so one occurrence is
// evidence about the tier, not about the code. Gating on it makes the suite
// flaky, and a flaky suite is one nobody reads. `--strict` turns adherence
// into a gate for the runs where you want to hold the line.
func checkVerdictContract(name string, v map[string]any, contract map[string]any) []harnessCheck {
	c := verdictContractChecks{name: name}
	if v == nil {
		c.add("verdict JSON parsed", false, "no parsed verdict in the artifact")
		return c.out
	}
	schema, _ := contract["output_schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	c.checkRequired(v, schema)
	c.checkConst(v, props)
	c.checkEnum(v, props)
	c.checkLengthCaps(v, contract)
	c.checkForbidden(v, contract)
	return c.out
}

// verdictContractChecks collects one forager's verdict checks, each named
// after the forager.
type verdictContractChecks struct {
	name string
	out  []harnessCheck
}

// add records a contract check.
func (c *verdictContractChecks) add(n string, ok bool, d string) {
	c.out = append(c.out, harnessCheck{Name: c.name + ": " + n, OK: ok, Detail: d})
}

// addWarn records an adherence check.
func (c *verdictContractChecks) addWarn(n string, ok bool, d string) {
	c.out = append(c.out, harnessCheck{Name: c.name + ": " + n, OK: ok, Warn: !ok, Detail: d})
}

// checkRequired: every key the schema requires is present.
func (c *verdictContractChecks) checkRequired(v, schema map[string]any) {
	var missing []string
	for _, k := range toStrings(schema["required"]) {
		if _, ok := v[k]; !ok {
			missing = append(missing, k)
		}
	}
	c.add("required keys present", len(missing) == 0, strings.Join(missing, ","))
}

// checkConst: the forager field is the schema's const, when it has one.
func (c *verdictContractChecks) checkConst(v, props map[string]any) {
	fp, ok := props["forager"].(map[string]any)
	if !ok {
		return
	}
	if want, ok := fp["const"].(string); ok {
		c.add("forager const", fmt.Sprint(v["forager"]) == want, fmt.Sprintf("%v", v["forager"]))
	}
}

// checkEnum: the verdict is in the schema's enum, when it has one.
func (c *verdictContractChecks) checkEnum(v, props map[string]any) {
	vp, ok := props["verdict"].(map[string]any)
	if !ok {
		return
	}
	if enum := toStrings(vp["enum"]); len(enum) > 0 {
		c.add("verdict in enum", slices.Contains(enum, fmt.Sprint(v["verdict"])), fmt.Sprintf("%v", v["verdict"]))
	}
}

// checkLengthCaps: every capped field keeps within its caps (adherence).
func (c *verdictContractChecks) checkLengthCaps(v, contract map[string]any) {
	caps, ok := contract["length_caps"].(map[string]any)
	if !ok {
		return
	}
	var over []string
	for field, rawCap := range caps {
		capm, _ := rawCap.(map[string]any)
		over = append(over, lengthCapOverruns(field, v[field], capm)...)
	}
	c.addWarn("length caps respected (adherence)", len(over) == 0, strings.Join(over, "; "))
}

// checkForbidden: the verdict uses none of the forbidden phrases, in any
// case (adherence).
func (c *verdictContractChecks) checkForbidden(v, contract map[string]any) {
	phrases := toStrings(contract["forbidden_phrases"])
	if len(phrases) == 0 {
		return
	}
	text := strings.ToLower(flattenStrings(v))
	var hit []string
	for _, p := range phrases {
		if strings.Contains(text, strings.ToLower(p)) {
			hit = append(hit, p)
		}
	}
	c.addWarn("no forbidden phrases (adherence)", len(hit) == 0, strings.Join(hit, ", "))
}

// lengthCapOverruns says how a field's value exceeds its caps: a list's
// item count and each item's characters, or a string's characters.
func lengthCapOverruns(field string, val any, capm map[string]any) []string {
	switch x := val.(type) {
	case []any:
		return listLengthCapOverruns(field, x, capm)
	case string:
		return stringLengthCapOverruns(field, x, capm)
	}
	return nil
}

// listLengthCapOverruns says how a list exceeds max_items, and which of its
// items exceed max_chars_each.
func listLengthCapOverruns(field string, val []any, capm map[string]any) []string {
	var over []string
	if mi, ok := toInt(capm["max_items"]); ok && len(val) > mi {
		over = append(over, fmt.Sprintf("%s items %d>%d", field, len(val), mi))
	}
	if mc, ok := toInt(capm["max_chars_each"]); ok {
		over = append(over, itemLengthCapOverruns(field, val, mc)...)
	}
	return over
}

// itemLengthCapOverruns names the string items of a list longer than mc
// characters.
func itemLengthCapOverruns(field string, val []any, mc int) []string {
	var over []string
	for i, it := range val {
		if s, ok := it.(string); ok && len([]rune(s)) > mc {
			over = append(over, fmt.Sprintf("%s[%d] %d>%d chars", field, i, len([]rune(s)), mc))
		}
	}
	return over
}

// stringLengthCapOverruns says how a string exceeds max_chars.
func stringLengthCapOverruns(field, val string, capm map[string]any) []string {
	if mc, ok := toInt(capm["max_chars"]); ok && len([]rune(val)) > mc {
		return []string{fmt.Sprintf("%s %d>%d chars", field, len([]rune(val)), mc)}
	}
	return nil
}

// resonatesPartners lists the foragers a persona's contract declares a
// resonates bond to.
func resonatesPartners(contract map[string]any) []string {
	var out []string
	bonds, _ := contract["bonds"].([]any)
	for _, b := range bonds {
		bm, _ := b.(map[string]any)
		if fmt.Sprint(bm["kind"]) == "resonates" {
			if to, ok := bm["to"].(string); ok {
				out = append(out, to)
			}
		}
	}
	return out
}

// pairKey names a pair of foragers in either order.
func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// flattenStrings is every string in a decoded JSON value, each followed by
// a space.
func flattenStrings(v any) string {
	var sb strings.Builder
	writeFlatStrings(&sb, v)
	return sb.String()
}

// writeFlatStrings writes every string under x, each followed by a space.
func writeFlatStrings(sb *strings.Builder, x any) {
	switch t := x.(type) {
	case string:
		sb.WriteString(t)
		sb.WriteString(" ")
	case []any:
		writeFlatStringList(sb, t)
	case map[string]any:
		writeFlatStringList(sb, slices.Collect(maps.Values(t)))
	}
}

// writeFlatStringList writes every string under each element.
func writeFlatStringList(sb *strings.Builder, xs []any) {
	for _, e := range xs {
		writeFlatStrings(sb, e)
	}
}

// toStrings is a decoded list, each element written with fmt.Sprint.
func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

// toInt is a decoded number as an int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
