package runner

// Tests for the tool-result bound (runner.md § Context window guard): a
// tool result is held to the room the next call leaves it, so no result
// pushes a call past the context window. Every model is an httptest fake;
// every page is an httptest page.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// toolCall renders a one-choice chat completion that calls tool with args.
func toolCall(tool, args string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{
			"content": "",
			"tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": tool, "arguments": args},
			}},
		}}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
	})
	return string(b)
}

// toolMessages are the tool messages of a chat request, in order.
func toolMessages(body map[string]any) []string {
	var out []string
	for _, m := range body["messages"].([]any) {
		m := m.(map[string]any)
		if m["role"] == "tool" {
			out = append(out, m["content"].(string))
		}
	}
	return out
}

// resultRoom restates the room the last tool message of body may take, in
// tokens by text class: half of what the request, with that message blank,
// leaves under the window less the reply floor, at the guard's scale, and
// under the window at its bound.
func resultRoom(t *testing.T, b *OpenAIBackend, body map[string]any, window, maxTokens int64) float64 {
	t.Helper()
	blank := map[string]any{}
	for k, v := range body {
		blank[k] = v
	}
	var msgs []map[string]any
	for _, m := range body["messages"].([]any) {
		msgs = append(msgs, m.(map[string]any))
	}
	last := msgs[len(msgs)-1]
	if last["role"] != "tool" {
		t.Fatalf("the last message is a %v message, want tool", last["role"])
	}
	copied := map[string]any{}
	for k, v := range last {
		copied[k] = v
	}
	copied["content"] = ""
	msgs[len(msgs)-1] = copied
	blank["messages"] = msgs
	floor := math.Min(float64(maxTokens), minReplyRoom)
	scale, bound, _ := b.calib.scale(b.BaseURL, body["model"].(string))
	fits := math.Min((float64(window)-floor)/scale, float64(window-1)/bound)
	return (fits - measurePrompt(blank).tokens) / 2
}

// cutResult splits a cut tool result into what was kept and its note, and
// fails when it carries none.
func cutResult(t *testing.T, got string) (kept, note string) {
	t.Helper()
	i := strings.LastIndex(got, "\n[cut: ")
	if i < 0 {
		t.Fatalf("the result carries no cut note:\n%s", truncate(got, 300))
	}
	return got[:i], got[i:]
}

// checkCut checks a cut result against full, the tool's whole result, and
// room: what was kept is a prefix of full, the whole fits the room, one
// more rune would not, and the note names the bytes kept and the total.
func checkCut(t *testing.T, got, full string, room float64) (kept string) {
	t.Helper()
	kept, note := cutResult(t, got)
	if !strings.HasPrefix(full, kept) {
		t.Fatalf("what was kept is not a prefix of the result")
	}
	if len(kept) == len(full) {
		t.Fatalf("nothing was cut")
	}
	if n := textTokens(got); n > room {
		t.Errorf("the cut result is %.0f tokens by text class, over the %.0f of room", n, room)
	}
	_, size := utf8.DecodeRuneInString(full[len(kept):])
	if n := textTokens(full[:len(kept)+size] + note); n <= room {
		t.Errorf("kept %d bytes, and one more rune would still fit: %.0f tokens of %.0f", len(kept), n, room)
	}
	if want := fmt.Sprintf("[cut: kept %d of %d bytes;", len(kept), len(full)); !strings.Contains(note, want) {
		t.Errorf("note %q lacks %q", note, want)
	}
	return kept
}

// page serves one HTML page of n paragraphs of prose.
func page(t *testing.T, n int) (*httptest.Server, string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html><html><head><title>A page</title><style>p{margin:0}</style></head><body>")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "<p>%s</p>\n", longPrompt(200))
	}
	sb.WriteString("</body></html>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(sb.String()))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return srv, "HTTP 200\n" + htmlText(sb.String(), u)
}

// loopBackend is a fake model that calls each of calls in turn, then
// answers "done", and the backend that runs it with a window.
func loopBackend(t *testing.T, calls ...string) (*fakeOllama, *OpenAIBackend) {
	t.Helper()
	f := &fakeOllama{modelID: "m:8b"}
	f.reply = func(body map[string]any) string {
		if n := len(toolMessages(body)); n < len(calls) {
			return calls[n]
		}
		return chatReply("done", "stop", 10, 5)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: srv.Client(), calib: &calibration{}}
}

func TestToolResult_APageOverTheRoomIsCut(t *testing.T) {
	const window, maxTokens = 8192, 8192
	pages, full := page(t, 300)
	f, b := loopBackend(t, toolCall("web_fetch", fmt.Sprintf(`{"url":%q}`, pages.URL)))
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	var log []string
	req := RunRequest{Model: "m:8b", Prompt: "read the page", Registry: reg, MaxTokens: maxTokens, ContextWindow: window, ContextWindowSource: "a test",
		Logf: func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }}
	res, err := b.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalText != "done" {
		t.Fatalf("FinalText %q, want done", res.FinalText)
	}
	chats := f.sent()
	if len(chats) != 2 {
		t.Fatalf("%d chat requests, want 2", len(chats))
	}
	got := toolMessages(chats[1])[0]
	kept := checkCut(t, got, full, resultRoom(t, b, chats[1], window, maxTokens))
	if want := fmt.Sprintf("call web_fetch again with the same url and offset %d.", len(kept)-len("HTTP 200\n")); !strings.Contains(got, want) {
		t.Errorf("the note lacks %q:\n%s", want, got[len(kept):])
	}
	if want := fmt.Sprintf("tool web_fetch: result cut to %d of %d bytes", len(kept), len(full)); len(log) != 1 || !strings.HasPrefix(log[0], want) {
		t.Errorf("log %q, want one line starting %q", log, want)
	}
	if inv := reg.Invocations(); len(inv) != 1 || inv[0].Output != full {
		t.Errorf("the invocation records %d bytes, want the tool's whole %d", len(inv[0].Output), len(full))
	}
}

func TestToolResult_ARaisedEstimateShrinksTheRoom(t *testing.T) {
	const window, maxTokens = 16384, 8192
	pages, full := page(t, 300)
	f, b := loopBackend(t, toolCall("web_fetch", fmt.Sprintf(`{"url":%q}`, pages.URL)))
	// The first call is counted at 1.2 times its estimate, which raises the
	// estimate of every later call on the model, and so shrinks the room.
	prompt := longPrompt(3000)
	plain := f.reply
	f.reply = func(body map[string]any) string {
		if len(toolMessages(body)) == 0 {
			est := measurePrompt(body).tokens
			return strings.Replace(plain(body), `"prompt_tokens":10`, fmt.Sprintf(`"prompt_tokens":%d`, int(est*1.2)), 1)
		}
		return plain(body)
	}
	req := RunRequest{Model: "m:8b", Prompt: prompt, Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder()), MaxTokens: maxTokens, ContextWindow: window, ContextWindowSource: "a test"}
	if _, err := b.Run(context.Background(), req); err != nil {
		t.Fatalf("run: %v", err)
	}
	chats := f.sent()
	if len(chats) != 2 {
		t.Fatalf("%d chat requests, want 2", len(chats))
	}
	if scale, _, _ := b.calib.scale(b.BaseURL, "m:8b"); scale <= 1 {
		t.Fatalf("scale %.2f after the count, want above 1", scale)
	}
	checkCut(t, toolMessages(chats[1])[0], full, resultRoom(t, b, chats[1], window, maxTokens))
}

func TestToolResult_TwoPagesInOneLoopBothFit(t *testing.T) {
	const window, maxTokens = 8192, 8192
	first, fullFirst := page(t, 300)
	second, fullSecond := page(t, 300)
	f, b := loopBackend(t,
		toolCall("web_fetch", fmt.Sprintf(`{"url":%q}`, first.URL)),
		toolCall("web_fetch", fmt.Sprintf(`{"url":%q}`, second.URL)))
	req := RunRequest{Model: "m:8b", Prompt: "read both pages", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder()), MaxTokens: maxTokens, ContextWindow: window, ContextWindowSource: "a test"}
	res, err := b.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalText != "done" || res.ToolUses != 2 {
		t.Fatalf("FinalText %q with %d tool uses, want done after 2", res.FinalText, res.ToolUses)
	}
	chats := f.sent()
	if len(chats) != 3 {
		t.Fatalf("%d chat requests, want 3", len(chats))
	}
	results := toolMessages(chats[2])
	keptFirst := checkCut(t, results[0], fullFirst, resultRoom(t, b, chats[1], window, maxTokens))
	keptSecond := checkCut(t, results[1], fullSecond, resultRoom(t, b, chats[2], window, maxTokens))
	if len(keptSecond) >= len(keptFirst) {
		t.Errorf("the second page kept %d bytes, the first %d; the second has less room", len(keptSecond), len(keptFirst))
	}
}

func TestToolResult_ASmallPageIsUntouched(t *testing.T) {
	const window, maxTokens = 8192, 8192
	pages, full := page(t, 2)
	f, b := loopBackend(t, toolCall("web_fetch", fmt.Sprintf(`{"url":%q}`, pages.URL)))
	req := RunRequest{Model: "m:8b", Prompt: "read the page", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder()), MaxTokens: maxTokens, ContextWindow: window, ContextWindowSource: "a test"}
	if _, err := b.Run(context.Background(), req); err != nil {
		t.Fatalf("run: %v", err)
	}
	chats := f.sent()
	if got := toolMessages(chats[1])[0]; got != full {
		t.Errorf("the result was changed:\n got %q\nwant %q", truncate(got, 200), truncate(full, 200))
	}
	if textTokens(full) > resultRoom(t, b, chats[1], window, maxTokens) {
		t.Fatalf("the page does not fit the room; the test needs a smaller page")
	}
}

// The shell tool's result is held to the room under either of its names,
// and the note says how to narrow the command.
func TestToolResult_ShellOutputOverTheRoomIsCut(t *testing.T) {
	const window, maxTokens = 8192, 8192
	for _, name := range []string{"shell", "bash"} {
		t.Run(name, func(t *testing.T) {
			f, b := loopBackend(t, toolCall(name, `{"command":"yes 'the quick brown fox jumps over the lazy dog' | head -c 60000"}`))
			reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
			req := RunRequest{Model: "m:8b", Prompt: "run it", Registry: reg, MaxTokens: maxTokens, ContextWindow: window, ContextWindowSource: "a test"}
			if _, err := b.Run(context.Background(), req); err != nil {
				t.Fatalf("run: %v", err)
			}
			chats := f.sent()
			if len(chats) != 2 {
				t.Fatalf("%d chat requests, want 2", len(chats))
			}
			inv := reg.Invocations()[0]
			if inv.Tool != "shell" {
				t.Errorf("the audit row names %q, want shell", inv.Tool)
			}
			full := inv.Output
			if len(full) != 60000 {
				t.Fatalf("the command wrote %d bytes, want 60000", len(full))
			}
			got := toolMessages(chats[1])[0]
			checkCut(t, got, full, resultRoom(t, b, chats[1], window, maxTokens))
			if !strings.Contains(got, "head, tail, grep") {
				t.Errorf("the note does not say how to narrow the output:\n%s", got[len(got)-200:])
			}
		})
	}
}

func TestToolResult_ReadFileOverTheRoomIsCutAndResumes(t *testing.T) {
	const window, maxTokens = 8192, 8192
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&sb, "the line of the file that says what it says, once more\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f, b := loopBackend(t, toolCall("read_file", `{"path":"long.txt"}`))
	reg := NewToolRegistry(dir, nil, NewFSRecorder())
	req := RunRequest{Model: "m:8b", Prompt: "read it", Registry: reg, MaxTokens: maxTokens, ContextWindow: window, ContextWindowSource: "a test"}
	if _, err := b.Run(context.Background(), req); err != nil {
		t.Fatalf("run: %v", err)
	}
	chats := f.sent()
	got := toolMessages(chats[1])[0]
	kept := checkCut(t, got, reg.Invocations()[0].Output, resultRoom(t, b, chats[1], window, maxTokens))
	next := 1 + strings.Count(kept, "\n")
	if want := fmt.Sprintf("call read_file again with offset %d.", next); !strings.Contains(got, want) {
		t.Fatalf("the note lacks %q:\n%s", want, got[len(kept):])
	}
	rest, _, err := reg.Invoke(context.Background(), "read_file", map[string]any{"path": "long.txt", "offset": float64(next)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rest, fmt.Sprintf("%d: the line", next)) {
		t.Errorf("read_file at offset %d starts %q", next, truncate(rest, 60))
	}
	if n := strings.Count(rest, "\n"); n != 2000-next+2 {
		t.Errorf("read_file at offset %d returned %d lines, want %d", next, n, 2000-next+2)
	}
}

// When one line is longer than the room, the note names the character to
// continue from, and the second read returns the rest of the line, so the
// two reads hold the line once.
func TestToolResult_ReadFileOneLongLineResumesByChar(t *testing.T) {
	const window, maxTokens = 8192, 8192
	dir := t.TempDir()
	line := strings.Repeat("é", 20000) // two bytes a rune, so a byte offset would not do
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(line+"\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, b := loopBackend(t, toolCall("read_file", `{"path":"long.txt"}`))
	reg := NewToolRegistry(dir, nil, NewFSRecorder())
	req := RunRequest{Model: "m:8b", Prompt: "read it", Registry: reg, MaxTokens: maxTokens, ContextWindow: window, ContextWindowSource: "a test"}
	if _, err := b.Run(context.Background(), req); err != nil {
		t.Fatalf("run: %v", err)
	}
	chats := f.sent()
	got := toolMessages(chats[1])[0]
	kept := checkCut(t, got, reg.Invocations()[0].Output, resultRoom(t, b, chats[1], window, maxTokens))
	if strings.Contains(kept, "\n") {
		t.Fatalf("a whole line fit; the test needs a longer line")
	}
	first := strings.TrimPrefix(kept, "1: ")
	next := utf8.RuneCountInString(first) + 1
	want := fmt.Sprintf("call read_file again with offset 1 and char %d.", next)
	if !strings.Contains(got, want) {
		t.Fatalf("the note lacks %q:\n%s", want, got[len(kept):])
	}
	rest, _, err := reg.Invoke(context.Background(), "read_file", map[string]any{"path": "long.txt", "offset": float64(1), "char": float64(next)})
	if err != nil {
		t.Fatal(err)
	}
	restLine, after, ok := strings.Cut(strings.TrimPrefix(rest, "1: "), "\n")
	if !ok || after != "2: second\n3: \n" {
		t.Fatalf("the second read does not go on to line 2:\n%s", truncate(rest, 80))
	}
	if first+restLine != line {
		t.Errorf("the two reads hold %d and %d characters; together they are not the %d-character line", utf8.RuneCountInString(first), utf8.RuneCountInString(restLine), utf8.RuneCountInString(line))
	}
	refusal, isError, err := reg.Invoke(context.Background(), "read_file", map[string]any{"path": "long.txt", "offset": float64(2), "char": float64(99)})
	if err != nil || !isError || !strings.Contains(refusal, "char 99 is past the end of line 2, which has 6 characters") {
		t.Errorf("a char past the line's end: %q, isError %v, err %v", refusal, isError, err)
	}
}

func TestToolResult_NoWindowCutAtTheCap(t *testing.T) {
	pages, full := page(t, 300)
	f, b := loopBackend(t, toolCall("web_fetch", fmt.Sprintf(`{"url":%q}`, pages.URL)))
	req := RunRequest{Model: "m:8b", Prompt: "read the page", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder()), MaxTokens: 8192}
	if _, err := b.Run(context.Background(), req); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := toolMessages(f.sent()[1])[0]
	if kept, note := cutResult(t, got); !strings.HasPrefix(full, kept) || len(got) > toolResultCap || !strings.Contains(note, fmt.Sprintf("past the %d-byte cap", toolResultCap)) {
		t.Errorf("with no window known, the %d-byte result was sent as %d bytes; want it cut to the %d-byte cap, with its note", len(full), len(got), toolResultCap)
	}
}

func TestWebFetch_HTMLBecomesText(t *testing.T) {
	const doc = `<!DOCTYPE html>
<html><head><title>SQLite File Format</title>
<style>body{color:red}</style>
<script>var x = "<p>not text</p>";</script>
</head>
<body>
<!-- a comment -->
<h1>Database   File Format</h1>
<p>This document describes &amp; defines the <a href="/fileformat2.html">on-disk format</a>
of an SQLite database.</p>
<ul><li>First item</li><li>Second <b>bold</b> item</li></ul>
<table><tr><th>Name</th><th>Size</th></tr><tr><td>header</td><td>100</td></tr></table>
<pre>
  code  line
</pre>
<p>Tail &lt;3<br>next line</p>
<a href="#top">top</a> <a href="javascript:void(0)">js</a> <a href="https://example.org/x?a=1&amp;b=2">abs</a>
</body></html>`
	const want = "SQLite File Format\n\n# Database File Format\n\nThis document describes & defines the [on-disk format](https://www.sqlite.org/fileformat2.html) of an SQLite database.\n\n- First item\n- Second bold item\n\nName | Size\nheader | 100\n\n  code  line\n\nTail <3\nnext line\n\ntop js [abs](https://example.org/x?a=1&b=2)"
	base, _ := url.Parse("https://www.sqlite.org/fileformat.html")
	if got := htmlText(doc, base); got != want {
		t.Errorf("htmlText:\n got %q\nwant %q", got, want)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><h2>Title</h2><p>Body &amp; soul.</p><script>bad()</script></body></html>`))
		case "/sniffed":
			w.Header()["Content-Type"] = nil // no header, and none sniffed by the server
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><p>Sniffed as HTML.</p></body></html>`))
		case "/data.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"a":"<b>kept</b>"}`))
		case "/plain.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello <b>world</b>"))
		}
	}))
	defer srv.Close()
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	fetch := func(path string, offset any) string {
		t.Helper()
		in := map[string]any{"url": srv.URL + path}
		if offset != nil {
			in["offset"] = offset
		}
		out, isErr, err := reg.Invoke(context.Background(), "web_fetch", in)
		if err != nil || isErr {
			t.Fatalf("web_fetch %s: %v %v", path, out, err)
		}
		return out
	}
	for _, tc := range []struct {
		path   string
		offset any
		want   string
	}{
		{"/page", nil, "HTTP 200\n## Title\n\nBody & soul."},
		{"/sniffed", nil, "HTTP 200\nSniffed as HTML."},
		{"/data.json", nil, "HTTP 200\n{\"a\":\"<b>kept</b>\"}"},
		{"/plain.txt", nil, "HTTP 200\nhello <b>world</b>"},
		{"/plain.txt", float64(6), "HTTP 200\n<b>world</b>"},
		{"/plain.txt", float64(18), "HTTP 200\n(the text is 18 bytes, and offset 18 is past its end)"},
	} {
		if got := fetch(tc.path, tc.offset); got != tc.want {
			t.Errorf("web_fetch %s offset %v:\n got %q\nwant %q", tc.path, tc.offset, got, tc.want)
		}
	}
}

func TestFitToolResult_CutsAtARuneBoundary(t *testing.T) {
	// Every character is three bytes, so a cut at a byte count that is not
	// a multiple of three would split one.
	content := strings.Repeat("日本語の文章 ", 400)
	body := map[string]any{"model": "m", "messages": []map[string]any{{"role": "user", "content": "x"}, {"role": "tool", "content": ""}}}
	req := RunRequest{MaxTokens: 8192, ContextWindow: 4096}
	got, kept := fitToolResult(nil, "e", body, req, content, nil)
	if kept >= len(content) || !utf8.ValidString(got) {
		t.Fatalf("kept %d of %d bytes, valid %v", kept, len(content), utf8.ValidString(got))
	}
	if !strings.HasPrefix(content, got[:kept]) || !strings.HasPrefix(got[kept:], "\n[cut: ") {
		t.Errorf("kept %d bytes is not a prefix with a note: %q", kept, got[max(kept-20, 0):min(kept+30, len(got))])
	}
}
