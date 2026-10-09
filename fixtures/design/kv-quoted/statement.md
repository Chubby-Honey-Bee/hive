# Implement a flat key=value parser with quoted values

Package `kv` (file `kv.go`) exports `func Parse(r io.Reader) (map[string]string, error)`. Implement it, and add the error type below. The package name and the signature stay.

The format, line by line:

- A line whose first non-blank character is `#`, and a blank line, is skipped.
- `key = value`: the key is the text before the first `=`, with surrounding blanks trimmed; an empty key is malformed. There are no sections: a line whose first non-blank character is `[` is malformed, and so is any other non-blank line without `=`.
- An unquoted value is the text after `=` up to the first `#`, if any, with surrounding blanks trimmed. It may be empty and may contain blanks inside.
- A value whose first non-blank character is `"` is quoted: it runs to the next unescaped `"`. Inside it, `\"`, `\\`, `\n` and `\t` stand for a quote, a backslash, a newline and a tab; any other `\` sequence is malformed; `#` is literal. After the closing quote only blanks or a `#` comment may follow. A quote never closed is malformed.
- A key that repeats is malformed at its second occurrence, with the message `duplicate key K`.

A malformed line stops parsing with `*ParseError`: `type ParseError struct { Line int; Msg string }`, where `Line` is the 1-based line number and `Error()` returns `line N: Msg`. On success the map holds every key, and it is never nil.
