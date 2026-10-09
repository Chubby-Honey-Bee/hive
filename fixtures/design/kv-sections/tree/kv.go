// Package kv parses simple key=value configuration text.
package kv

import "io"

// Parse reads key=value lines from r into a map. Not yet implemented: it
// returns an empty map.
func Parse(r io.Reader) (map[string]string, error) {
	return map[string]string{}, nil
}
