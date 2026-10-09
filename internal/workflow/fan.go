package workflow

import (
	"encoding/json"
	"regexp"
	"strings"
)

// setFanFields fills a parallel_fan's items from its fan_source, the first
// fan_limit of them when it has one, and the placeholder each item fills.
// The node lists the items past the limit under fan_overflow when it
// completes.
func setFanFields(d *DispatchNode, node, state map[string]any) {
	items, empty := fanItems(node, state)
	if limit, _ := node["fan_limit"].(int); limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	d.FanItems, d.FanEmpty, d.FanItemPlaceholder = items, empty, fanPlaceholder(node)
}

// fanItems reads a fan's items from the upstream output its fan_source names
// in state, so the runner can dispatch one backend.Run per item in parallel.
// A list (an upstream JSON array) gives one item per element; a string is
// parsed as a JSON array, a markdown numbered list, or newline-delimited
// lines (parseFanSource). empty is true when the list or the string holds no
// items. Any other value, or none, gives no items and empty false: the node
// runs as one ordinary call on its prompt.
func fanItems(node, state map[string]any) (items []string, empty bool) {
	src, _ := node["fan_source"].(string)
	if src == "" {
		return nil, false
	}
	switch v := state[src].(type) {
	case string:
		items = parseFanSource(v)
	case []any:
		items = fanItemsFromList(v)
	default:
		return nil, false
	}
	return items, len(items) == 0
}

// fanPlaceholder is the token of a fan's prompt each item fills: its
// fan_placeholder, {item} by default.
func fanPlaceholder(node map[string]any) string {
	if p, _ := node["fan_placeholder"].(string); p != "" {
		return p
	}
	return "{item}"
}

// fanItemsFromList turns a fan_source list into items, one per element:
// a string element as it is, any other element JSON-encoded.
func fanItemsFromList(list []any) []string {
	var items []string
	for _, v := range list {
		if s, ok := v.(string); ok {
			items = append(items, s)
			continue
		}
		b, _ := json.Marshal(v)
		items = append(items, string(b))
	}
	return items
}

// parseFanSource parses a fan_source value (the upstream node's
// output) into a slice of items. Three formats are accepted:
//
//  1. JSON array of strings: `["item1","item2","item3"]`
//  2. Markdown numbered list: `1. ... \n2. ...` (multi-line items
//     coalesce until the next `<digit>.` boundary or blank line).
//  3. Newline-delimited list: each non-empty line becomes an item.
//
// A JSON array of strings wins even when it is empty. Otherwise
// whichever of the others produces a non-empty list wins, in priority
// order. Returns no items when none can be extracted (the runner then
// completes the fan without a backend call).
func parseFanSource(src string) []string {
	s := strings.TrimSpace(src)
	if s == "" {
		return nil
	}
	if arr, ok := jsonStringArray(s); ok {
		return arr
	}
	return bestList(parseMarkdownNumberedList(s), plainLines(s))
}

// jsonStringArray parses s as a JSON array of strings.
func jsonStringArray(s string) ([]string, bool) {
	if !strings.HasPrefix(s, "[") {
		return nil, false
	}
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return nil, false
	}
	return arr, true
}

// bestList picks a fan's items from a markdown numbered list and from plain
// lines: a list of two or more items wins over a list of one, and between
// equals the numbered list wins. With no item in either, there are none.
func bestList(numbered, lines []string) []string {
	for _, least := range []int{2, 1} {
		for _, list := range [][]string{numbered, lines} {
			if len(list) >= least {
				return list
			}
		}
	}
	return nil
}

// plainLines is each non-empty line of s, trimmed, that is not a heading
// (#) or a divider (---).
func plainLines(s string) []string {
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if isItemLine(ln) {
			lines = append(lines, ln)
		}
	}
	return lines
}

func isItemLine(ln string) bool {
	return ln != "" && !strings.HasPrefix(ln, "#") && !strings.HasPrefix(ln, "---")
}

// numberedItemStart matches a numbered-list start at the beginning of a
// line: "1." or "1)", optionally in bold (`**1.**`).
var numberedItemStart = regexp.MustCompile(`(?m)^\s*(?:\*\*)?(\d+)[.)](?:\*\*)?\s+`)

// parseMarkdownNumberedList recognises lines that begin with `<digit>.`
// or `**<digit>.**` as item starts and groups everything up to the
// next item-start into one item. Trims the leading numbering token.
// Empty and divider-only items are dropped.
func parseMarkdownNumberedList(s string) []string {
	idx := numberedItemStart.FindAllStringIndex(s, -1)
	if len(idx) == 0 {
		return nil
	}
	out := make([]string, 0, len(idx))
	for i := range idx {
		if item := numberedItem(s, idx, i); keptItem(item) {
			out = append(out, item)
		}
	}
	return out
}

// numberedItem is the text of the i-th numbered item, trimmed: from the end
// of its numbering to the start of the next item's numbering.
func numberedItem(s string, idx [][]int, i int) string {
	end := len(s)
	if i+1 < len(idx) {
		end = idx[i+1][0]
	}
	return strings.TrimSpace(s[idx[i][1]:end])
}

// keptItem reports whether a numbered item is kept: it is neither empty nor
// a divider.
func keptItem(item string) bool {
	return item != "" && item != "---"
}

// writeSourceOverflows writes the overflow of every other parallel_fan with
// fan_limit whose fan_source the completing node set, in its outputs or its
// state_updates, from the new source. The overflow is then in state whether
// the fan completes, fails or is skipped; the fan's own completion writes it
// again from the source it read.
func writeSourceOverflows(defn map[string]any, nodeName string, outputs, state map[string]any) {
	nodes, _ := defn["nodes"].(map[string]any)
	self, _ := nodes[nodeName].(map[string]any)
	updates, _ := self["state_updates"].(map[string]any)
	for name, raw := range nodes {
		node, _ := raw.(map[string]any)
		if name != nodeName && setsFanSource(node, outputs, updates) {
			writeOverflow(node, state)
		}
	}
}

// setsFanSource reports whether node is a parallel_fan whose fan_source is
// among a completing node's outputs or state_updates.
func setsFanSource(node, outputs, updates map[string]any) bool {
	t, _ := node["type"].(string)
	src, _ := node["fan_source"].(string)
	_, inOutputs := outputs[src]
	_, inUpdates := updates[src]
	return t == "parallel_fan" && src != "" && (inOutputs || inUpdates)
}

// writeOverflow writes a fan's overflow to state when it has fan_limit.
func writeOverflow(node, state map[string]any) {
	if key, rest, ok := fanOverflow(node, state); ok {
		state[key] = rest
	}
}

// fanOverflowFor looks the node up in the definition and applies
// fanOverflow to it.
func fanOverflowFor(defn map[string]any, nodeName string, state map[string]any) (string, []any, bool) {
	nodes, _ := defn["nodes"].(map[string]any)
	node, _ := nodes[nodeName].(map[string]any)
	if node == nil {
		return "", nil, false
	}
	return fanOverflow(node, state)
}

// fanOverflow is what a parallel_fan with fan_limit writes under its
// fan_overflow key when it completes: the items past the limit, in order,
// or an empty list. A list source keeps its elements as they are; a string
// source gives the strings parseFanSource reads from it. It returns
// ok=false for any other node.
func fanOverflow(node map[string]any, state map[string]any) (key string, rest []any, ok bool) {
	key, _ = node["fan_overflow"].(string)
	limit, _ := node["fan_limit"].(int)
	if key == "" || limit < 1 {
		return "", nil, false
	}
	src, _ := node["fan_source"].(string)
	items := sourceItems(state[src])
	rest = []any{}
	if len(items) > limit {
		rest = append(rest, items[limit:]...)
	}
	return key, rest, true
}

// sourceItems is a fan_source value as overflow items: a list's elements as
// they are, a string's items as parseFanSource reads them, and none for any
// other value.
func sourceItems(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case string:
		var items []any
		for _, s := range parseFanSource(x) {
			items = append(items, s)
		}
		return items
	}
	return nil
}
