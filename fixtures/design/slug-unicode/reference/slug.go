// Package slug turns titles into URL slugs.
package slug

import (
	"strings"
	"unicode"
)

// Slugify returns a slug for s: the letters and digits of any script,
// lowercased, runs of anything else as one hyphen, an apostrophe between two
// kept characters dropped, trimmed, or "untitled" when nothing is kept.
func Slugify(s string) string {
	runes := []rune(s)
	kept := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	var b strings.Builder
	hyphen := false
	for i, r := range runes {
		switch {
		case kept(r):
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(unicode.ToLower(r))
		case r == '\'' && i > 0 && i+1 < len(runes) && kept(runes[i-1]) && kept(runes[i+1]):
			// Dropped: no hyphen, and the pending state is unchanged.
		default:
			hyphen = true
		}
	}
	if b.Len() == 0 {
		return "untitled"
	}
	return b.String()
}
