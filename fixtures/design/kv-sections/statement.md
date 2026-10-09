# Implement a sectioned key=value parser

Package `kv` (file `kv.go`) exports `func Parse(r io.Reader) (map[string]string, error)`. Implement it, and add the error type below. The package name and the signature stay.

The format, line by line:

- A line whose first non-blank character is `#` or `;`, and a blank line, is skipped.
- `[name]` (blanks around `name` trimmed) starts a section. Every later `key = value` line is stored under `name.key`. Lines before any section are stored under `key` alone. A header whose name is empty is malformed.
- `key = value`: the key is the text before the first `=`, the value the text after it, both with surrounding blanks trimmed. The value may be empty and may contain further `=` characters. An empty key is malformed.
- Any other non-blank line is malformed.
- When a key repeats, the last value wins.

A malformed line stops parsing with `*ParseError`: `type ParseError struct { Line int; Msg string }`, where `Line` is the 1-based line number and `Error()` returns `line N: Msg`. On success the map holds every key, and it is never nil.
