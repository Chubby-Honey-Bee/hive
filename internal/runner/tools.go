// Package runner closes the "fully unattended" gap in HIVE: it takes
// workflow nodes dispatched by internal/workflow and executes them against
// the Anthropic API, exposing a minimal set of tools (file I/O, HTTP fetch,
// restricted shell, CDE-encoded DB writes) so agents can do research, edit
// files, and record findings without human intervention.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/anthropics/anthropic-sdk-go"
)

// ToolHandler executes a single tool invocation from the agent and returns
// a string result (plain text that gets fed back into Claude as a tool_result).
type ToolHandler func(ctx context.Context, input map[string]any) (string, error)

// ToolRegistry holds the tool schemas + handlers the agent may call.
type ToolRegistry struct {
	Schemas  []anthropic.ToolUnionParam
	Handlers map[string]ToolHandler
	// Env is the environment the bash tool runs commands with (agentEnv);
	// nil inherits the process's. Either way the bash tool drops the
	// provider credentials (withoutProviderCredentials).
	Env []string
	// Tracks every invocation for post-run audit. Append through record():
	// parallel_fan children share one registry, and an unguarded append
	// from several goroutines is a data race that crashes the whole run.
	Log []ToolInvocation
	mu  sync.Mutex
	// restricted is set by Restrict: a node's `tools:` allowlist narrowed
	// this registry, which the CLI backends read to narrow their own tools.
	restricted bool
	// rest says, per tool, how to get the rest of a result the tool loop
	// cut to the context window (fitToolResult), given the call's input and
	// what was kept. A tool with no entry has no way.
	rest map[string]func(input map[string]any, kept string) string
}

// setRest registers how to get the rest of a cut result of name's calls.
func (r *ToolRegistry) setRest(name string, f func(input map[string]any, kept string) string) {
	if r.rest == nil {
		r.rest = map[string]func(map[string]any, string) string{}
	}
	r.rest[name] = f
}

// restOf is how to get the rest of a cut result of name's call with input,
// given what was kept; nil when the tool has no way. name may be an alias.
func (r *ToolRegistry) restOf(name string, input map[string]any) func(kept string) string {
	f, ok := r.rest[workflow.CanonicalTool(name)]
	if !ok {
		return nil
	}
	return func(kept string) string { return f(input, kept) }
}

// Names lists the registry's tools in the order they were registered.
func (r *ToolRegistry) Names() []string {
	out := make([]string, 0, len(r.Schemas))
	for _, t := range r.Schemas {
		if t.OfTool != nil {
			out = append(out, t.OfTool.Name)
		}
	}
	return out
}

// Restrict keeps only the named tools, schema and handler, in registration
// order: a node's `tools:` allowlist, in which a tool's alias names the
// tool. An empty list keeps none and leaves Schemas nil, so no backend
// offers a tool.
func (r *ToolRegistry) Restrict(keep []string) {
	allowed := canonicalToolSet(keep)
	r.Schemas = keptSchemas(r.Schemas, allowed)
	maps.DeleteFunc(r.Handlers, func(name string, _ ToolHandler) bool { return !allowed[name] })
	r.restricted = true
}

// canonicalToolSet is the tools names name, each alias as the tool it
// names.
func canonicalToolSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[workflow.CanonicalTool(n)] = true
	}
	return set
}

// keptSchemas are the schemas, in order, of the tools allowed holds; nil
// when there are none.
func keptSchemas(schemas []anthropic.ToolUnionParam, allowed map[string]bool) []anthropic.ToolUnionParam {
	var kept []anthropic.ToolUnionParam
	for _, t := range schemas {
		if t.OfTool != nil && allowed[t.OfTool.Name] {
			kept = append(kept, t)
		}
	}
	return kept
}

// Restricted reports whether Restrict narrowed the registry.
func (r *ToolRegistry) Restricted() bool { return r.restricted }

// record appends one invocation to the audit log under the lock.
func (r *ToolRegistry) record(inv ToolInvocation) {
	r.mu.Lock()
	r.Log = append(r.Log, inv)
	r.mu.Unlock()
}

// refuse records a tool call that was not run, with the reason as its
// output, so the audit trail shows the call the model made.
func (r *ToolRegistry) refuse(name, input, reason string) {
	r.record(ToolInvocation{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Tool:      name,
		Input:     input,
		Output:    reason,
		IsError:   true,
	})
}

// Invocations returns a copy of the audit log, safe to read while children
// are still running.
func (r *ToolRegistry) Invocations() []ToolInvocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ToolInvocation(nil), r.Log...)
}

// ToolInvocation is one tool call, captured for the tool_invocations audit table.
type ToolInvocation struct {
	Timestamp string
	Tool      string
	Input     string
	Output    string
	IsError   bool
	DurationS float64
}

// NewToolRegistry wires the default tool set. projectDir is the working dir
// (everything the agent does is sandboxed to that subtree).
func NewToolRegistry(projectDir string, store *db.Store, fs *FileSystemRecorder) *ToolRegistry {
	r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r.registerRead(projectDir, fs)
	r.registerWrite(projectDir, fs)
	r.registerEdit(projectDir, fs)
	r.registerGlob(projectDir)
	r.registerGrep(projectDir)
	r.registerShell(projectDir, fs, hostShell())
	r.registerWebFetch()
	r.registerDBWrite(store)
	return r
}

// FileSystemRecorder records which files the agent touched. It feeds the
// audit log and nothing else — staging is `git add -A` over the whole tree,
// which never consults this. It could not drive staging anyway: it is empty
// under the CLI backend, whose edits happen in a subprocess, and any bash
// call marks the project root wholesale.
type FileSystemRecorder struct {
	mu      sync.Mutex
	Touched map[string]bool
}

// NewFSRecorder returns an empty FileSystemRecorder.
func NewFSRecorder() *FileSystemRecorder { return &FileSystemRecorder{Touched: map[string]bool{}} }

func (f *FileSystemRecorder) mark(path string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.Touched[path] = true
	f.mu.Unlock()
}

// TouchedPaths lists the paths the agent touched, in no order; nil on a nil
// recorder.
func (f *FileSystemRecorder) TouchedPaths() []string {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.Touched))
	for p := range f.Touched {
		out = append(out, p)
	}
	return out
}

// Reset clears the recorder between nodes, so the audit log attributes each
// file to the node that touched it. Commits are not scoped by this — see the
// note on FileSystemRecorder.
func (f *FileSystemRecorder) Reset() {
	f.mu.Lock()
	f.Touched = map[string]bool{}
	f.mu.Unlock()
}

// ── Provider-neutral tool schema ──
//
// ToolRegistry stores schemas as anthropic.ToolUnionParam, which
// is fine for the Anthropic backend but unusable for Gemini and OpenAI. Rather
// than refactor every register* function (high churn, no behavior change for
// existing callers), we expose a small adapter that walks the existing
// anthropic-typed schemas and emits a generic shape. New backends translate
// from this shape to their provider-specific tool schema.

// NeutralTool is a backend-agnostic tool descriptor. The JSONSchema field is
// the same JSON-Schema-flavored Properties+Required object that Anthropic,
// OpenAI, and Gemini all accept (with slight formatting differences which
// each backend handles).
type NeutralTool struct {
	Name        string
	Description string
	JSONSchema  map[string]any
}

// NeutralSchemas returns the registry's tools in a provider-neutral shape.
// Used by backend_openai.go and backend_gemini.go to translate to their
// respective tool-call schemas.
func (r *ToolRegistry) NeutralSchemas() []NeutralTool {
	out := make([]NeutralTool, 0, len(r.Schemas))
	for _, t := range r.Schemas {
		if t.OfTool == nil {
			continue
		}
		props := t.OfTool.InputSchema.Properties
		propMap, _ := props.(map[string]any)
		desc := ""
		if v := t.OfTool.Description; v.Valid() {
			desc = v.Value
		}
		out = append(out, NeutralTool{
			Name:        t.OfTool.Name,
			Description: desc,
			JSONSchema: map[string]any{
				"type":       "object",
				"properties": propMap,
				"required":   t.OfTool.InputSchema.Required,
			},
		})
	}
	return out
}

// Invoke runs a tool by name with the parsed input map. Returns the tool's
// output string, an isError flag (mirroring Anthropic's tool_result.is_error),
// and an internal error (for provider plumbing failures, distinct from a
// tool that ran-but-returned-an-error). A tool the registry does not hold,
// one a node's allowlist removed included, is refused as an error result
// naming the tools it does hold, so the backend hands the refusal back to
// the model instead of failing the call. name may be an alias: the call
// runs the tool it names, and the audit row names that tool; a refusal
// names the tool as called.
func (r *ToolRegistry) Invoke(ctx context.Context, name string, input map[string]any) (output string, isError bool, err error) {
	inputJSON, _ := json.Marshal(input)
	tool := workflow.CanonicalTool(name)
	h, ok := r.Handlers[tool]
	if !ok {
		refusal := r.unknownToolRefusal(name)
		r.record(ToolInvocation{
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Tool:      tool,
			Input:     string(inputJSON),
			Output:    refusal,
			IsError:   true,
		})
		return refusal, true, nil
	}
	start := time.Now()
	out, runErr := h(ctx, input)
	inv := ToolInvocation{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Tool:      tool,
		Input:     string(inputJSON),
		DurationS: time.Since(start).Seconds(),
	}
	if runErr != nil {
		inv.Output = runErr.Error()
		inv.IsError = true
		r.record(inv)
		return runErr.Error(), true, nil
	}
	inv.Output = out
	r.record(inv)
	return out, false, nil
}

// unknownToolRefusal refuses a call to name, a tool the registry does not
// hold, naming the tools it does hold, sorted.
func (r *ToolRegistry) unknownToolRefusal(name string) string {
	held := slices.Sorted(maps.Keys(r.Handlers))
	if len(held) == 0 {
		return fmt.Sprintf("unknown tool: %s; no tools are available here", name)
	}
	return fmt.Sprintf("unknown tool: %s; the tools available here are %s", name, strings.Join(held, ", "))
}

// resolvePath keeps every file operation inside projectDir.
func resolvePath(projectDir, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectDir, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rootAbs, _ := filepath.Abs(projectDir)
	if !withinDir(abs, rootAbs) {
		return "", fmt.Errorf("path %s escapes project dir %s", abs, rootAbs)
	}
	return abs, nil
}

// withinDir reports whether abs, an absolute path, is dir or inside it.
func withinDir(abs, dir string) bool {
	return abs == dir || strings.HasPrefix(abs, dir+string(filepath.Separator))
}

// ─── Tool: Read ───────────────────────────────────────────────────────
func (r *ToolRegistry) registerRead(projectDir string, _ *FileSystemRecorder) {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "read_file",
			Description: anthropic.String("Read a text file from the project. Returns file content with line numbers (cat -n style), from line offset (1, the first, when not given). A result too long for the context window is cut and says which offset, and when one line is longer than the room which char of it, gets the rest."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":   map[string]any{"type": "string", "description": "relative or absolute path inside the project"},
					"offset": map[string]any{"type": "integer", "description": "the first line to return, counted from 1"},
					"char":   map[string]any{"type": "integer", "description": "the first character of that first line to return, counted from 1; a cut result's note gives it when one line is longer than the room"},
				},
				Required: []string{"path"},
			},
		},
	})
	r.Handlers["read_file"] = func(_ context.Context, in map[string]any) (string, error) {
		return readFileTool(projectDir, in)
	}
	r.setRest("read_file", readFileRest)
}

// readFileTool is a read_file call with input in: the file's lines from the
// call's offset, each prefixed with "N: " for agent convenience, the first
// from the call's char.
func readFileTool(projectDir string, in map[string]any) (string, error) {
	path, _ := in["path"].(string)
	abs, err := resolvePath(projectDir, path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return numberedLines(path, strings.Split(string(b), "\n"), startLine(in), startChar(in))
}

// numberedLines is lines, those of the file path, from line first, counted
// from 1, each prefixed with "N: ", the first from its char'th character.
// A first or a char past the end is an error.
func numberedLines(path string, lines []string, first, char int) (string, error) {
	if first > len(lines) {
		return "", fmt.Errorf("read %s: offset %d is past its last line, %d", path, first, len(lines))
	}
	line, err := lineFromChar(path, lines[first-1], first, char)
	if err != nil {
		return "", err
	}
	lines[first-1] = line
	var sb strings.Builder
	for i, line := range lines[first-1:] {
		fmt.Fprintf(&sb, "%d: %s\n", first+i, line)
	}
	return sb.String(), nil
}

// lineFromChar is line, line n of the file path, from its char'th
// character, counted from 1; the whole line when char is 1 or less.
func lineFromChar(path, line string, n, char int) (string, error) {
	if char <= 1 {
		return line, nil
	}
	runes := []rune(line)
	if char > len(runes) {
		return "", fmt.Errorf("read %s: char %d is past the end of line %d, which has %d characters", path, char, n, len(runes))
	}
	return string(runes[char-1:]), nil
}

// readFileRest is how to get the rest of a read_file result cut to kept,
// for a call with input in. Each line kept ends with a newline, so the
// newlines kept count the whole lines; a line cut in two is read again.
// When no whole line fit, the first line is longer than the room: what was
// kept is its "N: " prefix and the first characters of the line, and the
// rest of the line is read from the next character, so an over-long line is
// never read again from its start.
func readFileRest(in map[string]any, kept string) string {
	first := startLine(in)
	if n := strings.Count(kept, "\n"); n > 0 {
		return fmt.Sprintf("For the rest, call read_file again with offset %d.", first+n)
	}
	got := utf8.RuneCountInString(kept) - len(fmt.Sprintf("%d: ", first))
	if got <= 0 {
		return ""
	}
	return fmt.Sprintf("Line %d is longer than the room. For the rest of it, call read_file again with offset %d and char %d.", first, first, startChar(in)+got)
}

// startLine is a read_file call's offset, the first line it returns: 1 when
// not given or under 1.
func startLine(in map[string]any) int {
	if n := reqInt(in, "offset"); n > 1 {
		return n
	}
	return 1
}

// startChar is a read_file call's char, the first character of its first
// line it returns: 1 when not given or under 1.
func startChar(in map[string]any) int {
	if n := reqInt(in, "char"); n > 1 {
		return n
	}
	return 1
}

// ─── Tool: Write ──────────────────────────────────────────────────────
func (r *ToolRegistry) registerWrite(projectDir string, fs *FileSystemRecorder) {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "write_file",
			Description: anthropic.String("Write (or overwrite) a file with the given content."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				Required: []string{"path", "content"},
			},
		},
	})
	r.Handlers["write_file"] = func(_ context.Context, in map[string]any) (string, error) {
		path, _ := in["path"].(string)
		content, _ := in["content"].(string)
		abs, err := resolvePath(projectDir, path)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			return "", err
		}
		fs.mark(abs)
		return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
	}
}

// ─── Tool: Edit ───────────────────────────────────────────────────────
func (r *ToolRegistry) registerEdit(projectDir string, fs *FileSystemRecorder) {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "edit_file",
			Description: anthropic.String("Replace a single occurrence of old_string with new_string in a file. Fails if old_string is not unique."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":       map[string]any{"type": "string"},
					"old_string": map[string]any{"type": "string"},
					"new_string": map[string]any{"type": "string"},
				},
				Required: []string{"path", "old_string", "new_string"},
			},
		},
	})
	r.Handlers["edit_file"] = func(_ context.Context, in map[string]any) (string, error) {
		return editFileTool(projectDir, fs, in)
	}
}

// editFileTool is an edit_file call with input in: old_string, which must
// occur once, replaced with new_string in the file.
func editFileTool(projectDir string, fs *FileSystemRecorder, in map[string]any) (string, error) {
	path, _ := in["path"].(string)
	oldS, _ := in["old_string"].(string)
	newS, _ := in["new_string"].(string)
	abs, original, err := readProjectFile(projectDir, path)
	if err != nil {
		return "", err
	}
	if err := checkUnique(original, oldS, path); err != nil {
		return "", err
	}
	updated := strings.Replace(original, oldS, newS, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return "", err
	}
	fs.mark(abs)
	return fmt.Sprintf("edited %s (%d → %d bytes)", path, len(original), len(updated)), nil
}

// readProjectFile reads path, kept inside projectDir (resolvePath): its
// absolute path and its content.
func readProjectFile(projectDir, path string) (string, string, error) {
	abs, err := resolvePath(projectDir, path)
	if err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", "", err
	}
	return abs, string(b), nil
}

// checkUnique refuses an edit of content, the file path's, whose oldS does
// not occur in it exactly once.
func checkUnique(content, oldS, path string) error {
	switch count := strings.Count(content, oldS); {
	case count == 0:
		return fmt.Errorf("old_string not found in %s", path)
	case count > 1:
		return fmt.Errorf("old_string occurs %d times in %s — not unique", count, path)
	}
	return nil
}

// ─── Tool: Glob ───────────────────────────────────────────────────────
func (r *ToolRegistry) registerGlob(projectDir string) {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "glob",
			Description: anthropic.String("Glob files matching a pattern (e.g. 'src/**/*.md'). Returns newline-separated matches relative to project root."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{"pattern": map[string]any{"type": "string"}},
				Required:   []string{"pattern"},
			},
		},
	})
	r.Handlers["glob"] = func(_ context.Context, in map[string]any) (string, error) {
		pattern, _ := in["pattern"].(string)
		return globTool(projectDir, pattern)
	}
}

// globTool is a glob call for pattern: the files under projectDir it
// matches (globMatches), relative to projectDir. It walks the tree, since
// filepath.Glob doesn't support **.
func globTool(projectDir, pattern string) (string, error) {
	var matches []string
	if err := filepath.Walk(projectDir, globWalk(projectDir, pattern, &matches)); err != nil {
		return "", err
	}
	return matchesText(matches), nil
}

// globWalk is the walk of root that adds to matches each file pattern
// matches, by its path relative to root.
func globWalk(root, pattern string, matches *[]string) filepath.WalkFunc {
	return func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if globMatches(pattern, rel, p) {
			*matches = append(*matches, rel)
		}
		return nil
	}
}

// globMatches reports whether pattern matches a file by rel, its path
// relative to the root, or by the base name of p, its path, for patterns
// like "*.md".
func globMatches(pattern, rel, p string) bool {
	if ok, _ := filepath.Match(pattern, rel); ok {
		return true
	}
	ok, _ := filepath.Match(pattern, filepath.Base(p))
	return ok
}

// matchesText is a search's result: its matches, a line each, or "(no
// matches)".
func matchesText(matches []string) string {
	if len(matches) == 0 {
		return "(no matches)"
	}
	return strings.Join(matches, "\n")
}

// ─── Tool: Grep ───────────────────────────────────────────────────────
func (r *ToolRegistry) registerGrep(projectDir string) {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "grep",
			Description: anthropic.String("Search files for a substring. Returns 'path:line:text' hits, up to 200."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"pattern": map[string]any{"type": "string"},
					"path":    map[string]any{"type": "string", "description": "optional subdirectory"},
				},
				Required: []string{"pattern"},
			},
		},
	})
	r.Handlers["grep"] = func(_ context.Context, in map[string]any) (string, error) {
		return grepTool(projectDir, in)
	}
}

// grepTool is a grep call with input in: the lines that hold its pattern,
// in the files under the project or under its path, a subdirectory, as
// 'path:line:text' hits, up to 200.
func grepTool(projectDir string, in map[string]any) (string, error) {
	pattern, _ := in["pattern"].(string)
	sub, _ := in["path"].(string)
	root, err := grepRoot(projectDir, sub)
	if err != nil {
		return "", err
	}
	g := &grepSearch{projectDir: projectDir, pattern: pattern}
	_ = filepath.Walk(root, g.visit)
	return matchesText(g.hits), nil
}

// grepRoot is the directory a grep searches: projectDir, or sub inside it
// when sub is set.
func grepRoot(projectDir, sub string) (string, error) {
	if sub == "" {
		return projectDir, nil
	}
	return resolvePath(projectDir, sub)
}

// grepSearch is one grep call's walk: the hits for pattern so far, each
// file named relative to projectDir.
type grepSearch struct {
	projectDir, pattern string
	hits                []string
}

// visit searches the file at p; directories and the files readSearchable
// leaves out are skipped.
func (g *grepSearch) visit(p string, info os.FileInfo, err error) error {
	if err != nil || info.IsDir() {
		return nil
	}
	if content, ok := readSearchable(p); ok {
		return g.search(p, content)
	}
	return nil
}

// readSearchable reads the file at p for a grep; false when it cannot be
// read, or is over 5 MiB, so binaries and huge files are skipped.
func readSearchable(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil || len(b) > 5*1024*1024 {
		return "", false
	}
	return string(b), true
}

// search adds to the hits each line of content, the file at p's, that holds
// the pattern, and stops the walk (filepath.SkipAll) at 200.
func (g *grepSearch) search(p, content string) error {
	for i, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, g.pattern) {
			continue
		}
		rel, _ := filepath.Rel(g.projectDir, p)
		g.hits = append(g.hits, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
		if len(g.hits) >= 200 {
			return filepath.SkipAll
		}
	}
	return nil
}

// ─── Tool: Shell ──────────────────────────────────────────────────────
// denyList is a typo-guard, not a security boundary. It is a substring match
// over a string that the shell then interprets, so "rm  -rf /" or "rm -fr /"
// walks straight past it. Treat an agent with this tool as having the
// privileges of the process that spawned it, and run it where that is
// acceptable.
var denyList = []string{
	"rm -rf /", "rm -rf /*", "rm -rf ~", "rm -rf ~/",
	"git push --force", "git push -f", "--no-verify",
	"mkfs", "dd if=", ":(){ :|:& };:",
	"shutdown", "reboot", "halt",
}

// registerShell registers `shell`, which runs a command through sh, the
// shell resolved for this machine (resolveShell). `bash` is another name for
// it (workflow.ToolAliases): a call under either name runs sh.
func (r *ToolRegistry) registerShell(projectDir string, fs *FileSystemRecorder, sh shellRunner) {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "shell",
			Description: anthropic.String(fmt.Sprintf("Run a shell command through %s (`%s`). Starts in the project directory and times out at 300s. A short deny-list catches a few obvious mistakes (rm -rf /, force-push); it is not a sandbox.", sh.name, sh)),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"command": map[string]any{"type": "string"},
				},
				Required: []string{"command"},
			},
		},
	})
	r.Handlers["shell"] = shellTool{r: r, projectDir: projectDir, fs: fs, sh: sh}.run
	r.setRest("shell", func(map[string]any, string) string {
		return "For the rest, run a command whose output is narrower: head, tail, grep, or a smaller range."
	})
}

// shellTool runs the shell tool's calls for registry r, through sh, in
// projectDir.
type shellTool struct {
	r          *ToolRegistry
	projectDir string
	fs         *FileSystemRecorder
	sh         shellRunner
}

// run is a shell call with input in: the command's output (shellRunner's
// result), within 300 s; a command the deny-list catches is refused.
func (t shellTool) run(ctx context.Context, in map[string]any) (string, error) {
	cmdStr, _ := in["command"].(string)
	if bad, denied := deniedCommand(cmdStr); denied {
		return "", fmt.Errorf("command blocked by deny-list: contains %q", bad)
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	out, err := t.command(ctx, cmdStr).CombinedOutput()
	// After any shell command, assume files may have changed — conservative
	// approach: mark the whole project as "needs git-add" via a sentinel.
	// The git helper diffs working tree vs HEAD to find real changes.
	t.fs.mark(t.projectDir)
	return t.sh.result(string(out), err), nil
}

// command is cmdStr run through the shell in the project directory, with
// the registry's environment, or the process's when it has none, less the
// provider credentials.
func (t shellTool) command(ctx context.Context, cmdStr string) *exec.Cmd {
	cmd := t.sh.command(ctx, cmdStr)
	cmd.Dir = t.projectDir
	env := t.r.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = withoutProviderCredentials(env)
	return cmd
}

// deniedCommand is the first entry of denyList that cmd holds; false when
// it holds none.
func deniedCommand(cmd string) (string, bool) {
	for _, bad := range denyList {
		if strings.Contains(cmd, bad) {
			return bad, true
		}
	}
	return "", false
}

// ─── Tool: WebFetch ───────────────────────────────────────────────────
func (r *ToolRegistry) registerWebFetch() {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "web_fetch",
			Description: anthropic.String("Fetch a URL via HTTP GET and return the response as text. An HTML page is reduced to its readable text: headings, paragraphs, lists and links, with scripts, styles and markup out; any other body is returned as it is. Reads up to 200KB of the body. A result too long for the context window is cut and says which offset gets the rest. Follows redirects. 15s timeout."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"url":    map[string]any{"type": "string"},
					"offset": map[string]any{"type": "integer", "description": "the byte of the text to start at; 0, the first, when not given"},
				},
				Required: []string{"url"},
			},
		},
	})
	r.Handlers["web_fetch"] = webFetchTool
	r.setRest("web_fetch", webFetchRest)
}

// webFetchTool is a web_fetch call with input in: the HTTP status, then the
// body as text (fetchText) from the call's offset, within 15 s.
func webFetchTool(ctx context.Context, in map[string]any) (string, error) {
	target, _ := in["url"].(string)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	status, text, err := fetchText(ctx, target)
	if err != nil {
		return "", err
	}
	return offsetText(status, text, textOffset(in)), nil
}

// fetchText GETs target and returns the status and up to 200 KiB of the
// body, as text (responseText).
func fetchText(ctx context.Context, target string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", useragent.For("agent-run"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200*1024))
	return resp.StatusCode, responseText(resp, body), nil
}

// responseText is body, resp's, as text: reduced to its readable text
// (htmlText) when its Content-Type, or with none what it sniffs as, names
// HTML; as it is otherwise.
func responseText(resp *http.Response, body []byte) string {
	kind := resp.Header.Get("Content-Type")
	if kind == "" {
		kind = http.DetectContentType(body)
	}
	if strings.Contains(strings.ToLower(kind), "html") {
		return htmlText(string(body), resp.Request.URL)
	}
	return string(body)
}

// offsetText is web_fetch's result: the status line, then text from byte
// offset, or a note when the offset is past its end.
func offsetText(status int, text string, offset int) string {
	if offset > 0 {
		if offset >= len(text) {
			return fmt.Sprintf("HTTP %d\n(the text is %d bytes, and offset %d is past its end)", status, len(text), offset)
		}
		text = text[offset:]
	}
	return fmt.Sprintf("HTTP %d\n%s", status, text)
}

// webFetchRest is how to get the rest of a web_fetch result cut to kept, for
// a call with input in. The result's first line is the status; the text kept
// is what follows it, and the next offset is the call's plus that.
func webFetchRest(in map[string]any, kept string) string {
	next := textOffset(in)
	if i := strings.IndexByte(kept, '\n'); i >= 0 {
		next += len(kept) - i - 1
	}
	return fmt.Sprintf("For the rest, call web_fetch again with the same url and offset %d.", next)
}

// textOffset is a web_fetch call's offset, the byte of the text it starts
// at: 0 when not given or under 0.
func textOffset(in map[string]any) int {
	if n := reqInt(in, "offset"); n > 0 {
		return n
	}
	return 0
}

// ─── Tool: chb_db_write ──────────────────────────────────────────
// Writes a CDE-encoded finding/gap/source, or closes a gap or conflict,
// directly in the DB, bypassing the CLI round-trip. It has the name of its
// MCP twin, so an agent is offered one name on every backend.
func (r *ToolRegistry) registerDBWrite(store *db.Store) {
	r.Schemas = append(r.Schemas, anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name: "chb_db_write",
			Description: anthropic.String(`Write a CDE-encoded record to the workspace database (hive.db). Kinds:
  "finding" { wave, agent, d1-d8, mss_label (assumption|guarantee|definition|unknown), finding, evidence?, source_urls? }
  "gap"     { wave, agent, description, priority (critical|important|minor; high→important, medium|low→minor), d1-d4? }
  "source"  { url, title?, wave?, agent?, contribution?, primary_source? }
  "resolve_gap"      { gap_id, wave, agent, finding_id } closes a gap with the finding that answers it
  "resolve_conflict" { conflict_id, wave, resolution } closes a conflict, recording how it was settled
All dimension values are ints matching the CDE dimension schema.`),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"kind":   map[string]any{"type": "string", "enum": []string{"finding", "gap", "source", "resolve_gap", "resolve_conflict"}},
					"fields": map[string]any{"type": "object"},
				},
				Required: []string{"kind", "fields"},
			},
		},
	})
	r.Handlers["chb_db_write"] = func(_ context.Context, in map[string]any) (string, error) {
		kind, _ := in["kind"].(string)
		fields, _ := in["fields"].(map[string]any)
		_, line, err := store.WriteRecord(kind, fields)
		return line, err
	}
}

// optInt is m's k as an int (anyInt), nil when m has no k or it is not a
// number anyInt reads.
func optInt(m map[string]any, k string) *int {
	if n, ok := anyInt(m[k]); ok {
		return &n
	}
	return nil
}

// anyInt is v, a decoded number, as an int: an int, an int64 or a float64,
// truncated, or a json.Number that holds an integer. ok is false for
// anything else.
func anyInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		return jsonNumberInt(n)
	}
	return 0, false
}

// jsonNumberInt is n as an int; ok is false when it holds no integer.
func jsonNumberInt(n json.Number) (int, bool) {
	i, err := n.Int64()
	return int(i), err == nil
}

func reqInt(m map[string]any, k string) int {
	if p := optInt(m, k); p != nil {
		return *p
	}
	return 0
}
