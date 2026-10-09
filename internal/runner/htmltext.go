package runner

// htmlText reduces an HTML page to its readable text for web_fetch
// (runner.md § Context window guard): markup, scripts and styles out;
// headings as lines prefixed with one # a level; paragraphs, list items,
// table rows and other block elements on lines of their own; links as
// [text](href), the href resolved against the page's URL; entities decoded;
// runs of whitespace folded to one space outside <pre>. It is a scanner,
// not a parser: it keeps no tree, so it needs no dependency and reads a
// page that is not well formed as a browser would in the main.

import (
	"html"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// paragraphTags stand apart from their neighbours by a blank line.
var paragraphTags = map[string]bool{
	"blockquote": true, "dl": true, "figure": true, "hr": true, "ol": true, "p": true, "pre": true, "table": true, "title": true, "ul": true,
}

// blockTags start and end a line.
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "body": true, "caption": true, "dd": true, "details": true, "dialog": true,
	"div": true, "dt": true, "fieldset": true, "figcaption": true, "footer": true, "form": true, "header": true, "html": true,
	"legend": true, "li": true, "main": true, "nav": true, "option": true, "section": true, "summary": true, "tbody": true,
	"tfoot": true, "thead": true, "tr": true,
}

// skipTags hold nothing readable: their content is dropped to the end tag.
var skipTags = map[string]bool{"noscript": true, "script": true, "style": true, "svg": true, "template": true}

// htmlText is src as readable text; base, when not nil, resolves its links.
func htmlText(src string, base *url.URL) string {
	w := &textWriter{nl: 2, link: -1, heading: -1}
	for i := 0; i < len(src); {
		i = w.scan(src, i, base)
	}
	return strings.TrimSpace(string(w.buf))
}

// scan lays out what starts at src[i], text or markup, and returns where
// what follows it starts. Comments, doctypes, CDATA sections and processing
// instructions lay out nothing.
func (w *textWriter) scan(src string, i int, base *url.URL) int {
	if src[i] != '<' {
		return w.textRun(src, i)
	}
	switch {
	case strings.HasPrefix(src[i:], "<!--"):
		return skipPast(src, i+4, "-->")
	case markupDeclaration(src, i):
		return skipPast(src, i, ">")
	}
	return w.element(src, i, base)
}

// textRun writes the text from src[i] to the next tag, entities decoded,
// and returns where the tag starts.
func (w *textWriter) textRun(src string, i int) int {
	j := strings.IndexByte(src[i:], '<')
	if j < 0 {
		j = len(src) - i
	}
	w.text(html.UnescapeString(src[i : i+j]))
	return i + j
}

// markupDeclaration reports whether the "<" at src[i] opens a doctype, a
// CDATA section or a processing instruction.
func markupDeclaration(src string, i int) bool {
	return i+1 < len(src) && (src[i+1] == '!' || src[i+1] == '?')
}

// skipPast is where src goes on after the first marker from src[from]: the
// end of src when there is none.
func skipPast(src string, from int, marker string) int {
	end := strings.Index(src[from:], marker)
	if end < 0 {
		return len(src)
	}
	return from + end + len(marker)
}

// element lays out the tag at src[i] and returns where what follows it
// starts: for an element that holds nothing readable (skipTags), past its
// end tag. A "<" that starts no tag is text.
func (w *textWriter) element(src string, i int, base *url.URL) int {
	tag, href, closing, n := parseTag(src[i:])
	if n == 0 {
		w.text("<")
		return i + 1
	}
	i += n
	if !closing && skipTags[tag] {
		return skipElement(src, i, tag)
	}
	w.tag(tag, href, closing, base)
	return i
}

// skipElement is where src goes on after the content of tag, from src[i]:
// past its end tag, or the end of src when it has none.
func skipElement(src string, i int, tag string) int {
	end := endTagIndex(src[i:], tag)
	if end < 0 {
		return len(src)
	}
	_, _, _, n := parseTag(src[i+end:])
	return i + end + n
}

// parseTag reads the tag at the start of s, which is "<": its lower-case
// name, its href attribute, whether it is an end tag, and its length. n is
// 0 when s does not start a tag, as "< " or "<3" do not.
func parseTag(s string) (tag, href string, closing bool, n int) {
	closing = strings.HasPrefix(s, "</")
	start := 1
	if closing {
		start = 2
	}
	end := skipWhile(s, start, isTagNameByte)
	if end == start || !isASCIILetter(s[start]) {
		return "", "", false, 0
	}
	href, n = tagAttributes(s, end)
	return strings.ToLower(s[start:end]), href, closing, n
}

// tagAttributes reads a tag's attributes, from s[i] to the > that closes
// the tag: its href attribute, unescaped, and where the tag ends, past the
// >, or at the end of s when nothing closes it.
func tagAttributes(s string, i int) (href string, n int) {
	for i = skipWhile(s, i, isAttrGap); i < len(s) && s[i] != '>'; i = skipWhile(s, i, isAttrGap) {
		var name, value string
		name, value, i = tagAttribute(s, i)
		if name == "href" {
			href = html.UnescapeString(value)
		}
	}
	return href, min(i+1, len(s))
}

// tagAttribute reads the attribute at s[i]: its lower-case name, its value,
// "" when it has none, and where it ends.
func tagAttribute(s string, i int) (name, value string, end int) {
	nameEnd := skipWhile(s, i, isAttrNameByte)
	name = strings.ToLower(s[i:nameEnd])
	i = skipWhile(s, nameEnd, isSpace)
	if i >= len(s) || s[i] != '=' {
		return name, "", i
	}
	value, end = attrValue(s, skipWhile(s, i+1, isSpace))
	return name, value, end
}

// attrValue reads the attribute value at s[i], quoted or bare, and where it
// ends.
func attrValue(s string, i int) (string, int) {
	if i < len(s) && (s[i] == '"' || s[i] == '\'') {
		return quotedValue(s, i)
	}
	end := skipWhile(s, i, isBareValueByte)
	return s[i:end], end
}

// quotedValue reads the value quoted at s[i] and where it ends, past its
// closing quote; one whose quote does not close runs to the end of s.
func quotedValue(s string, i int) (string, int) {
	end := strings.IndexByte(s[i+1:], s[i])
	if end < 0 {
		return s[i+1:], len(s)
	}
	return s[i+1 : i+1+end], i + 1 + end + 1
}

// skipWhile is where the bytes of s from s[i] that in reports stop.
func skipWhile(s string, i int, in func(byte) bool) int {
	for i < len(s) && in(s[i]) {
		i++
	}
	return i
}

// isTagNameByte reports whether c may be part of a tag's name.
func isTagNameByte(c byte) bool {
	return isASCIILetter(c) || isASCIIDigit(c) || c == '-' || c == ':'
}

// isAttrGap reports whether c may come between a tag's attributes.
func isAttrGap(c byte) bool { return isSpace(c) || c == '/' }

// isAttrNameByte reports whether c may be part of an attribute's name.
func isAttrNameByte(c byte) bool { return !isSpace(c) && c != '=' && c != '>' && c != '/' }

// isBareValueByte reports whether c may be part of an unquoted attribute
// value.
func isBareValueByte(c byte) bool { return !isSpace(c) && c != '>' }

// endTagIndex is where s's first end tag for tag starts, or -1.
func endTagIndex(s, tag string) int {
	for i := 0; i+2+len(tag) <= len(s); i++ {
		if endTagAt(s, i, tag) {
			return i
		}
	}
	return -1
}

// endTagAt reports whether an end tag for tag, in any case, starts at s[i],
// which leaves room for one.
func endTagAt(s string, i int, tag string) bool {
	return strings.HasPrefix(s[i:], "</") && strings.EqualFold(s[i+2:i+2+len(tag)], tag) && tagNameEnds(s, i+2+len(tag))
}

// tagNameEnds reports whether a tag's name ends at s[j]: s ends there, or
// the tag does, or an attribute or a slash follows.
func tagNameEnds(s string, j int) bool {
	return j == len(s) || s[j] == '>' || isSpace(s[j]) || s[j] == '/'
}

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// isASCIIDigit reports whether c is a digit, 0 to 9.
func isASCIIDigit(c byte) bool { return '0' <= c && c <= '9' }

func isSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	}
	return false
}

// textWriter lays the text out as it is scanned.
type textWriter struct {
	buf []byte
	// nl is how many newlines buf ends with; 2 at the start, so nothing
	// leads the text.
	nl int
	// space is set when whitespace was read since the last character of
	// the line, and written as one space before the next.
	space bool
	// marker is set when the line holds only a heading's or a list item's
	// marker, which a block boundary then leaves alone.
	marker bool
	// pre is the depth inside <pre>, and preStart is set from a <pre> to
	// its first text.
	pre      int
	preStart bool
	// link is where an open link's text starts in buf, after its "[", and
	// href its target; -1 when none is open.
	link int
	href string
	// heading is where an open heading's marker starts in buf; -1 when
	// none is open.
	heading int
}

// text writes s, folding its whitespace outside <pre>.
func (w *textWriter) text(s string) {
	if w.pre > 0 {
		w.preText(s)
		return
	}
	for _, r := range s {
		if unicode.IsSpace(r) {
			w.space = true
			continue
		}
		w.put(r)
	}
}

// preText writes s, text inside <pre>, as it is, but for a newline right
// after the tag, which a browser drops.
func (w *textWriter) preText(s string) {
	if w.preStart {
		s = strings.TrimPrefix(strings.TrimPrefix(s, "\r"), "\n")
		w.preStart = false
	}
	w.buf = append(w.buf, s...)
	w.recount()
}

// put writes one character of the line, after the space it was read after;
// a marker already ends in one.
func (w *textWriter) put(r rune) {
	if w.space && w.nl == 0 && !w.marker {
		w.buf = append(w.buf, ' ')
	}
	w.space = false
	w.buf = utf8.AppendRune(w.buf, r)
	w.nl, w.marker = 0, false
}

// lines ends the line and leaves at least k newlines, unless the line holds
// only a marker.
func (w *textWriter) lines(k int) {
	w.space = false
	if w.marker {
		return
	}
	for w.nl < k {
		w.buf = append(w.buf, '\n')
		w.nl++
	}
}

// recount sets nl from buf's end after text was appended or removed.
func (w *textWriter) recount() {
	w.nl, w.space = 0, false
	for i := len(w.buf) - 1; i >= 0 && w.buf[i] == '\n'; i-- {
		w.nl++
	}
	if len(w.buf) == 0 {
		w.nl = 2
	}
}

// tagLayouts lay out the tags whose layout is more than a line break, but
// for links and headings, which tag lays out itself.
var tagLayouts = map[string]func(w *textWriter, closing bool){
	"br":  (*textWriter).lineBreak,
	"li":  (*textWriter).listItem,
	"td":  (*textWriter).cell,
	"th":  (*textWriter).cell,
	"pre": (*textWriter).preformatted,
}

// headingLevels are the heading tags' levels.
var headingLevels = map[string]int{"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}

// tag lays out one tag.
func (w *textWriter) tag(name, href string, closing bool, base *url.URL) {
	if layout, ok := tagLayouts[name]; ok {
		layout(w, closing)
		return
	}
	switch level := headingLevels[name]; {
	case name == "a":
		w.anchor(href, closing, base)
	case level > 0:
		w.headingTag(level, closing)
	default:
		w.blockBreak(name)
	}
}

// lineBreak lays out <br>: a newline, up to a blank line.
func (w *textWriter) lineBreak(bool) {
	w.space = false
	if w.nl < 2 {
		w.buf = append(w.buf, '\n')
		w.nl++
	}
}

// listItem lays out <li>: a line of its own, which opens with "- ".
func (w *textWriter) listItem(closing bool) {
	w.lines(1)
	if !closing {
		w.buf = append(w.buf, "- "...)
		w.nl, w.marker = 0, true
	}
}

// cell lays out <td> and <th>: " | " between the cells of a row.
func (w *textWriter) cell(closing bool) {
	w.space = false
	if !closing && w.nl == 0 && !w.marker {
		w.buf = append(w.buf, " | "...)
	}
}

// preformatted lays out <pre>, inside which whitespace stands: a blank line
// before and after.
func (w *textWriter) preformatted(closing bool) {
	w.lines(2)
	if closing {
		w.pre = max(w.pre-1, 0)
	} else {
		w.pre++
		w.preStart = true
	}
}

// anchor opens a link to href, or closes the open one.
func (w *textWriter) anchor(href string, closing bool, base *url.URL) {
	if closing {
		w.closeLink()
		return
	}
	w.openLink(href, base)
}

// headingTag opens a heading of level, its marker one # a level, or closes
// the open one.
func (w *textWriter) headingTag(level int, closing bool) {
	if closing {
		w.closeHeading()
		return
	}
	w.lines(2)
	w.heading = len(w.buf)
	w.buf = append(w.buf, strings.Repeat("#", level)+" "...)
	w.nl, w.marker = 0, true
}

// blockBreak lays out a block tag: a paragraph tag stands apart by a blank
// line, another block tag starts and ends a line. Any other tag lays out
// nothing.
func (w *textWriter) blockBreak(name string) {
	switch {
	case paragraphTags[name]:
		w.lines(2)
	case blockTags[name]:
		w.lines(1)
	}
}

// openLink starts a link to href: a fragment of the page, a script and an
// empty href are left as text alone.
func (w *textWriter) openLink(href string, base *url.URL) {
	href = strings.TrimSpace(href)
	if w.link >= 0 || !linkTarget(href) {
		return
	}
	href = resolveHref(href, base)
	w.put('[')
	w.link, w.href = len(w.buf), href
}

// linkTarget reports whether href, trimmed, is a link's target: not empty,
// not a fragment of the page and not a script.
func linkTarget(href string) bool {
	return href != "" && href[0] != '#' && !strings.HasPrefix(strings.ToLower(href), "javascript:")
}

// resolveHref is href resolved against base, the page's URL, when both
// parse; href as it is otherwise.
func resolveHref(href string, base *url.URL) string {
	if u, err := url.Parse(href); err == nil && base != nil {
		return base.ResolveReference(u).String()
	}
	return href
}

// closeLink ends the open link with its target, or drops its "[" when it
// held no text.
func (w *textWriter) closeLink() {
	if w.link < 0 {
		return
	}
	if strings.TrimSpace(string(w.buf[w.link:])) == "" {
		w.buf = w.buf[:w.link-1]
		w.recount()
	} else {
		w.buf = append(w.buf, "]("+w.href+")"...)
		w.nl = 0
	}
	w.link = -1
}

// closeHeading ends the open heading, or drops its marker when it held no
// text.
func (w *textWriter) closeHeading() {
	if w.heading >= 0 && w.marker {
		w.buf = w.buf[:w.heading]
		w.marker = false
		w.recount()
	}
	w.heading = -1
	w.lines(2)
}
