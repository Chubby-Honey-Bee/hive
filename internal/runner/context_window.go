package runner

// The context-window guard (runner.md § Context window guard). A local
// server such as Ollama answers a prompt longer than the model's window by
// dropping its start, with no error: measured on Ollama 0.34.4, an
// 11,317-token prompt sent at num_ctx 4096 came back counted as 2,050
// prompt tokens and was answered. So before each call on the
// OpenAI-compatible backend the prompt's size is estimated and a call that
// would not fit is refused, and after it the server's own count is checked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// The estimate (textTokens and templateTokens). Each number is at or above
// what qwen3.5 and ministral-3 counted on the text measured (runner.md §
// Context window guard): English, German, French and Spanish prose, Go,
// YAML, JSON, minified JSON, CSV, hex hashes, a Markdown table, Russian,
// Japanese and Chinese.
const (
	// asciiLettersPerToken and twoByteLettersPerToken: a run of letters is
	// a token per this many ASCII letters, and per this many two-byte
	// letters (Latin with diacritics, Greek, Cyrillic), rounded up.
	asciiLettersPerToken   = 4.0
	twoByteLettersPerToken = 2.0
	// wideCharTokens is a character of three or more bytes: CJK, Indic
	// scripts, symbols, emoji. ministral-3 counted Japanese at 1.26 tokens
	// a character.
	wideCharTokens = 1.5
	// What a chat template adds around the messages: templateBaseTokens
	// and templateMessageTokens a message (qwen3.5 added 11 to a lone user
	// message); templateToolsTokens when the request offers tools (qwen3.5
	// wraps them in about 200 tokens of instructions); and
	// templateNoSystemTokens when it has no system message (ministral-3
	// then puts in a system prompt of its own, 554 tokens).
	templateBaseTokens     = 64
	templateMessageTokens  = 8
	templateToolsTokens    = 512
	templateNoSystemTokens = 768
)

const (
	// calibrationMargin: a server that counts a prompt at more than this
	// share of its estimate raises the model's later estimates, so the
	// densest count on the model stays this share of them. Once
	// minCalibrationCalls counts have passed the check, a sparser densest
	// count lowers them to the same share.
	calibrationMargin = 0.9
	// minCalibrationCalls is how many counts on a model at an endpoint must
	// pass the check before they may lower its estimates.
	minCalibrationCalls = 3
	// minCalibrationBytes is the smallest prompt a count is learned from,
	// so a chat template's fixed tokens do not dominate it.
	minCalibrationBytes = 2048
	// minReplyRoom is the room a call must leave for its reply, in tokens,
	// unless it asks for less.
	minReplyRoom = 1024
	// cutBytesPerToken bounds a plausible count: a server that counts
	// fewer than one token per this many bytes of a prompt's text has
	// dropped part of it. The sparsest text measured, Russian on qwen3.5,
	// counted 9.66 bytes a token.
	cutBytesPerToken = 16
	// minCutTokens is the fewest tokens a cut prompt keeps when no window
	// is known: a server that drops part of a prompt keeps at least half
	// its window, and the smallest window assumed is 2,048 tokens.
	minCutTokens = 1024
)

// contextWindowError is a call the guard refused, or a reply it rejected
// because the server dropped, or may have dropped, part of the prompt. It
// is never a rate limit, whatever numbers it names (isRateLimitError).
type contextWindowError struct{ msg string }

func (e *contextWindowError) Error() string { return e.msg }

// textTokens is the estimate of s's tokens: each ASCII digit, punctuation
// mark or symbol is a token, as is each run of whitespace but a lone space
// before a letter or a mark; a run of letters is a token per
// asciiLettersPerToken ASCII letters and per twoByteLettersPerToken two-byte
// letters, rounded up, and a token a letter when it touches a digit, as in
// a hash; any other two-byte character is a token, and a wider one
// wideCharTokens.
func textTokens(s string) float64 {
	e := tokenEstimate{onlySpace: true}
	for _, r := range s {
		e.read(r)
	}
	e.endRun(false)
	e.endSpaces(false)
	return e.n
}

// tokenEstimate is textTokens' count as it reads the text.
type tokenEstimate struct {
	n float64
	// ascii and twoByte count the letters of the run being read, and
	// touchesDigit says a digit came right before the run.
	ascii, twoByte int
	touchesDigit   bool
	// prevDigit says the last rune read was a digit.
	prevDigit bool
	// spaces counts the whitespace run being read, and onlySpace says it
	// holds nothing but ' '.
	spaces    int
	onlySpace bool
}

// read counts r.
func (e *tokenEstimate) read(r rune) {
	if r < utf8.RuneSelf && unicode.IsSpace(r) {
		e.space(r)
		return
	}
	digit := '0' <= r && r <= '9'
	e.endSpaces(digit)
	e.char(r, digit)
	e.prevDigit = digit
}

// space reads r, an ASCII whitespace rune: it ends the run of letters and
// joins the whitespace run.
func (e *tokenEstimate) space(r rune) {
	e.endRun(false)
	e.spaces++
	e.onlySpace = e.onlySpace && r == ' '
	e.prevDigit = false
}

// char counts r, which is not whitespace and is a digit when digit is set:
// a letter of at most two bytes joins the run of letters; a digit, a mark
// or a symbol ends the run and is a token; a wider character ends it and is
// wideCharTokens.
func (e *tokenEstimate) char(r rune, digit bool) {
	switch {
	case isNarrowLetter(r):
		e.letter(r)
	case digit:
		e.endRun(true)
		e.n++
	case r < 0x800:
		e.endRun(false)
		e.n++
	default:
		e.endRun(false)
		e.n += wideCharTokens
	}
}

// isNarrowLetter reports whether r is a letter of at most two bytes.
func isNarrowLetter(r rune) bool { return r < 0x800 && unicode.IsLetter(r) }

// letter adds r, a letter of at most two bytes, to the run of letters.
func (e *tokenEstimate) letter(r rune) {
	if e.ascii+e.twoByte == 0 {
		e.touchesDigit = e.prevDigit
	}
	if r < utf8.RuneSelf {
		e.ascii++
	} else {
		e.twoByte++
	}
}

// endRun ends the run of letters being read: a token a letter when it
// touches a digit, the one before it or, when nextIsDigit, the one after;
// else a token per asciiLettersPerToken ASCII letters and per
// twoByteLettersPerToken two-byte letters, rounded up.
func (e *tokenEstimate) endRun(nextIsDigit bool) {
	if e.ascii+e.twoByte == 0 {
		return
	}
	if e.touchesDigit || nextIsDigit {
		e.n += float64(e.ascii + e.twoByte)
	} else {
		e.n += math.Ceil(float64(e.ascii)/asciiLettersPerToken + float64(e.twoByte)/twoByteLettersPerToken)
	}
	e.ascii, e.twoByte, e.touchesDigit = 0, 0, false
}

// endSpaces ends the whitespace run being read, before a digit when
// beforeDigit is set: it is a token (spacesAreToken).
func (e *tokenEstimate) endSpaces(beforeDigit bool) {
	if e.spacesAreToken(beforeDigit) {
		e.n++
	}
	e.spaces, e.onlySpace = 0, true
}

// spacesAreToken reports whether the whitespace run being read is a token:
// any run but none at all and a lone space before a letter or a mark.
func (e *tokenEstimate) spacesAreToken(beforeDigit bool) bool {
	return e.spaces > 1 || !e.onlySpace || e.spaces > 0 && beforeDigit
}

// promptSize is what one chat request puts in the model's context.
type promptSize struct {
	// bytes is the text every chat template renders: each message's
	// content and tool calls, and the tool schemas.
	bytes int64
	// tokens is the estimate: those by textTokens, with the reasoning of
	// each assistant turn after the last user message, in `reasoning` or
	// `reasoning_content` as the backend echoes it back, which a template
	// may render (qwen3.5's does inside a tool loop and drops once a user
	// message follows), and templateTokens.
	tokens float64
}

// measurePrompt sizes body's messages and tools.
func measurePrompt(body map[string]any) promptSize {
	msgs, _ := body["messages"].([]map[string]any)
	lastUser, system := promptRoles(msgs)
	var p promptSize
	for i, m := range msgs {
		p.addMessage(m, i > lastUser)
	}
	p.tokens += templateBaseTokens + templateMessageTokens*float64(len(msgs))
	p.addTools(body["tools"])
	if !system {
		p.tokens += templateNoSystemTokens
	}
	return p
}

// promptRoles is where msgs' last user message is, -1 when they have none,
// and whether they hold a system message.
func promptRoles(msgs []map[string]any) (lastUser int, system bool) {
	lastUser = -1
	for i, m := range msgs {
		switch m["role"] {
		case "user":
			lastUser = i
		case "system":
			system = true
		}
	}
	return lastUser, system
}

// add counts s, text every chat template renders.
func (p *promptSize) add(s string) {
	p.bytes += int64(len(s))
	p.tokens += textTokens(s)
}

// addJSON counts v as the JSON a template renders.
func (p *promptSize) addJSON(v any) {
	js, _ := json.Marshal(v)
	p.add(string(js))
}

// addMessage counts message m: its content and tool calls, and its
// reasoning when it comes afterLastUser.
func (p *promptSize) addMessage(m map[string]any, afterLastUser bool) {
	if s, ok := m["content"].(string); ok {
		p.add(s)
	}
	if tc := m["tool_calls"]; tc != nil {
		p.addJSON(tc)
	}
	if afterLastUser {
		p.addReasoning(m)
	}
}

// addReasoning counts the reasoning of m, a turn after the last user
// message, in `reasoning` or `reasoning_content` as the backend echoes it
// back, which a template may render.
func (p *promptSize) addReasoning(m map[string]any) {
	for _, field := range []string{"reasoning", "reasoning_content"} {
		if s, ok := m[field].(string); ok {
			p.tokens += textTokens(s)
		}
	}
}

// addTools counts the tools a request offers, nil when it offers none:
// their schemas, and the instructions a template wraps them in.
func (p *promptSize) addTools(tools any) {
	if tools == nil {
		return
	}
	p.addJSON(tools)
	p.tokens += templateToolsTokens
}

// calibration holds, per (endpoint, model), the most tokens a server
// counted for each token the estimate gave a prompt, how many calls it has
// counted, and how many of those passed the check, and which endpoints are
// local servers. Run makes one per run, which every OpenAI-compatible
// backend built from the run's Config shares, so a count carries to every
// later call on that model at that endpoint, whichever node, fan item or
// repair makes it. A nil calibration keeps nothing.
type calibration struct {
	mu     sync.Mutex
	most   map[[2]string]float64
	calls  map[[2]string]int
	passed map[[2]string]int
	local  map[string]bool
}

// serverAt notes endpoint as a local server when server, the one found
// there, is Ollama, LM Studio or llama.cpp. Only there may counts lower an
// estimate: each runs a call whose prompt and output cap together pass the
// window, and a server that refuses such a call would refuse a cap set by a
// lowered estimate before counting the prompt, so no count could correct
// it.
func (c *calibration) serverAt(endpoint string, server localServer) {
	if c == nil || server == serverNone {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.local == nil {
		c.local = map[string]bool{}
	}
	c.local[endpoint] = true
}

// scale is what the estimate for model at endpoint is multiplied by, and
// bound what the prompt is held to the window at. When the most tokens
// counted per estimated token on it, over the margin, is above 1, both are
// that. When it is below 1, bound stays 1, so no count of other prompts can
// let a denser one past the window, and scale falls to it once
// minCalibrationCalls counts have passed the check at a local server
// (serverAt): the estimate then sets only the room left for the reply,
// which a denser prompt can overrun (check, windowStop). basis says how the
// estimate was made.
func (c *calibration) scale(endpoint, model string) (scale, bound float64, basis string) {
	if c == nil {
		return 1, 1, "by text class"
	}
	k := [2]string{endpoint, model}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.most[k] / calibrationMargin
	switch {
	case s > 1:
		return s, s, fmt.Sprintf("by text class, raised %.2f times by the densest of %d counted call(s) with a %.0f%% margin", s, c.calls[k], (1-calibrationMargin)*100)
	case c.lowers(s, k, endpoint):
		return s, 1, fmt.Sprintf("by text class, lowered to %.2f times by the densest of %d counted calls with a %.0f%% margin", s, c.calls[k], (1-calibrationMargin)*100)
	}
	return 1, 1, "by text class"
}

// lowers reports whether s, the scale the counts on k give, lowers its
// estimates: it is below 1, and minCalibrationCalls counts on k passed the
// check at endpoint, a local server (serverAt). c.mu is held.
func (c *calibration) lowers(s float64, k [2]string, endpoint string) bool {
	return s < 1 && c.passed[k] >= minCalibrationCalls && c.local[endpoint]
}

// observe records a call's count against its estimate at scale 1. passed
// says the check found nothing dropped; only such counts can lower later
// estimates.
func (c *calibration) observe(endpoint, model string, size promptSize, promptTokens int64, passed bool) {
	if c == nil || skipsCalibration(size, promptTokens) {
		return
	}
	c.record([2]string{endpoint, model}, float64(promptTokens)/size.tokens, passed)
}

// skipsCalibration reports whether a count of promptTokens for a prompt of
// size is not learned from: the prompt is under minCalibrationBytes, so a
// chat template's fixed tokens would dominate it, or either count is not
// positive.
func skipsCalibration(size promptSize, promptTokens int64) bool {
	return size.bytes < minCalibrationBytes || promptTokens <= 0 || size.tokens <= 0
}

// record notes a count on k of r tokens per estimated token, and whether it
// passed the check.
func (c *calibration) record(k [2]string, r float64, passed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.most == nil {
		c.most, c.calls, c.passed = map[[2]string]float64{}, map[[2]string]int{}, map[[2]string]int{}
	}
	if r > c.most[k] {
		c.most[k] = r
	}
	c.calls[k]++
	if passed {
		c.passed[k]++
	}
}

// contextFit is the guard's account of one call.
type contextFit struct {
	window   int64  // tokens; 0 when no window is known
	source   string // where the window came from
	endpoint string // the base URL the call goes to
	model    string
	size     promptSize
	scale    float64
	basis    string
	estimate int64 // estimated prompt tokens
	// bound is the estimate the prompt is held to the window at: estimate,
	// or the class count when the calibration lowered estimate below it.
	bound int64
	asked int64 // the output cap the request asked for; 0 for none
	sent  int64 // the output cap sent; 0 for none
	// counted and replied are the server's count of the prompt and the
	// reply, once check has read them.
	counted, replied int64
}

// replyFloor is the room the call must leave for its reply: minReplyRoom,
// or its cap when it asks for less.
func (f contextFit) replyFloor() int64 {
	if f.asked > 0 && f.asked < minReplyRoom {
		return f.asked
	}
	return minReplyRoom
}

// fitContext estimates the prompt body carries to endpoint and, when the
// request names a context window, refuses a prompt that would leave less
// than the reply floor of it, or whose bound reaches it, and lowers the
// output cap in body to the room left. So the prompt fits the window while
// its bound is at least the server's count, and the reply does while the
// estimate is. A lowered estimate need not be, and a reply that then runs
// into the window fails after the call (check, windowStop).
func fitContext(c *calibration, endpoint string, body map[string]any, req RunRequest) (contextFit, error) {
	f := newContextFit(c, endpoint, body, req)
	if f.window <= 0 {
		return f, nil
	}
	if err := f.refusal(); err != nil {
		return f, err
	}
	f.capReply(body)
	return f, nil
}

// newContextFit is the guard's estimate of the prompt body carries to
// endpoint, under the request's window and output cap, before any check.
func newContextFit(c *calibration, endpoint string, body map[string]any, req RunRequest) contextFit {
	model, _ := body["model"].(string)
	f := contextFit{window: req.ContextWindow, source: req.ContextWindowSource, endpoint: endpoint, model: model, size: measurePrompt(body), asked: req.MaxTokens, sent: req.MaxTokens}
	var bound float64
	f.scale, bound, f.basis = c.scale(endpoint, model)
	f.estimate = int64(math.Ceil(f.size.tokens * f.scale))
	f.bound = int64(math.Ceil(f.size.tokens * bound))
	return f
}

// refusal is the error for a prompt that would not fit f's window: its
// estimate reaches the window, or leaves less than the reply floor of it,
// or its bound reaches it. nil when it fits.
func (f contextFit) refusal() error {
	if f.estimate >= f.window {
		return &contextWindowError{fmt.Sprintf(
			"context window: the prompt is about %d tokens by estimate (%d bytes, estimated %s), and %s's context window is %d tokens (%s): sent, it would lose its earliest part. Shorten the prompt (for a swarm, --persona-profile lean, or fewer foragers) or run the model with a larger window",
			f.estimate, f.size.bytes, f.basis, f.model, f.window, f.source)}
	}
	if room, floor := f.window-f.estimate, f.replyFloor(); room < floor {
		return &contextWindowError{fmt.Sprintf(
			"context window: the prompt is about %d tokens by estimate (%d bytes, estimated %s), which leaves %d of %s's %d-token context window (%s) for the reply, fewer than the %d a reply needs. Shorten the prompt (for a swarm, --persona-profile lean, or fewer foragers) or run the model with a larger window",
			f.estimate, f.size.bytes, f.basis, room, f.model, f.window, f.source, floor)}
	}
	if f.bound >= f.window {
		return &contextWindowError{fmt.Sprintf(
			"context window: the prompt is %d tokens by text class (%d bytes), and %s's context window is %d tokens (%s): sent, it could lose its earliest part. The server's counts of earlier prompts put it at about %d (estimated %s), but they cannot show that this prompt is no denser than those, so they set only the room for the reply. Shorten the prompt (for a swarm, --persona-profile lean, or fewer foragers) or run the model with a larger window",
			f.bound, f.size.bytes, f.model, f.window, f.source, f.estimate, f.basis)}
	}
	return nil
}

// capReply lowers the output cap, in f and in body, to the room the prompt
// leaves in the window, when the request asks for no cap or a larger one.
func (f *contextFit) capReply(body map[string]any) {
	room := f.window - f.estimate
	if f.asked <= 0 || f.asked > room {
		f.sent = room
		body["max_completion_tokens"] = room
		body["max_tokens"] = room
	}
}

// toolResultShare is the share of the room a tool result may take: half of
// what the prompt it joins leaves under the window and the reply floor. Each
// result in a loop takes at most half of what the ones before it left, so
// the results alone never fill the room, and a loop that fetches a second
// page has it cut, not refused.
const toolResultShare = 0.5

// toolResultRoom is the most tokens by text class a tool result may add to
// the prompt body carries, as the content of its last message, for the next
// call to pass fitContext: toolResultShare of what the prompt leaves under
// the window less the reply floor at the estimate's scale, and under the
// window at the bound's. Negative when the prompt already leaves none. ok is
// false when no window is known.
func toolResultRoom(c *calibration, endpoint string, body map[string]any, req RunRequest) (room float64, ok bool) {
	if req.ContextWindow <= 0 {
		return 0, false
	}
	model, _ := body["model"].(string)
	scale, bound, _ := c.scale(endpoint, model)
	floor := contextFit{asked: req.MaxTokens}.replyFloor()
	fits := math.Min(float64(req.ContextWindow-floor)/scale, float64(req.ContextWindow-1)/bound)
	return (fits - measurePrompt(body).tokens) * toolResultShare, true
}

// toolResultCap is the most bytes a tool result may add to the conversation,
// its cut note included, when no context window is known to hold it to: on
// the Anthropic SDK and Gemini backends, which are given none, and on the
// OpenAI-compatible backend at an endpoint whose window is unknown.
const toolResultCap = 50_000

// toolResultBound is what a tool result is held to before it joins the
// conversation, the one rule every SDK backend follows: when window is set,
// room, the tokens by text class the next call leaves it under the context
// window (toolResultRoom); else toolResultCap bytes.
type toolResultBound struct {
	window bool
	room   float64
}

// fits reports whether s, a result as it would be sent, is within b.
func (b toolResultBound) fits(s string) bool {
	if b.window {
		return textTokens(s) <= b.room
	}
	return len(s) <= toolResultCap
}

// why is what a cut note says of the rest of a result cut to b.
func (b toolResultBound) why() string {
	if b.window {
		return "the rest would not fit the model's context window with room for the reply"
	}
	return fmt.Sprintf("the rest is past the %d-byte cap on a tool result", toolResultCap)
}

// hold is content, a tool result, held to b. A result within b is returned as
// it is. A longer one is cut at a rune boundary so that what is kept and the
// note that ends it fit: the note says how many bytes of how many were kept
// and why, then what rest says, how to get the rest given what was kept, when
// the tool has a way (rest nil when it has none). kept is how many bytes of
// content result holds: all of them when it was not cut.
func (b toolResultBound) hold(content string, rest func(kept string) string) (result string, kept int) {
	if b.fits(content) {
		return content, len(content)
	}
	n := cutLength(content, b, rest)
	return content[:n] + cutNote(content, content[:n], b.why(), rest), n
}

// fitToolResult is content, a tool result that is the content of body's last
// message, held to the room toolResultRoom gives it under the request's
// context window, or to toolResultCap when no window is known
// (toolResultBound.hold).
func fitToolResult(c *calibration, endpoint string, body map[string]any, req RunRequest, content string, rest func(kept string) string) (result string, kept int) {
	room, ok := toolResultRoom(c, endpoint, body, req)
	return toolResultBound{window: ok, room: room}.hold(content, rest)
}

// logToolCut logs, with logf when it is set, a result of the call to tool
// name that was cut to kept of the total bytes the tool returned: held to
// the context window when window is set, else to toolResultCap. A result
// that was not cut logs nothing.
func logToolCut(logf func(string, ...any), name string, kept, total int, window bool) {
	if kept == total || logf == nil {
		return
	}
	limit := fmt.Sprintf("the %d-byte cap on a tool result", toolResultCap)
	if window {
		limit = "the room the context window leaves it"
	}
	logf("tool %s: result cut to %d of %d bytes, %s", name, kept, total, limit)
}

// cutLength is how many bytes of content a cut result keeps: the longest
// prefix that, with its cutNote, fits b. At rune boundaries neither the
// estimate nor the length falls as a prefix grows, so the first length that
// does not fit bounds the kept prefix. A length inside a rune counts its
// split bytes as wide characters, a little over the whole, which can stop
// the search one rune early, never late; a cut inside a rune is moved back
// to its start and checked again.
func cutLength(content string, b toolResultBound, rest func(kept string) string) int {
	fits := func(n int) bool { return b.fits(content[:n] + cutNote(content, content[:n], b.why(), rest)) }
	n := sort.Search(len(content)+1, func(n int) bool { return !fits(n) }) - 1
	for n = runeStart(content, max(n, 0)); n > 0 && !fits(n); n = runeStart(content, n-1) {
	}
	return n
}

// cutNote ends a tool result that keeps kept of content: how many bytes of
// how many were kept, why, the rest was not, then what rest says, how to get
// the rest given what was kept, when the tool has a way (rest nil when it
// has none).
func cutNote(content, kept, why string, rest func(kept string) string) string {
	s := fmt.Sprintf("\n[cut: kept %d of %d bytes; %s.", len(kept), len(content), why)
	if rest != nil {
		if more := rest(kept); more != "" {
			s += " " + more
		}
	}
	return s + "]"
}

// runeStart is n, or the start of the rune content[n] is inside.
func runeStart(content string, n int) int {
	for n > 0 && n < len(content) && !utf8.RuneStart(content[n]) {
		n--
	}
	return n
}

// check reads the server's count of a call into f, fails the reply when
// verdict does, and records the count for later estimates: any count can
// raise them, and only one that passes counts toward lowering them.
func (f *contextFit) check(c *calibration, promptTokens, completionTokens int64) error {
	f.counted, f.replied = promptTokens, completionTokens
	err := f.verdict(promptTokens, completionTokens)
	c.observe(f.endpoint, f.model, f.size, promptTokens, err == nil)
	return err
}

// verdict fails a reply the server gave after dropping part of the
// conversation:
//   - a prompt counted at fewer than one token per cutBytesPerToken bytes,
//     window or no window, when the count is one a cut could leave (at
//     least minCutTokens);
//   - a prompt and reply that together ran past the window;
//   - a prompt counted at half the window or more and above its bound:
//     a server that drops the start of a prompt leaves at least half its
//     window, so such a count cannot show that nothing was dropped, and the
//     bound, which is meant to be at or above the count, was not.
//
// Past the first rule, a count under half the window shows that nothing was
// dropped.
func (f contextFit) verdict(promptTokens, completionTokens int64) error {
	if promptTokens <= 0 {
		return nil
	}
	if err := f.sparseCount(promptTokens); err != nil {
		return err
	}
	if f.window <= 0 {
		return nil
	}
	return f.windowVerdict(promptTokens, completionTokens)
}

// sparseCount fails a prompt counted at fewer than one token per
// cutBytesPerToken bytes, when the count is one a cut could leave (at least
// minCutTokens).
func (f contextFit) sparseCount(promptTokens int64) error {
	if promptTokens >= minCutTokens && promptTokens*cutBytesPerToken < f.size.bytes {
		return &contextWindowError{fmt.Sprintf(
			"context window: the server counted %d prompt tokens for %d bytes of prompt text, fewer than one token per %d bytes, which no tokenizer measured counts, so it dropped part of the prompt before answering%s",
			promptTokens, f.size.bytes, cutBytesPerToken, f.windowClause())}
	}
	return nil
}

// windowVerdict fails, under a known window, a prompt and reply that
// together ran past it, and a prompt counted at half of it or more and above
// its bound.
func (f contextFit) windowVerdict(promptTokens, completionTokens int64) error {
	if promptTokens+completionTokens > f.window {
		return &contextWindowError{fmt.Sprintf(
			"context window: the server counted %d prompt and %d reply tokens, more than %s's %d-token context window (%s), so it dropped the earliest part of the conversation while it answered%s",
			promptTokens, completionTokens, f.model, f.window, f.source, f.underEstimate(promptTokens))}
	}
	if 2*promptTokens >= f.window && promptTokens > f.bound {
		return &contextWindowError{fmt.Sprintf(
			"context window: the server counted %d prompt tokens, more than the %d estimated%s and at least half of %s's %d-token context window (%s). A server that drops the start of a prompt leaves such a count, so this reply may rest on part of the prompt. Shorten the prompt or run the model with a larger window",
			promptTokens, f.bound, f.boundClause(), f.model, f.window, f.source)}
	}
	return nil
}

// boundClause says how f's bound was estimated, " by text class", when the
// calibration's estimate differs from it; "" when it does not.
func (f contextFit) boundClause() string {
	if f.bound != f.estimate {
		return " by text class"
	}
	return ""
}

// underEstimate is added to the error for a reply that ran into the window
// when the guard left it room by an estimate under the server's count of
// the prompt, and says why when counts of earlier prompts lowered that
// estimate. "" when the estimate was at least the count.
func (f contextFit) underEstimate(promptTokens int64) string {
	if promptTokens <= f.estimate {
		return ""
	}
	s := fmt.Sprintf("; the guard left room for the reply by an estimate of %d prompt tokens (estimated %s), under the server's count", f.estimate, f.basis)
	if f.estimate < f.bound {
		s += fmt.Sprintf(": this prompt, %d tokens by text class, is denser than the earlier prompts whose counts lowered the estimate", f.bound)
	}
	return s
}

// windowStop is the error for a reply the server stopped at length short of
// the cap sent, with the prompt and reply counted at the window or one
// token under it, as llama.cpp stops a reply when context shift is off: the
// window cut it, not the cap. "" for any other reply.
func (f contextFit) windowStop() string {
	if !f.measured() || !f.filledWindow() {
		return ""
	}
	return fmt.Sprintf("reply cut off at the context window, short of the output cap (stop reason length, max tokens %d): the server counted %d prompt and %d reply tokens, which fill %s's %d-token context window (%s)%s",
		f.sent, f.counted, f.replied, f.model, f.window, f.source, f.underEstimate(f.counted))
}

// measured reports whether f knows a window, sent a cap and has the
// server's count of the prompt.
func (f contextFit) measured() bool {
	return f.window > 0 && f.sent > 0 && f.counted > 0
}

// filledWindow reports whether the server's counts show a reply short of
// the cap sent, with the prompt and reply at the window or one token under
// it.
func (f contextFit) filledWindow() bool {
	return f.replied < f.sent && f.counted+f.replied >= f.window-1
}

// windowClause names the window in an error, when one is known.
func (f contextFit) windowClause() string {
	if f.window <= 0 {
		return fmt.Sprintf("; %s's context window is unknown", f.model)
	}
	return fmt.Sprintf("; %s's context window is %d tokens (%s)", f.model, f.window, f.source)
}

// note is one line on the call for the run log: the estimate, the server's
// count, and the window.
func (f contextFit) note(promptTokens int64) string {
	s := fmt.Sprintf("prompt about %d tokens by estimate (%d bytes, estimated %s)", f.estimate, f.size.bytes, f.basis)
	if promptTokens > 0 {
		s += fmt.Sprintf("; the server counted %d", promptTokens)
	}
	if f.window <= 0 {
		return s + "; no context window known"
	}
	return s + f.windowNote()
}

// windowNote is note's account of a known window: the window, the bound the
// prompt must fit it at when that is not the estimate, and the output cap
// when the guard set or lowered it.
func (f contextFit) windowNote() string {
	s := fmt.Sprintf("; context window %d tokens (%s)", f.window, f.source)
	if f.bound != f.estimate {
		s += fmt.Sprintf(", which the prompt must fit at %d tokens by text class", f.bound)
	}
	return s + f.capChange()
}

// capChange is note's account of an output cap the guard set or lowered to
// fit the window; "" when it sent the cap asked for.
func (f contextFit) capChange() string {
	switch {
	case f.sent == f.asked:
		return ""
	case f.asked <= 0:
		return fmt.Sprintf("; output cap set to %d to fit it", f.sent)
	}
	return fmt.Sprintf("; output cap lowered from %d to %d to fit it", f.asked, f.sent)
}

// capNote is added to the error for a reply cut off at a cap the guard set
// or lowered.
func (f contextFit) capNote() string {
	switch {
	case f.sent == f.asked:
		return ""
	case f.asked <= 0:
		return fmt.Sprintf("; the guard set the cap so the reply fits the %d-token context window (%s)", f.window, f.source)
	}
	return fmt.Sprintf("; the cap was lowered from %d so the reply fits the %d-token context window (%s)", f.asked, f.window, f.source)
}

// Where a context window came from.
const (
	windowFromConfig   = "context_window in the models config"
	windowFromOllama   = "Ollama /api/ps"
	windowFromLMStudio = "LM Studio /api/v1/models"
	windowFromLlamaCpp = "llama.cpp /props"
	windowFromEnv      = "OLLAMA_CONTEXT_LENGTH in chb's environment"
)

// localServer is the kind of server the guard found behind an
// OpenAI-compatible endpoint, by the API it answers beside /v1.
type localServer string

const (
	serverNone     localServer = ""
	serverOllama   localServer = "Ollama"
	serverLMStudio localServer = "LM Studio"
	serverLlamaCpp localServer = "llama.cpp"
)

// modelContextWindow is the models config's context_window lookup, a
// variable so tests can declare one.
var modelContextWindow = models.ContextWindowFor

// ollamaContextEnv is OLLAMA_CONTEXT_LENGTH in chb's environment as a
// positive count of tokens; 0 when unset or not one.
func ollamaContextEnv() int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("OLLAMA_CONTEXT_LENGTH")), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// resolveContextWindow is the context window model is held to at the
// endpoint b serves, which is server: the smaller of the models config's
// context_window and the window the server reports it runs model with.
// On an Ollama server that reports none, OLLAMA_CONTEXT_LENGTH in chb's
// environment stands in for it. 0 when none is known.
func resolveContextWindow(ctx context.Context, b *OpenAIBackend, server localServer, model string) (window int64, source string) {
	window, source = smallerWindow(0, "", modelContextWindow(model), windowFromConfig)
	if b == nil {
		return window, source
	}
	w, src := b.serverContextWindow(ctx, server, model)
	return smallerWindow(window, source, w, src)
}

// smallerWindow is the smaller known window of window, which is 0 when none
// is known, and w, with where it came from.
func smallerWindow(window int64, source string, w int64, src string) (int64, string) {
	if narrowerWindow(w, window) {
		return w, src
	}
	return window, source
}

// narrowerWindow reports whether w is a known window (above 0) narrower
// than window, which is 0 when none is known.
func narrowerWindow(w, window int64) bool {
	return w > 0 && (window == 0 || w < window)
}

// serverContextWindow is the window server, the local server at b's
// endpoint, runs model with, and where that came from; 0 when it reports
// none.
func (b *OpenAIBackend) serverContextWindow(ctx context.Context, server localServer, model string) (int64, string) {
	switch server {
	case serverOllama:
		return b.ollamaWindow(ctx, model)
	case serverLMStudio:
		return b.lmStudioContextWindow(ctx, model), windowFromLMStudio
	case serverLlamaCpp:
		return b.llamaCppContextWindow(ctx), windowFromLlamaCpp
	}
	return 0, ""
}

// ollamaWindow is the window an Ollama server reports it runs model with,
// or, when it reports none, OLLAMA_CONTEXT_LENGTH in chb's environment.
func (b *OpenAIBackend) ollamaWindow(ctx context.Context, model string) (int64, string) {
	if w := b.ollamaContextWindow(ctx, model); w > 0 {
		return w, windowFromOllama
	}
	return ollamaContextEnv(), windowFromEnv
}

// serverRoot is b's base URL without its /v1, where a local server answers
// its own API; "" when the base URL does not end in /v1.
func (b *OpenAIBackend) serverRoot() string {
	if !strings.HasSuffix(b.BaseURL, "/v1") {
		return ""
	}
	return strings.TrimSuffix(b.BaseURL, "/v1")
}

// findServer asks the endpoint which local server it is, in turn: Ollama
// (GET /api/ps answers with a models list), LM Studio (GET /api/v1/models
// does) and llama.cpp (GET /props reports default_generation_settings.n_ctx).
// serverNone when it answers none of them, as a hosted API does.
func (b *OpenAIBackend) findServer(ctx context.Context) localServer {
	root := b.serverRoot()
	if root == "" {
		return serverNone
	}
	return b.serverAt(ctx, root)
}

// serverAt asks root, a server's root, which local server it is, in the
// order findServer says.
func (b *OpenAIBackend) serverAt(ctx context.Context, root string) localServer {
	switch {
	case b.isOllama(ctx, root):
		return serverOllama
	case b.isLMStudio(ctx, root):
		return serverLMStudio
	case b.isLlamaCpp(ctx, root):
		return serverLlamaCpp
	}
	return serverNone
}

// isOllama reports whether the server at root answers GET /api/ps with a
// models list, as Ollama does.
func (b *OpenAIBackend) isOllama(ctx context.Context, root string) bool {
	var ps struct {
		Models *[]json.RawMessage `json:"models"`
	}
	return b.getJSON(ctx, root+"/api/ps", &ps) && ps.Models != nil
}

// isLMStudio reports whether the server at root answers GET /api/v1/models
// with a models list, as LM Studio does.
func (b *OpenAIBackend) isLMStudio(ctx context.Context, root string) bool {
	var lms lmStudioModels
	return b.getJSON(ctx, root+"/api/v1/models", &lms) && lms.Models != nil
}

// isLlamaCpp reports whether the server at root reports
// default_generation_settings.n_ctx in GET /props, as llama.cpp does.
func (b *OpenAIBackend) isLlamaCpp(ctx context.Context, root string) bool {
	var props llamaCppProps
	return b.getJSON(ctx, root+"/props", &props) && props.Settings != nil && props.Settings.NCtx > 0
}

// getJSON decodes GET endpoint into out, bounded at 30 seconds; false when
// the server does not answer 200 with JSON.
func (b *OpenAIBackend) getJSON(ctx context.Context, endpoint string, out any) bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+b.APIKey)
	resp, err := b.Client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(out) == nil
}

// postJSON sends body to endpoint and discards the reply, bounded at 5
// minutes: a model load.
func (b *OpenAIBackend) postJSON(ctx context.Context, endpoint string, body any) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	js, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(js))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.APIKey)
	resp, err := b.Client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// loadModel asks the server to load a model, by posting body to endpoint,
// within an endpoint slot, so it does not run beside chb's own calls to a
// single-slot server.
func (b *OpenAIBackend) loadModel(ctx context.Context, endpoint string, body any) {
	release, err := acquireEndpointSlot(ctx, b.provider(), b.BaseURL)
	if err != nil {
		return
	}
	defer release()
	b.postJSON(ctx, endpoint, body)
}

// ollamaContextWindow is the context_length an Ollama server reports in
// GET /api/ps for model: the window it runs the loaded model with. A model
// not loaded yet is loaded first, by POST /api/generate naming only the
// model, which Ollama documents as loading it without generating; the
// node's own call would load it anyway. /api/show is not asked: it reports
// the length the model was trained for, not the window it runs with. 0 when
// it reports none.
func (b *OpenAIBackend) ollamaContextWindow(ctx context.Context, model string) int64 {
	root := b.serverRoot()
	if w, loaded := b.ollamaLoaded(ctx, root, model); loaded {
		return w
	}
	b.loadModel(ctx, root+"/api/generate", map[string]string{"model": model})
	w, _ := b.ollamaLoaded(ctx, root, model)
	return w
}

// ollamaLoaded reads GET /api/ps: loaded when it lists model, with the
// context_length it reports.
func (b *OpenAIBackend) ollamaLoaded(ctx context.Context, root, model string) (window int64, loaded bool) {
	var out struct {
		Models []ollamaPSModel `json:"models"`
	}
	if !b.getJSON(ctx, root+"/api/ps", &out) {
		return 0, false
	}
	for _, m := range out.Models {
		if m.is(model) {
			return m.ContextLength, true
		}
	}
	return 0, false
}

// ollamaPSModel is one loaded model in Ollama's GET /api/ps.
type ollamaPSModel struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	ContextLength int64  `json:"context_length"`
}

// is reports whether m is model, by its name or its model id
// (ollamaModelMatch).
func (m ollamaPSModel) is(model string) bool {
	return ollamaModelMatch(m.Name, model) || ollamaModelMatch(m.Model, model)
}

// ollamaModelMatch reports whether id, a model Ollama lists, is model: the
// same name, or, for a model named with no tag, that name with Ollama's
// default tag, latest.
func ollamaModelMatch(id, model string) bool {
	return id == model || !strings.Contains(model, ":") && id == model+":latest"
}

// lmStudioModels is LM Studio's GET /api/v1/models (LM Studio 0.4 and
// later): each model's key, variants and loaded instances, each instance
// with the context_length it was loaded with.
type lmStudioModels struct {
	Models *[]lmStudioModel `json:"models"`
}

// lmStudioModel is one model in LM Studio's GET /api/v1/models.
type lmStudioModel struct {
	Key             string             `json:"key"`
	Variants        []string           `json:"variants"`
	LoadedInstances []lmStudioInstance `json:"loaded_instances"`
}

// lmStudioInstance is one loaded instance of an LM Studio model.
type lmStudioInstance struct {
	ID     string `json:"id"`
	Config struct {
		ContextLength int64 `json:"context_length"`
	} `json:"config"`
}

// named reports whether model names m: its key, one of its variants, or the
// id of one of its loaded instances.
func (m lmStudioModel) named(model string) bool {
	return m.Key == model || slices.Contains(m.Variants, model) ||
		slices.ContainsFunc(m.LoadedInstances, func(inst lmStudioInstance) bool { return inst.ID == model })
}

// smallestWindow is the smallest context_length of m's loaded instances, 0
// when none is loaded with one.
func (m lmStudioModel) smallestWindow() int64 {
	var window int64
	for _, inst := range m.LoadedInstances {
		if w := inst.Config.ContextLength; narrowerWindow(w, window) {
			window = w
		}
	}
	return window
}

// lmStudioContextWindow is the context_length LM Studio reports for model's
// loaded instance, the smallest when it has several; model matches a
// model's key, one of its variants or an instance's id. A listed model with
// no instance loaded is loaded first, by POST /api/v1/models/load naming
// only the model; the node's own call would load it anyway. 0 when LM
// Studio does not list the model.
func (b *OpenAIBackend) lmStudioContextWindow(ctx context.Context, model string) int64 {
	root := b.serverRoot()
	w, listed := b.lmStudioLoaded(ctx, root, model)
	if w > 0 || !listed {
		return w
	}
	b.loadModel(ctx, root+"/api/v1/models/load", map[string]string{"model": model})
	w, _ = b.lmStudioLoaded(ctx, root, model)
	return w
}

// lmStudioLoaded reads GET /api/v1/models: listed when it lists model, with
// the smallest context_length of its loaded instances, 0 when none is
// loaded.
func (b *OpenAIBackend) lmStudioLoaded(ctx context.Context, root, model string) (window int64, listed bool) {
	var out lmStudioModels
	if !b.getJSON(ctx, root+"/api/v1/models", &out) || out.Models == nil {
		return 0, false
	}
	i := slices.IndexFunc(*out.Models, func(m lmStudioModel) bool { return m.named(model) })
	if i < 0 {
		return 0, false
	}
	return (*out.Models)[i].smallestWindow(), true
}

// llamaCppProps is llama.cpp's GET /props: the context each slot runs with.
type llamaCppProps struct {
	Settings *struct {
		NCtx int64 `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// llamaCppContextWindow is the n_ctx a llama.cpp server reports in GET
// /props: the context of each of its slots, which serves every model it
// runs. 0 when it reports none.
func (b *OpenAIBackend) llamaCppContextWindow(ctx context.Context) int64 {
	var out llamaCppProps
	if !b.getJSON(ctx, b.serverRoot()+"/props", &out) || out.Settings == nil {
		return 0
	}
	return out.Settings.NCtx
}

// isLocalNetworkURL reports whether endpoint's host is this machine or an
// address on a private network, where a server that drops the start of a
// prompt, such as Ollama, is likely to run.
func isLocalNetworkURL(endpoint string) bool {
	if isLoopbackURL(endpoint) {
		return true
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return privateNetworkIP(u.Hostname())
}

// privateNetworkIP reports whether host is an IP address on a private
// network or a link-local one.
func privateNetworkIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
