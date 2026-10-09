package foragers

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Persona profiles: how much of each persona a generated swarm's prompts
// carry (swarm.md § Persona profiles).
const (
	// ProfileFull renders each persona body whole with its Jungian and IFS
	// lines, and Queen and the coverage evaluator read every forager's full
	// verdict. It is the default.
	ProfileFull = "full"
	// ProfileLean renders the body sections LeanSections names, drops the
	// Jungian and IFS lines, and hands Queen and the evaluator the swarm
	// ledger in place of every full verdict.
	ProfileLean = "lean"
)

// LeanSections are the template-v1 body sections the lean profile renders:
// the output contract, the decision rubric, the anti-patterns, the
// tie-breakers and the emission guard. It leaves out the worked example, the
// bonds in prose (the prompt states the bonds) and the lens lore.
var LeanSections = []int{1, 2, 4, 5, 7}

// ParsePersonaSections reads a --persona-sections value: section numbers,
// comma-separated, each a whole number of 1 or more. They come back sorted,
// without repeats.
func ParsePersonaSections(s string) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, ok := sectionNumber(part)
		if !ok {
			return nil, fmt.Errorf("persona sections %q: want section numbers such as 1,2,4,5,7", s)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

// sectionNumber reads one section number: a whole number of 1 or more.
func sectionNumber(part string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(part))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// personaSection is one "## § N" section of a persona body: its number, its
// marker line, and its text from the marker line to the next marker.
type personaSection struct {
	num     int
	heading string
	text    string
}

// sectionMarker matches a section marker line, "## § 3 — Worked example".
var sectionMarker = regexp.MustCompile(`(?m)^## § ?(\d+)\b.*$`)

// sectionRef matches a reference to a section in persona prose, "§ 3" or
// "§3". Group 2 holds a dotted tail, as in "RFC 793 §2.10", which names
// another document's section.
var sectionRef = regexp.MustCompile(`§ ?(\d+)(\.\d+)?`)

// splitSections splits a persona body at its section markers into the text
// before the first marker and the sections in body order. A body with no
// marker is all preamble.
func splitSections(body string) (preamble string, secs []personaSection) {
	locs := sectionMarker.FindAllStringSubmatchIndex(body, -1)
	if len(locs) == 0 {
		return body, nil
	}
	preamble = body[:locs[0][0]]
	for i, loc := range locs {
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		n, _ := strconv.Atoi(body[loc[2]:loc[3]])
		secs = append(secs, personaSection{num: n, heading: body[loc[0]:loc[1]], text: body[loc[0]:end]})
	}
	return preamble, secs
}

// leftOutNote follows the marker line of a section a prompt leaves out but
// its kept text refers to.
const leftOutNote = " (left out of this prompt)"

// renderPersona is the persona text a prompt carries. With sections nil it is
// the whole body. Otherwise it is the text before the first section marker
// and the sections named, in body order. A section left out that the kept
// text refers to ("§ 3") is written as its marker line and leftOutNote, so no
// reference points at text the prompt does not hold. ok is false when
// sections are asked for and the body has no section markers: it then
// renders whole.
func renderPersona(body string, sections []int) (text string, ok bool) {
	if sections == nil {
		return body, true
	}
	preamble, secs := splitSections(body)
	if len(secs) == 0 {
		return body, false
	}
	keep := sectionSet(sections)
	referenced := referencedSections(keptText(preamble, secs, keep))
	return assemblePersona(preamble, secs, keep, referenced), true
}

// sectionSet is the set of section numbers asked for.
func sectionSet(sections []int) map[int]bool {
	keep := make(map[int]bool, len(sections))
	for _, n := range sections {
		keep[n] = true
	}
	return keep
}

// keptText is the preamble and the sections kept, in body order.
func keptText(preamble string, secs []personaSection, keep map[int]bool) string {
	kept := preamble
	for _, s := range secs {
		if keep[s.num] {
			kept += s.text
		}
	}
	return kept
}

// referencedSections are the sections text refers to ("§ 3"), less
// references to another document's section ("§2.10").
func referencedSections(text string) map[int]bool {
	referenced := map[int]bool{}
	for _, m := range sectionRef.FindAllStringSubmatch(text, -1) {
		if m[2] != "" {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		referenced[n] = true
	}
	return referenced
}

// assemblePersona writes the preamble, each kept section, and the marker
// line and leftOutNote of each section left out that the kept text refers
// to.
func assemblePersona(preamble string, secs []personaSection, keep, referenced map[int]bool) string {
	var b strings.Builder
	b.WriteString(preamble)
	for _, s := range secs {
		switch {
		case keep[s.num]:
			b.WriteString(s.text)
		case referenced[s.num]:
			b.WriteString(s.heading + leftOutNote + "\n\n")
		}
	}
	return b.String()
}

// counterBiasHeading heads the counter-bias clause in a prompt. Persona prose
// refers to the clause as §behavioral_floor.
const counterBiasHeading = "## § behavioral_floor — counter-bias clause"

// counterBiasBlock is the persona's counter-bias clause as its prompt carries
// it, "" when it declares none. A clause conditioned on a forager's absence
// (behavioral_floor.counter_bias_when_absent) is stated as applying or not,
// by whether that forager is in the swarm, so the model is never left to
// work out the condition.
func counterBiasBlock(w Forager, inSwarm map[string]bool) string {
	if w.CounterBias == "" {
		return ""
	}
	if w.CounterBiasWhenAbsent == "" {
		return counterBiasHeading + "\n\n" + w.CounterBias + "\n"
	}
	who := capitalize(w.CounterBiasWhenAbsent)
	if inSwarm[w.CounterBiasWhenAbsent] {
		return counterBiasHeading + "\n\n" + who + " is in this swarm, so the counter-bias clause does not apply.\n"
	}
	return counterBiasHeading + "\n\n" + who + " is not in this swarm, so this clause applies: " + w.CounterBias + "\n"
}

// parseCounterBias reads behavioral_floor.counter_bias_clause and
// behavioral_floor.counter_bias_when_absent from a persona's frontmatter.
// Frontmatter whose behavioral_floor is not a mapping gives neither.
func parseCounterBias(frontmatter []byte) (clause, whenAbsent string) {
	var fm struct {
		Floor map[string]any `yaml:"behavioral_floor"`
	}
	if yaml.Unmarshal(frontmatter, &fm) != nil {
		return "", ""
	}
	clause, _ = fm.Floor["counter_bias_clause"].(string)
	whenAbsent, _ = fm.Floor["counter_bias_when_absent"].(string)
	return strings.TrimSpace(clause), strings.ToLower(strings.TrimSpace(whenAbsent))
}

// lensAxes are the coverage values the roster audit reads (swarm.md § Axis
// ownership), less CDE-encode, which is the synthesizer's phase and which no
// lens declares.
var lensAxes = []struct{ family, value, label string }{
	{"wasp", "k", "WASP-k"}, {"wasp", "k_execution", "WASP-k_execution"}, {"wasp", "E", "WASP-E"},
	{"wasp", "I", "WASP-I"}, {"wasp", "T", "WASP-T"}, {"wasp", "F", "WASP-F"},
	{"cde", "detect", "CDE-detect"}, {"cde", "decompose", "CDE-decompose"}, {"cde", "execute", "CDE-execute"},
	{"mss", "def", "MSS-def"}, {"mss", "gua", "MSS-gua"}, {"mss", "asm", "MSS-asm"}, {"mss", "unk", "MSS-unk"},
}

// uncoveredAxes lists, in lensAxes order, the axes no lens declares.
func uncoveredAxes(lens []Forager) []string {
	owned := map[string]bool{}
	for _, w := range lens {
		owned["wasp:"+w.Coverage.Wasp] = true
		owned["cde:"+w.Coverage.Cde] = true
		owned["mss:"+w.Coverage.Mss] = true
	}
	var out []string
	for _, a := range lensAxes {
		if !owned[a.family+":"+a.value] {
			out = append(out, a.label)
		}
	}
	return out
}
