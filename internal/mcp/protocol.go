package mcp

// The stdio JSON-RPC 2.0 loop: a line read from stdin is parsed, checked
// against the revisions this server declares, dispatched on its own
// goroutine and answered on stdout. A tools/call is held to the schema the
// server advertises, then routed to its handler in the tools_*.go files.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

const serverName = "chb-mcp"

// serverTitle is the display name an MCP host may show for this server:
// the brand, where serverName is the command.
const serverTitle = "HIVE"

// supportedProtocolVersions, newest first. initialize negotiates per the
// legacy-revision rule: echo the client's version when supported, else
// answer with the newest supported and let the client decide.
//
//	2025-06-18 — no batching requirement; reserved _meta keys are ignored.
//	2024-11-05 — the original tools-only stdio contract.
//
// Not claimed: 2025-03-26 (MUST accept JSON-RPC batches; this server does
// not), 2025-11-25 (MUST validate per JSON Schema 2020-12; not verified
// here), 2026-07-28 (stateless: initialize removed, server/discover
// required — a protocol-shape change tracked separately).
var supportedProtocolVersions = []string{"2025-06-18", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

var reNonAlpha = regexp.MustCompile(`[^a-z0-9]+`)

func sanitizeProject(topic string, override any) string {
	if s, ok := override.(string); ok && strings.TrimSpace(s) != "" {
		return reNonAlpha.ReplaceAllString(strings.ToLower(s), "-")
	}
	p := reNonAlpha.ReplaceAllString(strings.ToLower(topic), "-")
	p = strings.Trim(p, "-")
	if len(p) > 40 {
		p = p[:40]
	}
	return p
}

// isScalarID reports whether a raw JSON id is one of the two shapes MCP
// allows for a request id: a string or an integer. raw is valid JSON. A
// number is an integer only as an optional minus and digits, so 1.5 and 1e3
// are no ids.
func isScalarID(raw json.RawMessage) bool {
	tok := bytes.TrimSpace(raw)
	if len(tok) > 0 && tok[0] == '"' {
		return true
	}
	return isDecimalDigits(bytes.TrimPrefix(tok, []byte("-")))
}

// isDecimalDigits reports whether b is one or more decimal digits.
func isDecimalDigits(b []byte) bool {
	return len(b) > 0 && !bytes.ContainsFunc(b, func(r rune) bool { return r < '0' || r > '9' })
}

// numericToolArgs refuses a tool argument that arrived as something other
// than a number. The filters below test for float64 and skip anything else,
// so unrefused, {"wave":"abc"} would silently mean "no wave filter" and
// return every row.
func numericToolArgs(args map[string]any, keys ...string) error {
	var bad []string
	for _, k := range keys {
		if v := args[k]; !numericOrAbsent(v) {
			bad = append(bad, fmt.Sprintf("%s=%v", k, v))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("argument(s) must be numeric: %s", strings.Join(bad, ", "))
	}
	return nil
}

// numericOrAbsent reports whether an argument's value is absent (nil) or a
// number.
func numericOrAbsent(v any) bool {
	_, isNumber := v.(float64)
	return v == nil || isNumber
}

func (s *mcpServer) handleLine(line []byte) {
	req, rerr := parseRPCRequest(line)
	if rerr != nil {
		s.writeErr(nil, rerr.Code, rerr.Message)
		return
	}
	if len(req.ID) == 0 {
		// Notifications are answered by nobody, and handled inline: they are
		// cheap, and a cancellation must never queue behind the very request
		// it is trying to cancel.
		s.handleNotification(req)
		return
	}
	if problem := requestHeaderProblem(req); problem != "" {
		s.writeErr(req.ID, -32600, problem)
		return
	}
	s.serveRequest(req)
}

// parseRPCRequest reads a line as a request, or as the error it is
// answered with, under a null id.
func parseRPCRequest(line []byte) (rpcRequest, *rpcError) {
	var req rpcRequest
	if rerr := decodeRPCObject(line, &req); rerr != nil {
		return req, rerr
	}
	if problem := requestIDProblem(req.ID); problem != "" {
		return req, &rpcError{Code: -32600, Message: problem}
	}
	return req, nil
}

// requestIDProblem is why a message's id cannot be answered on, "" when it
// can. JSON-RPC 2.0 as MCP profiles it: a request carries a string or
// integer id, a notification carries none, and null is not a valid id in any
// MCP revision. An id that is neither a string nor an integer is not a valid
// id, so there is nothing to echo a response back on, and `"id": [1,2]` or
// `"id": {"a":1}` gets no ordinary result.
func requestIDProblem(id json.RawMessage) string {
	switch {
	case string(id) == "null":
		return "invalid request: id must not be null"
	case len(id) > 0 && !isScalarID(id):
		return "invalid request: id must be a string or an integer, got " + string(id)
	}
	return ""
}

// decodeRPCObject decodes a line into req. Only a line that is not JSON is a
// parse error. JSON that is not a request object (a number, a string, null,
// a batch array, or members of the wrong type) is an invalid request.
func decodeRPCObject(line []byte, req *rpcRequest) *rpcError {
	var raw json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return &rpcError{Code: -32700, Message: "parse error: " + err.Error()}
	}
	if bytes.TrimSpace(raw)[0] != '{' {
		return &rpcError{Code: -32600, Message: "invalid request: a request must be a JSON object"}
	}
	if err := json.Unmarshal(line, req); err != nil {
		return &rpcError{Code: -32600, Message: "invalid request: " + err.Error()}
	}
	return nil
}

// handleNotification handles a message with no id, which is never
// answered, whatever method it names.
func (s *mcpServer) handleNotification(req rpcRequest) {
	switch {
	case req.Method == "notifications/cancelled":
		s.handleCancelled(req.Params)
	case !strings.HasPrefix(req.Method, "notifications/"):
		fmt.Fprintf(os.Stderr, "chb-mcp: ignoring id-less %q (notifications are not answered)\n", req.Method)
	}
}

// requestHeaderProblem is why a request is not one this server can answer,
// "" when it is one. A request that does not declare JSON-RPC 2.0, or names
// no method, is an Invalid Request, not a missing method.
func requestHeaderProblem(req rpcRequest) string {
	switch {
	case req.JSONRPC != "2.0":
		return "invalid request: jsonrpc must be \"2.0\", got " + strconv.Quote(req.JSONRPC)
	case strings.TrimSpace(req.Method) == "":
		return "invalid request: method is required"
	}
	return ""
}

// serveRequest dispatches a request on its own goroutine, so the read loop
// stays free to see the next line — which is what makes
// `notifications/cancelled` reachable at all, and stops a slow tool from
// delaying every other response including `ping`. Once the shutdown has
// begun a request is refused, and nothing is dispatched.
func (s *mcpServer) serveRequest(req rpcRequest) {
	if s.inflight == nil {
		s.inflight = newInflightRegistry()
	}
	ctx, done, ok := s.beginRequest(req.ID)
	if !ok {
		s.writeErr(req.ID, -32000, "server is shutting down")
		return
	}
	go s.serve(ctx, done, req)
}

// beginRequest registers a request as in flight, unless the shutdown has
// begun: the shutdown waits on the requests it has, then closes the store.
// The check and reqWG.Add hold drainMu, which the shutdown takes to begin,
// so no Add comes after its Wait.
func (s *mcpServer) beginRequest(id json.RawMessage) (context.Context, func(), bool) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	if s.draining {
		return nil, nil, false
	}
	ctx, done := s.inflight.begin(string(id))
	s.reqWG.Add(1)
	return ctx, done, true
}

// serve runs a request on its goroutine, then ends its time in flight.
func (s *mcpServer) serve(ctx context.Context, done func(), req rpcRequest) {
	defer done()
	defer s.reqWG.Done()
	defer s.recoverRequestPanic(req)
	s.dispatch(ctx, req)
}

// recoverRequestPanic, deferred, fails a request whose handler panicked. A
// panic in a tool handler must fail that request, not the server. Dispatch
// runs on its own goroutine, and an unrecovered panic in any goroutine
// terminates the whole process, and with it the host's connection and every
// request in flight.
func (s *mcpServer) recoverRequestPanic(req rpcRequest) {
	if rec := recover(); rec != nil {
		fmt.Fprintf(os.Stderr, "chb-mcp: panic serving %q (id %s): %v\n%s\n",
			req.Method, string(req.ID), rec, debug.Stack())
		s.writeErr(req.ID, -32603, fmt.Sprintf("internal error serving %s", req.Method))
	}
}

// rpcMethods serves each method this server answers.
var rpcMethods = map[string]func(*mcpServer, context.Context, rpcRequest){
	"initialize": (*mcpServer).handleInitialize,
	"ping":       (*mcpServer).handlePing,
	"tools/list": (*mcpServer).handleToolsList,
	"tools/call": (*mcpServer).handleToolCall,
}

// dispatch serves one request. Runs on its own goroutine; ctx is cancelled if
// the client sends `notifications/cancelled` for this request's id.
func (s *mcpServer) dispatch(ctx context.Context, req rpcRequest) {
	if handle, ok := rpcMethods[req.Method]; ok {
		handle(s, ctx, req)
		return
	}
	s.writeErr(req.ID, -32601, "method not found: "+req.Method)
}

// handleInitialize answers initialize with the revision negotiated, the
// server's capabilities and its name.
func (s *mcpServer) handleInitialize(_ context.Context, req rpcRequest) {
	s.writeResult(req.ID, map[string]any{
		"protocolVersion": negotiateProtocolVersion(req.Params),
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": serverName, "title": serverTitle, "version": version},
	})
}

// handlePing answers ping.
func (s *mcpServer) handlePing(_ context.Context, req rpcRequest) {
	s.writeResult(req.ID, map[string]any{})
}

// handleToolsList answers tools/list with every tool's spec.
func (s *mcpServer) handleToolsList(_ context.Context, req rpcRequest) {
	s.writeResult(req.ID, map[string]any{"tools": allToolSpecs()})
}

// negotiateProtocolVersion echoes the client's requested revision when
// the server supports it, and otherwise returns the newest revision the
// server does support, leaving the client to decide whether to proceed.
func negotiateProtocolVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	for _, v := range supportedProtocolVersions {
		if v == p.ProtocolVersion {
			return v
		}
	}
	return supportedProtocolVersions[0]
}

// ─── Tool specs ──────────────────────────────────────────────────

func allToolSpecs() []any {
	specs := []any{
		dbWriteSpec(),
		researchSpec(),
		statusSpec(),
		summarySpec(),
		findingsSpec(),
	}
	// The other 15: the families in tools_run.go, tools_review.go,
	// tools_repo_audit.go and tools_calibration.go, listed in tools.go.
	specs = append(specs, capabilityToolSpecs()...)
	return specs
}

// ─── Tool dispatch ────────────────────────────────────────────────

func (s *mcpServer) handleToolCall(ctx context.Context, req rpcRequest) {
	name, args, err := toolCallParams(req.Params)
	if err != nil {
		s.writeErr(req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if err := validateToolCall(name, args); err != nil {
		s.writeErr(req.ID, -32602, err.Error())
		return
	}
	handle, ok := toolHandlers[name]
	if !ok {
		// An unknown tool is a protocol error, not a tool result.
		s.writeErr(req.ID, -32602, fmt.Sprintf("unknown tool %q", name))
		return
	}
	handle(s, ctx, req, args)
}

// toolCallParams reads a tools/call's tool name and arguments; absent
// arguments are none.
func toolCallParams(params json.RawMessage) (string, map[string]any, error) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", nil, err
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}
	return p.Name, p.Arguments, nil
}

// validateToolCall holds the caller to the schema this server advertises for
// the tool, when it advertises one. Every handler reads its arguments with a
// type assertion and treats anything else as absent, so unchecked, a
// wrong-typed argument would mean "unset" and come back as an ordinary
// result.
func validateToolCall(name string, args map[string]any) error {
	spec := specByToolName(name)
	if spec == nil {
		return nil
	}
	return validateToolArgs(spec, args)
}

// ─── RPC helpers ─────────────────────────────────────────────────

func (s *mcpServer) writeToolResult(id json.RawMessage, text, errMsg string, isErr bool) {
	content := []any{}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	if errMsg != "" {
		content = append(content, map[string]any{"type": "text", "text": errMsg})
	}
	s.writeResult(id, map[string]any{"content": content, "isError": isErr})
}

func (s *mcpServer) writeResult(id json.RawMessage, result any) {
	s.write(id, rpcResponse{JSONRPC: "2.0", ID: idOrNull(id), Result: result})
}

func (s *mcpServer) writeErr(id json.RawMessage, code int, msg string) {
	s.write(id, rpcResponse{JSONRPC: "2.0", ID: idOrNull(id), Error: &rpcError{Code: code, Message: msg}})
}

// write emits one response. Requests are served concurrently, so the encoder
// is serialised here — json.Encoder is not safe for concurrent use and two
// interleaved writes would corrupt the stream. A response for a request the
// client cancelled is dropped: the spec says a cancelling server "SHOULD NOT
// send a response" for it.
func (s *mcpServer) write(id json.RawMessage, resp rpcResponse) {
	if s.inflight != nil && len(id) > 0 && s.inflight.isCancelled(string(id)) {
		fmt.Fprintf(os.Stderr, "chb-mcp: dropping response for cancelled request %s\n", string(id))
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_ = s.out.Encode(&resp)
}

// idOrNull makes the id member explicit: responses to a request echo its
// id; an error for a message whose id could not be read carries null.
func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
