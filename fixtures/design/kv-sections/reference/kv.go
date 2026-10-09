// Package kv parses simple key=value configuration text.
package kv

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ParseError is a malformed line.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Parse reads sectioned key=value lines from r. Keys under a [section] are
// stored as section.key; the last value of a repeated key wins.
func Parse(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	section := ""
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		switch {
		case text == "", text[0] == '#', text[0] == ';':
			continue
		case text[0] == '[':
			if !strings.HasSuffix(text, "]") {
				return nil, &ParseError{Line: line, Msg: "unterminated section header"}
			}
			name := strings.TrimSpace(text[1 : len(text)-1])
			if name == "" {
				return nil, &ParseError{Line: line, Msg: "empty section name"}
			}
			section = name + "."
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, &ParseError{Line: line, Msg: "expected key = value"}
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, &ParseError{Line: line, Msg: "empty key"}
		}
		out[section+key] = strings.TrimSpace(value)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
