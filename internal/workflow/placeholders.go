package workflow

import "strings"

// Placeholder is a {…} token of a node's template that the engine left as
// written, at its byte offset in the resolved prompt: a {comb.…} or {cde.…}
// token, a fan's item placeholder, or a key state lacks. The runner fills
// those it knows (FillLeft) and reads no other brace, so text a value
// brought in, such as a context pack, is never taken for a token.
type Placeholder struct {
	Offset int
	Token  string // with its braces
}

// fillPlaceholders replaces each {key} in template that lookup resolves,
// in one pass: text a value brings in is not read again, so a placeholder
// or run token inside a value (a context pack, a question, a model's
// output) stays as written, whatever order the keys come in. A {key}
// lookup does not resolve stays as written, and is returned among left at
// its offset in the result; left is never nil.
func fillPlaceholders(template string, lookup func(key string) (string, bool)) (out string, left []Placeholder) {
	f := &filler{lookup: lookup, left: []Placeholder{}}
	rest, more := template, true
	for more {
		rest, more = f.step(rest)
	}
	f.b.WriteString(rest)
	return f.b.String(), f.left
}

// filler is fillPlaceholders' pass: the text written so far and the
// placeholders it left.
type filler struct {
	lookup func(key string) (string, bool)
	b      strings.Builder
	left   []Placeholder
}

// step writes rest up to and including its next {…} token, filled or left,
// and returns the text after it; it reports false, consuming nothing, when
// rest holds no further token.
func (f *filler) step(rest string) (string, bool) {
	open, end, ok := nextBraces(rest)
	if !ok {
		return rest, false
	}
	if inner := strings.LastIndexByte(rest[open+1:end], '{'); inner >= 0 {
		// "{a {b}": the placeholder, if any, opens at the later brace.
		f.b.WriteString(rest[:open+1+inner])
		return rest[open+1+inner:], true
	}
	f.b.WriteString(rest[:open])
	f.fill(rest[open : end+1])
	return rest[end+1:], true
}

// nextBraces is the offset of the first { in s and of the first } after it.
func nextBraces(s string) (open, end int, ok bool) {
	open = strings.IndexByte(s, '{')
	if open < 0 {
		return 0, 0, false
	}
	n := strings.IndexByte(s[open+1:], '}')
	if n < 0 {
		return 0, 0, false
	}
	return open, open + 1 + n, true
}

// fill writes the value lookup gives a {key} token, or the token as it is,
// recorded among the placeholders left at its offset.
func (f *filler) fill(token string) {
	if v, ok := f.lookup(token[1 : len(token)-1]); ok {
		f.b.WriteString(v)
		return
	}
	f.left = append(f.left, Placeholder{Offset: f.b.Len(), Token: token})
	f.b.WriteString(token)
}

// FillLeft fills each placeholder of left in prompt that fill resolves by
// its key, the text between its braces, and returns the prompt and the
// placeholders still left, at their new offsets. Text fill brings in is not
// read. With left nil, as for a node the engine did not hand out, every
// {…} token of prompt is a placeholder.
func FillLeft(prompt string, left []Placeholder, fill func(key string) (string, bool)) (string, []Placeholder) {
	if left == nil {
		_, left = fillPlaceholders(prompt, func(string) (string, bool) { return "", false })
	}
	var b strings.Builder
	still := []Placeholder{}
	pos := 0
	for _, p := range left {
		b.WriteString(prompt[pos:p.Offset])
		if v, ok := fill(p.Token[1 : len(p.Token)-1]); ok {
			b.WriteString(v)
		} else {
			still = append(still, Placeholder{Offset: b.Len(), Token: p.Token})
			b.WriteString(p.Token)
		}
		pos = p.Offset + len(p.Token)
	}
	b.WriteString(prompt[pos:])
	return b.String(), still
}
