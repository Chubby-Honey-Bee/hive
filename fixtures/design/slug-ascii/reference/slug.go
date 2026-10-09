// Package slug turns titles into URL slugs.
package slug

import "strings"

// MaxLen is the longest slug Slugify returns, in bytes.
const MaxLen = 32

// Slugify returns an ASCII slug for s: lowercase letters and digits, runs
// of anything else as one hyphen, trimmed, at most MaxLen bytes cut at a
// word boundary, or "untitled" when nothing is kept.
func Slugify(s string) string {
	var b strings.Builder
	hyphen := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
			fallthrough
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteByte(c)
		default:
			hyphen = true
		}
	}
	out := b.String()
	if out == "" {
		return "untitled"
	}
	if len(out) > MaxLen {
		if i := strings.LastIndexByte(out[:MaxLen+1], '-'); i >= 0 {
			out = out[:i]
		} else {
			out = out[:MaxLen]
		}
	}
	return out
}
