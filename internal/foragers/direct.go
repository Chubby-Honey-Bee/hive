package foragers

import (
	"regexp"
	"strings"
)

// The direct voice (swarm.md § The direct voice): the model's own answer to
// the question and the context, with no persona, as one more vote in the
// Queen's tally. Its node is forager-<DirectVoiceName>, so the run tokens
// count it as they count a lens. The name is plain rather than bee-accurate:
// it is the model's direct answer, and no roster file uses the word.
const (
	DirectVoiceName  = "direct"
	directVoiceTitle = "The direct answer"
)

// DirectPrompt is the direct voice's prompt and the bench solo control's: the
// question, the context, and the lens contract's verdict, key points and
// recommendation. It is unindented; a YAML writer indents it under `prompt: |`.
const DirectPrompt = `Question:
  {question}

Context:
  {context}

Return JSON exactly with these keys:
  {"verdict":"support|oppose|conditional|abstain",
   "key_points":[...],
   "recommendation":"<one sentence>"}`

// DirectSchemaJSON is the direct voice's output_schema as one line of JSON:
// the reasons before the verdict, every key required.
const DirectSchemaJSON = `{"type": "object", "properties": {"key_points": {"type": "array", "items": {"type": "string"}}, "verdict": {"enum": ["support", "oppose", "conditional", "abstain"]}, "recommendation": {"type": "string"}}, "required": ["key_points", "verdict", "recommendation"]}`

// ContextKey is the workflow input a lens reads its part of the context
// from under --context-split.
func ContextKey(name string) string { return "context_" + name }

// ContextParts deals context among the swarm's lens foragers as the
// generated workflow reads it: in name order, round-robin by block
// (SplitContext). It returns each lens's part under its ContextKey.
func ContextParts(swarm []Forager, context string) map[string]string {
	lens, _ := SplitByArchetype(swarm)
	parts := SplitContext(context, len(lens))
	out := make(map[string]string, len(lens))
	for i, w := range lens {
		out[ContextKey(w.Name)] = parts[i]
	}
	return out
}

var (
	paragraphBreak = regexp.MustCompile(`\n[ \t]*\n+`)
	listItem       = regexp.MustCompile(`^([ \t]*)- `)
)

// SplitContext cuts context into n parts, dealing its blocks round-robin:
// part i holds blocks i, i+n, i+2n, … in their original order. The blocks
// are the context's paragraphs, separated by blank lines; when it has one
// paragraph, they are the items of its shallowest list, the lines starting
// `- ` at the least indentation, which is the roster entry boundary the
// bench's pack uses, the text before the first item riding with the first.
// A context with neither is one block. Every non-blank line lands in one
// part, and a part past the last block is empty. n below 1 gives nil.
func SplitContext(context string, n int) []string {
	if n < 1 {
		return nil
	}
	blocks, sep := contextBlocks(context)
	groups := make([][]string, n)
	for i, b := range blocks {
		groups[i%n] = append(groups[i%n], b)
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = strings.Join(groups[i], sep)
	}
	return parts
}

// contextBlocks is the block list SplitContext deals and the separator that
// joins a part's blocks back together.
func contextBlocks(context string) (blocks []string, sep string) {
	text := strings.Trim(context, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, ""
	}
	paras := paragraphs(text)
	if len(paras) > 1 {
		return paras, "\n\n"
	}
	lines := strings.Split(text, "\n")
	depth := shallowestListIndent(lines)
	if depth < 0 {
		return paras, "\n"
	}
	return listBlocks(lines, depth), "\n"
}

// paragraphs are text's non-blank paragraphs, separated by blank lines.
func paragraphs(text string) []string {
	var paras []string
	for _, p := range paragraphBreak.Split(text, -1) {
		if strings.TrimSpace(p) != "" {
			paras = append(paras, p)
		}
	}
	return paras
}

// listIndent is the indentation of a line that is a list item.
func listIndent(l string) (int, bool) {
	m := listItem.FindStringSubmatch(l)
	if m == nil {
		return 0, false
	}
	return len(m[1]), true
}

// shallowestListIndent is the least indentation of a list item among
// lines, or -1 when none is a list item.
func shallowestListIndent(lines []string) int {
	depth := -1
	for _, l := range lines {
		if d, ok := listIndent(l); ok && shallower(d, depth) {
			depth = d
		}
	}
	return depth
}

// shallower reports whether indentation d is less than depth, or depth is
// still unset (-1).
func shallower(d, depth int) bool {
	return depth < 0 || d < depth
}

// listBlocks cuts lines at the list items indented by depth, the text
// before the first item riding with the first.
func listBlocks(lines []string, depth int) []string {
	s := &listSplit{depth: depth}
	for _, l := range lines {
		s.line(l)
	}
	return append(s.blocks, strings.Join(s.cur, "\n"))
}

// listSplit is listBlocks' progress: the blocks cut so far, the lines of
// the block being read, and whether the first item has been seen.
type listSplit struct {
	depth  int
	blocks []string
	cur    []string
	seen   bool
}

// line adds a line to the block being read, closing that block first when
// the line starts the next one.
func (s *listSplit) line(l string) {
	if s.startsBlock(l) {
		s.blocks = append(s.blocks, strings.Join(s.cur, "\n"))
		s.cur = nil
	}
	s.cur = append(s.cur, l)
}

// startsBlock reports whether l is an item of the list after its first, and
// notes the first.
func (s *listSplit) startsBlock(l string) bool {
	if d, ok := listIndent(l); !ok || d != s.depth {
		return false
	}
	starts := s.seen
	s.seen = true
	return starts
}
