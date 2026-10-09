package schema

import (
	"maps"

	"gopkg.in/yaml.v3"
)

// MapEntry is one key and its value in a YAML mapping.
type MapEntry struct {
	Key, Value *yaml.Node
}

// MapEntries returns a YAML mapping's entries in order, with its merge key
// (`<<: *anchor`, or a list of anchors) resolved as yaml.v3 decodes it into
// a map, so what is read here is what the decoded workflow holds. A key the
// mapping sets itself wins over a merged one; among merged mappings the first
// to set a key wins; only the last merge key counts. The merged entries take
// the merge key's place. Aliases are followed, keys and values alike. It
// returns nil for a node that is not a mapping.
func MapEntries(m *yaml.Node) []MapEntry {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	own, lastMerge := ownKeys(m)
	var out []MapEntry
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, entriesAt(m, i, lastMerge, own)...)
	}
	return out
}

// ownKeys is the keys a mapping sets itself, and the index of its last merge
// key, -1 when it has none.
func ownKeys(m *yaml.Node) (map[string]bool, int) {
	own := map[string]bool{}
	lastMerge := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isMergeKey(m.Content[i]) {
			lastMerge = i
		} else {
			own[m.Content[i].Value] = true
		}
	}
	return own, lastMerge
}

// entriesAt is the entries the key at index i of a mapping gives: the entry
// itself; for the last merge key, the merged entries the mapping does not
// set itself; for any other merge key, none.
func entriesAt(m *yaml.Node, i, lastMerge int, own map[string]bool) []MapEntry {
	k, v := m.Content[i], deref(m.Content[i+1])
	switch {
	case !isMergeKey(k):
		return []MapEntry{{Key: k, Value: v}}
	case i != lastMerge || v == nil:
		return nil
	}
	return mergedEntries(mergeSources(v), own)
}

// mergeSources is the mappings a merge key's value names: one, or a list of
// them.
func mergeSources(v *yaml.Node) []*yaml.Node {
	if v.Kind == yaml.SequenceNode {
		return v.Content
	}
	return []*yaml.Node{v}
}

// mergedEntries is the entries of sources whose keys own does not hold, the
// first source to set a key winning.
func mergedEntries(sources []*yaml.Node, own map[string]bool) []MapEntry {
	taken := maps.Clone(own)
	var out []MapEntry
	for _, src := range sources {
		for _, e := range MapEntries(src) {
			if taken[e.Key.Value] {
				continue
			}
			taken[e.Key.Value] = true
			out = append(out, e)
		}
	}
	return out
}

// MapValue is the value under key in a YAML mapping, merge keys resolved
// (MapEntries), or nil.
func MapValue(m *yaml.Node, key string) *yaml.Node {
	for _, e := range MapEntries(m) {
		if e.Key.Value == key {
			return e.Value
		}
	}
	return nil
}

func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && k.ShortTag() == "!!merge"
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}
