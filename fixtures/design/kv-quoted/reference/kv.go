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

// Parse reads flat key=value lines from r, with quoted values and #
// comments. A repeated key is an error.
func Parse(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || text[0] == '#' {
			continue
		}
		fail := func(msg string) (map[string]string, error) { return nil, &ParseError{Line: line, Msg: msg} }
		if text[0] == '[' {
			return fail("sections are not supported")
		}
		key, raw, ok := strings.Cut(text, "=")
		if !ok {
			return fail("expected key = value")
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fail("empty key")
		}
		if _, dup := out[key]; dup {
			return fail("duplicate key " + key)
		}
		value, msg := unquote(strings.TrimSpace(raw))
		if msg != "" {
			return fail(msg)
		}
		out[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// unquote reads a value: quoted with the four escapes, or unquoted up to
// the first #. It returns the value, or why it is malformed.
func unquote(raw string) (string, string) {
	if raw == "" || raw[0] != '"' {
		value, _, _ := strings.Cut(raw, "#")
		return strings.TrimSpace(value), ""
	}
	var b strings.Builder
	rest := raw[1:]
	for i := 0; i < len(rest); i++ {
		switch c := rest[i]; c {
		case '"':
			tail := strings.TrimSpace(rest[i+1:])
			if tail != "" && tail[0] != '#' {
				return "", "text after the closing quote"
			}
			return b.String(), ""
		case '\\':
			if i+1 >= len(rest) {
				return "", "quote never closed"
			}
			i++
			switch rest[i] {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", fmt.Sprintf("unknown escape \\%c", rest[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "quote never closed"
}
