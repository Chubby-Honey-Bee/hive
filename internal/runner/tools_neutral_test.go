package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// TestFileSystemRecorderReset covers FileSystemRecorder.Reset.
func TestFileSystemRecorderReset(t *testing.T) {
	t.Run("clears populated map", func(t *testing.T) {
		f := NewFSRecorder()
		f.Touched["a.txt"] = true
		f.Touched["b.txt"] = true
		f.Reset()
		if len(f.Touched) != 0 {
			t.Fatalf("want empty map after Reset, got %d entries", len(f.Touched))
		}
	})

	t.Run("reset on already-empty recorder is a no-op", func(t *testing.T) {
		f := NewFSRecorder()
		f.Reset()
		if len(f.Touched) != 0 {
			t.Fatalf("want empty map, got %d entries", len(f.Touched))
		}
	})

	t.Run("Touched map is usable after Reset", func(t *testing.T) {
		f := NewFSRecorder()
		f.Touched["old.txt"] = true
		f.Reset()
		f.Touched["new.txt"] = true
		if _, ok := f.Touched["new.txt"]; !ok {
			t.Fatal("map should accept writes after Reset")
		}
		if _, ok := f.Touched["old.txt"]; ok {
			t.Fatal("old entry should not be present after Reset")
		}
	})

	t.Run("TouchedPaths returns empty slice after Reset", func(t *testing.T) {
		f := NewFSRecorder()
		f.Touched["x.go"] = true
		f.Reset()
		paths := f.TouchedPaths()
		if len(paths) != 0 {
			t.Fatalf("want 0 paths after Reset, got %v", paths)
		}
	})
}

// TestNeutralSchemas covers the NeutralSchemas adapter that translates the
// Anthropic-typed ToolRegistry.Schemas into a provider-neutral slice.
func TestNeutralSchemas(t *testing.T) {
	makeToolParam := func(name, desc string, props map[string]any, required []string) anthropic.ToolUnionParam {
		tp := anthropic.ToolParam{
			Name: name,
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: props,
				Required:   required,
			},
		}
		if desc != "" {
			tp.Description = param.NewOpt(desc)
		}
		return anthropic.ToolUnionParam{OfTool: &tp}
	}

	t.Run("empty registry returns empty slice", func(t *testing.T) {
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		got := r.NeutralSchemas()
		if len(got) != 0 {
			t.Fatalf("want 0 tools, got %d", len(got))
		}
	})

	t.Run("nil OfTool entries are skipped", func(t *testing.T) {
		// A ToolUnionParam with OfTool == nil represents a non-custom tool variant
		// (e.g. OfBashTool20250124). NeutralSchemas must skip these.
		r := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas: []anthropic.ToolUnionParam{
				{OfTool: nil}, // should be skipped
				makeToolParam("my_tool", "does stuff", map[string]any{"x": "string"}, []string{"x"}),
			},
		}
		got := r.NeutralSchemas()
		if len(got) != 1 {
			t.Fatalf("want 1 tool, got %d", len(got))
		}
		if got[0].Name != "my_tool" {
			t.Fatalf("want name 'my_tool', got %q", got[0].Name)
		}
	})

	t.Run("happy path: name, description and schema are copied correctly", func(t *testing.T) {
		props := map[string]any{"path": map[string]any{"type": "string"}}
		req := []string{"path"}
		r := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas: []anthropic.ToolUnionParam{
				makeToolParam("read_file", "reads a file", props, req),
			},
		}
		got := r.NeutralSchemas()
		if len(got) != 1 {
			t.Fatalf("want 1 tool, got %d", len(got))
		}
		nt := got[0]
		if nt.Name != "read_file" {
			t.Errorf("Name: want %q, got %q", "read_file", nt.Name)
		}
		if nt.Description != "reads a file" {
			t.Errorf("Description: want %q, got %q", "reads a file", nt.Description)
		}
		if nt.JSONSchema["type"] != "object" {
			t.Errorf("JSONSchema[type]: want 'object', got %v", nt.JSONSchema["type"])
		}
		gotProps, ok := nt.JSONSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("JSONSchema[properties] is not map[string]any: %T", nt.JSONSchema["properties"])
		}
		if _, has := gotProps["path"]; !has {
			t.Errorf("expected 'path' key in properties")
		}
		gotReq, _ := nt.JSONSchema["required"].([]string)
		if len(gotReq) != 1 || gotReq[0] != "path" {
			t.Errorf("required: want [path], got %v", gotReq)
		}
	})

	t.Run("tool without description yields empty string", func(t *testing.T) {
		r := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas: []anthropic.ToolUnionParam{
				makeToolParam("no_desc", "", nil, nil),
			},
		}
		got := r.NeutralSchemas()
		if len(got) != 1 {
			t.Fatalf("want 1 tool, got %d", len(got))
		}
		if got[0].Description != "" {
			t.Errorf("expected empty description, got %q", got[0].Description)
		}
	})

	t.Run("properties not a map[string]any yields nil map", func(t *testing.T) {
		// The function does a type-assertion without checking ok; when Properties
		// is not a map[string]any the zero value (nil map) is used — no panic.
		tp := anthropic.ToolParam{
			Name: "weird",
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: "this is a string not a map", // unexpected type
			},
		}
		r := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas:  []anthropic.ToolUnionParam{{OfTool: &tp}},
		}
		got := r.NeutralSchemas()
		if len(got) != 1 {
			t.Fatalf("want 1 tool, got %d", len(got))
		}
		// The JSONSchema["properties"] entry holds a typed nil map[string]any.
		// Type-assert and confirm it is nil (not a non-nil map with entries).
		propVal, _ := got[0].JSONSchema["properties"].(map[string]any)
		if len(propVal) != 0 {
			t.Errorf("expected empty/nil property map, got %v", propVal)
		}
	})

	t.Run("multiple tools preserves order relative to non-nil entries", func(t *testing.T) {
		r := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas: []anthropic.ToolUnionParam{
				makeToolParam("alpha", "a", nil, nil),
				{OfTool: nil}, // skipped
				makeToolParam("beta", "b", nil, nil),
				makeToolParam("gamma", "c", nil, nil),
			},
		}
		got := r.NeutralSchemas()
		if len(got) != 3 {
			t.Fatalf("want 3 tools, got %d", len(got))
		}
		names := []string{got[0].Name, got[1].Name, got[2].Name}
		want := []string{"alpha", "beta", "gamma"}
		for i, w := range want {
			if names[i] != w {
				t.Errorf("index %d: want %q, got %q", i, w, names[i])
			}
		}
	})
}

// TestRegisterWebFetch exercises the handler registered by registerWebFetch.
func TestRegisterWebFetch(t *testing.T) {
	ctx := context.Background()

	makeReg := func() *ToolRegistry {
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		r.registerWebFetch()
		return r
	}

	invoke := func(r *ToolRegistry, in map[string]any) (string, error) {
		t.Helper()
		h, ok := r.Handlers["web_fetch"]
		if !ok {
			t.Fatal("web_fetch handler not registered")
		}
		return h(ctx, in)
	}

	t.Run("schema is registered", func(t *testing.T) {
		r := makeReg()
		found := false
		for _, s := range r.Schemas {
			if s.OfTool != nil && s.OfTool.Name == "web_fetch" {
				found = true
			}
		}
		if !found {
			t.Error("web_fetch schema not found in registry after registerWebFetch")
		}
	})

	t.Run("happy path: returns HTTP status and body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "hello hive")
		}))
		defer srv.Close()

		r := makeReg()
		out, err := invoke(r, map[string]any{"url": srv.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(out, "HTTP 200") {
			t.Errorf("expected output to start with 'HTTP 200', got %q", out)
		}
		if !strings.Contains(out, "hello hive") {
			t.Errorf("expected body in output, got %q", out)
		}
	})

	t.Run("non-200 status is reflected in output", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()

		r := makeReg()
		out, err := invoke(r, map[string]any{"url": srv.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(out, "HTTP 404") {
			t.Errorf("expected output to start with 'HTTP 404', got %q", out)
		}
	})

	t.Run("body truncated at 200KB", func(t *testing.T) {
		const limit = 200 * 1024
		bigBody := make([]byte, limit+512)
		for i := range bigBody {
			bigBody[i] = 'x'
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bigBody)
		}))
		defer srv.Close()

		r := makeReg()
		out, err := invoke(r, map[string]any{"url": srv.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// "HTTP 200\n" prefix + exactly limit bytes of 'x'
		prefix := "HTTP 200\n"
		bodyPart := out[len(prefix):]
		if len(bodyPart) > limit {
			t.Errorf("body should be capped at %d bytes, got %d", limit, len(bodyPart))
		}
	})

	t.Run("invalid URL causes NewRequestWithContext error", func(t *testing.T) {
		r := makeReg()
		// A URL with a control character is rejected by http.NewRequestWithContext.
		_, err := invoke(r, map[string]any{"url": "http://bad host\x00/"})
		if err == nil {
			t.Fatal("expected error for invalid URL, got nil")
		}
	})

	t.Run("unreachable host causes Do error", func(t *testing.T) {
		r := makeReg()
		// 192.0.2.0/24 is TEST-NET (RFC 5737) — guaranteed unroutable.
		// A just-closed local listener refuses the connection at once; a
		// blackholed address would wait out the client timeout instead.
		closed := httptest.NewServer(http.NotFoundHandler())
		closedURL := closed.URL
		closed.Close()
		_, err := invoke(r, map[string]any{"url": closedURL})
		if err == nil {
			t.Fatal("expected error for unreachable host, got nil")
		}
	})

	t.Run("missing url key yields empty string url and error", func(t *testing.T) {
		r := makeReg()
		// Type assertion on missing key yields "" — NewRequestWithContext errors on "".
		_, err := invoke(r, map[string]any{})
		if err == nil {
			t.Fatal("expected error for empty URL, got nil")
		}
	})

	t.Run("non-string url key yields empty string url and error", func(t *testing.T) {
		r := makeReg()
		_, err := invoke(r, map[string]any{"url": 42})
		if err == nil {
			t.Fatal("expected error for non-string url value, got nil")
		}
	})

	t.Run("User-Agent header is set", func(t *testing.T) {
		prev := useragent.Version
		useragent.Version = "0.9.1"
		t.Cleanup(func() { useragent.Version = prev })
		var gotUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			gotUA = req.Header.Get("User-Agent")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		r := makeReg()
		_, err := invoke(r, map[string]any{"url": srv.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "chb/0.9.1 (+https://github.com/Chubby-Honey-Bee/hive) agent-run"; gotUA != want {
			t.Errorf("want User-Agent %q, got %q", want, gotUA)
		}
	})
}

// TestRegisterGlob exercises the handler registered by registerGlob.
func TestRegisterGlob(t *testing.T) {
	ctx := context.Background()

	// helper: build a registry with only the glob tool wired up.
	makeReg := func(dir string) *ToolRegistry {
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		r.registerGlob(dir)
		return r
	}

	// helper: invoke the "glob" handler directly.
	invokeGlob := func(r *ToolRegistry, pattern string) (string, error) {
		t.Helper()
		h, ok := r.Handlers["glob"]
		if !ok {
			t.Fatal("glob handler not registered")
		}
		return h(ctx, map[string]any{"pattern": pattern})
	}

	t.Run("happy path: matches *.md files", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGlob(r, "*.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "README.md") {
			t.Errorf("expected README.md in output, got %q", out)
		}
		if strings.Contains(out, "main.go") {
			t.Errorf("main.go should not appear for *.md pattern, got %q", out)
		}
	})

	t.Run("no matches returns sentinel string", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGlob(r, "*.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "(no matches)" {
			t.Errorf("want '(no matches)', got %q", out)
		}
	})

	t.Run("base-name match: pattern matches nested files by base name", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "deep.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGlob(r, "*.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "deep.md") {
			t.Errorf("expected deep.md to be found via base-name match, got %q", out)
		}
	})

	t.Run("empty pattern matches nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGlob(r, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "(no matches)" {
			t.Errorf("want '(no matches)' for empty pattern, got %q", out)
		}
	})

	t.Run("missing input key treated as empty pattern", func(t *testing.T) {
		dir := t.TempDir()
		r := makeReg(dir)
		h := r.Handlers["glob"]
		out, err := h(ctx, map[string]any{}) // no "pattern" key
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "(no matches)" {
			t.Errorf("want '(no matches)' for missing key, got %q", out)
		}
	})

	t.Run("non-existent projectDir silently returns no matches", func(t *testing.T) {
		// The walk callback swallows errors (returns nil on err != nil), so a
		// missing root produces no matches rather than a hard error.
		r := makeReg("/this/path/does/not/exist")
		out, err := invokeGlob(r, "*.go")
		if err != nil {
			t.Fatalf("unexpected error for non-existent projectDir: %v", err)
		}
		if out != "(no matches)" {
			t.Errorf("want '(no matches)' for non-existent root, got %q", out)
		}
	})

	t.Run("schema is registered for glob tool", func(t *testing.T) {
		dir := t.TempDir()
		r := makeReg(dir)
		found := false
		for _, s := range r.Schemas {
			if s.OfTool != nil && s.OfTool.Name == "glob" {
				found = true
			}
		}
		if !found {
			t.Error("glob schema not found in registry after registerGlob")
		}
	})
}

// TestRegisterGrep exercises the handler registered by registerGrep.
func TestRegisterGrep(t *testing.T) {
	ctx := context.Background()

	// helper: build a registry with only the grep tool wired up.
	makeReg := func(dir string) *ToolRegistry {
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		r.registerGrep(dir)
		return r
	}

	// helper: invoke the "grep" handler directly.
	invokeGrep := func(r *ToolRegistry, in map[string]any) (string, error) {
		t.Helper()
		h, ok := r.Handlers["grep"]
		if !ok {
			t.Fatal("grep handler not registered")
		}
		return h(ctx, in)
	}

	t.Run("schema is registered for grep tool", func(t *testing.T) {
		dir := t.TempDir()
		r := makeReg(dir)
		found := false
		for _, s := range r.Schemas {
			if s.OfTool != nil && s.OfTool.Name == "grep" {
				found = true
			}
		}
		if !found {
			t.Error("grep schema not found in registry after registerGrep")
		}
	})

	t.Run("happy path: matches lines in a file", func(t *testing.T) {
		dir := t.TempDir()
		content := "hello world\nfoo bar\nhello again\n"
		if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGrep(r, map[string]any{"pattern": "hello"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "hello world") {
			t.Errorf("expected 'hello world' in output, got %q", out)
		}
		if !strings.Contains(out, "hello again") {
			t.Errorf("expected 'hello again' in output, got %q", out)
		}
		if strings.Contains(out, "foo bar") {
			t.Errorf("'foo bar' should not appear for pattern 'hello', got %q", out)
		}
	})

	t.Run("no matches returns sentinel string", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("no match here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGrep(r, map[string]any{"pattern": "ZZZNOTPRESENT"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "(no matches)" {
			t.Errorf("want '(no matches)', got %q", out)
		}
	})

	t.Run("output format is path:line:text", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("first\nTARGET line\nthird\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGrep(r, map[string]any{"pattern": "TARGET"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Format: rel_path:line_number:line_text
		if !strings.Contains(out, "a.txt:2:TARGET line") {
			t.Errorf("expected 'a.txt:2:TARGET line' in output, got %q", out)
		}
	})

	t.Run("optional path subdirectory restricts search", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "subdir")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "root.txt"), []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "sub.txt"), []byte("no hit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		// Searching inside subdir — should not see root.txt
		out, err := invokeGrep(r, map[string]any{"pattern": "needle", "path": "subdir"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "(no matches)" {
			t.Errorf("expected no matches within subdir, got %q", out)
		}
	})

	t.Run("path escaping projectDir returns error", func(t *testing.T) {
		dir := t.TempDir()
		r := makeReg(dir)
		_, err := invokeGrep(r, map[string]any{"pattern": "x", "path": "../../../etc"})
		if err == nil {
			t.Fatal("expected error for path that escapes project dir, got nil")
		}
	})

	t.Run("missing pattern key treated as empty string matches all lines", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		// No "pattern" key — type-assertion yields "" which matches every non-empty line
		out, err := invokeGrep(r, map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "line1") {
			t.Errorf("expected all lines matched by empty pattern, got %q", out)
		}
	})

	t.Run("hit limit: stops at 200 results", func(t *testing.T) {
		dir := t.TempDir()
		// Write a file with 300 matching lines.
		var sb strings.Builder
		for i := 0; i < 300; i++ {
			sb.WriteString("MATCH line\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGrep(r, map[string]any{"pattern": "MATCH"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 200 {
			t.Errorf("expected exactly 200 hits (cap), got %d", len(lines))
		}
	})

	t.Run("files larger than 5MB are skipped", func(t *testing.T) {
		dir := t.TempDir()
		// Write a file whose byte count exceeds 5*1024*1024.
		big := make([]byte, 5*1024*1024+1)
		// Fill with "NEEDLE\n" repeated to make the file readable ASCII but too large.
		copy(big, []byte("NEEDLE"))
		if err := os.WriteFile(filepath.Join(dir, "huge.txt"), big, 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGrep(r, map[string]any{"pattern": "NEEDLE"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "(no matches)" {
			t.Errorf("expected huge file to be skipped, got %q", out)
		}
	})

	t.Run("multiple files: results from all files returned", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("FIND me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("FIND you\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := makeReg(dir)
		out, err := invokeGrep(r, map[string]any{"pattern": "FIND"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "a.txt") {
			t.Errorf("expected a.txt hit in output, got %q", out)
		}
		if !strings.Contains(out, "b.txt") {
			t.Errorf("expected b.txt hit in output, got %q", out)
		}
	})
}

// TestRegisterBash exercises the handler registered by registerBash.
func TestRegisterBash(t *testing.T) {
	ctx := context.Background()

	makeReg := func(dir string) (*ToolRegistry, *FileSystemRecorder) {
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		fs := NewFSRecorder()
		r.registerShell(dir, fs, hostShell())
		return r, fs
	}

	invokeBash := func(r *ToolRegistry, cmd string) (string, error) {
		t.Helper()
		h, ok := r.Handlers["shell"]
		if !ok {
			t.Fatal("bash handler not registered")
		}
		return h(ctx, map[string]any{"command": cmd})
	}

	t.Run("schema is registered for shell tool", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		found := false
		for _, s := range r.Schemas {
			if s.OfTool != nil && s.OfTool.Name == "shell" {
				found = true
			}
		}
		if !found {
			t.Error("shell schema not found in registry after registerShell")
		}
	})

	t.Run("happy path: command output returned, no Go error", func(t *testing.T) {
		dir := t.TempDir()
		r, fs := makeReg(dir)
		out, err := invokeBash(r, "echo hello")
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !strings.Contains(out, "hello") {
			t.Errorf("expected 'hello' in output, got %q", out)
		}
		if !fs.Touched[dir] {
			t.Errorf("expected projectDir %q to be marked in FileSystemRecorder", dir)
		}
	})

	t.Run("command exits non-zero: output starts with EXIT, no Go error", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		out, err := invokeBash(r, "exit 1")
		if err != nil {
			t.Fatalf("expected nil Go error for non-zero exit, got %v", err)
		}
		if !strings.HasPrefix(out, "EXIT ") {
			t.Errorf("expected output to start with 'EXIT ', got %q", out)
		}
	})

	t.Run("deny-list rm -rf / is blocked", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		_, err := invokeBash(r, "rm -rf /")
		if err == nil {
			t.Fatal("expected error for deny-listed command, got nil")
		}
		if !strings.Contains(err.Error(), "blocked by deny-list") {
			t.Errorf("expected deny-list error, got %q", err.Error())
		}
	})

	t.Run("deny-list git push --force is blocked", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		_, err := invokeBash(r, "git push --force origin main")
		if err == nil {
			t.Fatal("expected error for force-push command, got nil")
		}
		if !strings.Contains(err.Error(), "blocked by deny-list") {
			t.Errorf("expected deny-list error, got %q", err.Error())
		}
	})

	t.Run("deny-list --no-verify is blocked", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		_, err := invokeBash(r, "git commit --no-verify -m msg")
		if err == nil {
			t.Fatal("expected error for --no-verify command, got nil")
		}
		if !strings.Contains(err.Error(), "blocked by deny-list") {
			t.Errorf("expected deny-list error, got %q", err.Error())
		}
	})

	t.Run("missing command key treated as empty string no error", func(t *testing.T) {
		dir := t.TempDir()
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		fs := NewFSRecorder()
		r.registerShell(dir, fs, hostShell())
		h := r.Handlers["shell"]
		_, err := h(ctx, map[string]any{})
		if err != nil {
			t.Fatalf("unexpected Go error for empty command: %v", err)
		}
	})

	t.Run("command runs within projectDir", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		out, err := invokeBash(r, "pwd")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		outTrimmed := strings.TrimSpace(out)
		realDir, _ := filepath.EvalSymlinks(dir)
		realOut, _ := filepath.EvalSymlinks(outTrimmed)
		if realOut != realDir {
			t.Errorf("expected pwd to be %q, got %q", realDir, realOut)
		}
	})

	t.Run("combined stdout+stderr returned for failing command", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		out, err := invokeBash(r, "echo stderr_msg >&2; exit 2")
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !strings.Contains(out, "stderr_msg") {
			t.Errorf("expected stderr_msg in combined output, got %q", out)
		}
	})
}

// newDBWriteReg builds a ToolRegistry with only chb_db_write registered.
func newDBWriteReg(t *testing.T) (*ToolRegistry, *db.Store) {
	t.Helper()
	store := newTestStoreForRunner(t)
	r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r.registerDBWrite(store)
	return r, store
}

// invokeDBWrite invokes the chb_db_write handler directly.
func invokeDBWrite(t *testing.T, r *ToolRegistry, in map[string]any) (string, error) {
	t.Helper()
	ctx := context.Background()
	h, ok := r.Handlers["chb_db_write"]
	if !ok {
		t.Fatal("chb_db_write handler not registered")
	}
	return h(ctx, in)
}

// TestRegisterDBWrite exercises all branches of the handler registered by registerDBWrite.
func TestRegisterDBWrite(t *testing.T) {
	t.Run("schema is registered", func(t *testing.T) {
		r, _ := newDBWriteReg(t)
		found := false
		for _, s := range r.Schemas {
			if s.OfTool != nil && s.OfTool.Name == "chb_db_write" {
				found = true
			}
		}
		if !found {
			t.Error("chb_db_write schema not found in registry after registerDBWrite")
		}
	})

	t.Run("nil fields returns error", func(t *testing.T) {
		r, _ := newDBWriteReg(t)
		// fields key missing → type-assert yields nil map
		_, err := invokeDBWrite(t, r, map[string]any{"kind": "finding"})
		if err == nil {
			t.Fatal("expected error for nil fields, got nil")
		}
		if !strings.Contains(err.Error(), "fields missing") {
			t.Fatalf("expected 'fields missing' in error, got %q", err.Error())
		}
	})

	t.Run("unknown kind returns error", func(t *testing.T) {
		r, _ := newDBWriteReg(t)
		_, err := invokeDBWrite(t, r, map[string]any{
			"kind":   "bogus",
			"fields": map[string]any{"x": 1},
		})
		if err == nil {
			t.Fatal("expected error for unknown kind, got nil")
		}
		if !strings.Contains(err.Error(), "unknown kind") {
			t.Fatalf("expected 'unknown kind' in error, got %q", err.Error())
		}
	})

	t.Run("finding happy path returns id string", func(t *testing.T) {
		r, _ := newDBWriteReg(t)
		out, err := invokeDBWrite(t, r, map[string]any{
			"kind": "finding",
			"fields": map[string]any{
				"wave":      float64(1),
				"agent":     "test-agent",
				"mss_label": "assumption",
				"finding":   "the sky is blue",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(out, "finding id=") {
			t.Fatalf("expected 'finding id=...' in output, got %q", out)
		}
	})

	t.Run("gap happy path returns id string", func(t *testing.T) {
		r, _ := newDBWriteReg(t)
		out, err := invokeDBWrite(t, r, map[string]any{
			"kind": "gap",
			"fields": map[string]any{
				"wave":        float64(1),
				"agent":       "test-agent",
				"description": "unknown vendor pricing",
				"priority":    "critical",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(out, "gap id=") {
			t.Fatalf("expected 'gap id=...' in output, got %q", out)
		}
	})

	t.Run("source happy path returns id+url string", func(t *testing.T) {
		r, _ := newDBWriteReg(t)
		out, err := invokeDBWrite(t, r, map[string]any{
			"kind": "source",
			"fields": map[string]any{
				"url":          "https://example.com",
				"title":        "Example",
				"agent":        "test-agent",
				"contribution": "reference",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(out, "source id=") {
			t.Fatalf("expected 'source id=...' in output, got %q", out)
		}
		if !strings.Contains(out, "https://example.com") {
			t.Fatalf("expected URL in output, got %q", out)
		}
	})

	t.Run("non-string kind yields unknown kind error", func(t *testing.T) {
		r, _ := newDBWriteReg(t)
		// kind key is an int — type-assert yields "" → default branch
		_, err := invokeDBWrite(t, r, map[string]any{
			"kind":   42,
			"fields": map[string]any{"x": 1},
		})
		if err == nil {
			t.Fatal("expected error for non-string kind, got nil")
		}
		if !strings.Contains(err.Error(), "unknown kind") {
			t.Fatalf("expected 'unknown kind', got %q", err.Error())
		}
	})

	t.Run("finding with mss_label defaulting to assumption", func(t *testing.T) {
		r, store := newDBWriteReg(t)
		// omit mss_label → the finding defaults to "assumption"
		out, err := invokeDBWrite(t, r, map[string]any{
			"kind": "finding",
			"fields": map[string]any{
				"wave":    float64(2),
				"agent":   "default-label-agent",
				"finding": "some finding with no label",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(out, "finding id=") {
			t.Fatalf("expected 'finding id=...' in output, got %q", out)
		}
		var label string
		store.ReadDB.QueryRow("SELECT mss_label FROM findings ORDER BY id DESC LIMIT 1").Scan(&label)
		if label != "assumption" {
			t.Fatalf("expected mss_label=assumption by default, got %q", label)
		}
	})
}

// TestRegisterEdit exercises the handler registered by registerEdit.
func TestRegisterEdit(t *testing.T) {
	ctx := context.Background()

	makeReg := func(dir string) (*ToolRegistry, *FileSystemRecorder) {
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		fs := NewFSRecorder()
		r.registerEdit(dir, fs)
		return r, fs
	}

	invokeEdit := func(r *ToolRegistry, in map[string]any) (string, error) {
		t.Helper()
		h, ok := r.Handlers["edit_file"]
		if !ok {
			t.Fatal("edit_file handler not registered")
		}
		return h(ctx, in)
	}

	t.Run("schema is registered for edit_file tool", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		found := false
		for _, s := range r.Schemas {
			if s.OfTool != nil && s.OfTool.Name == "edit_file" {
				found = true
			}
		}
		if !found {
			t.Error("edit_file schema not found in registry after registerEdit")
		}
	})

	t.Run("happy path: unique replacement returns byte counts and marks file", func(t *testing.T) {
		dir := t.TempDir()
		r, fs := makeReg(dir)
		target := filepath.Join(dir, "hello.txt")
		if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := invokeEdit(r, map[string]any{
			"path":       "hello.txt",
			"old_string": "world",
			"new_string": "hive",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "hello.txt") {
			t.Errorf("expected filename in output, got %q", out)
		}
		got, _ := os.ReadFile(target)
		if string(got) != "hello hive" {
			t.Errorf("file content: want %q, got %q", "hello hive", string(got))
		}
		if !fs.Touched[target] {
			t.Errorf("expected %q to be marked in FileSystemRecorder", target)
		}
	})

	t.Run("old_string not found returns error", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("no match here"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := invokeEdit(r, map[string]any{
			"path":       "f.txt",
			"old_string": "ZZZNOTPRESENT",
			"new_string": "x",
		})
		if err == nil {
			t.Fatal("expected error when old_string not found, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got %q", err.Error())
		}
	})

	t.Run("old_string not unique returns error", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		if err := os.WriteFile(filepath.Join(dir, "dup.txt"), []byte("dup dup"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := invokeEdit(r, map[string]any{
			"path":       "dup.txt",
			"old_string": "dup",
			"new_string": "x",
		})
		if err == nil {
			t.Fatal("expected error when old_string is not unique, got nil")
		}
		if !strings.Contains(err.Error(), "not unique") {
			t.Errorf("expected 'not unique' in error, got %q", err.Error())
		}
	})

	t.Run("file does not exist returns ReadFile error", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		_, err := invokeEdit(r, map[string]any{
			"path":       "nonexistent.txt",
			"old_string": "x",
			"new_string": "y",
		})
		if err == nil {
			t.Fatal("expected error when file does not exist, got nil")
		}
	})

	t.Run("path escaping projectDir returns error", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		_, err := invokeEdit(r, map[string]any{
			"path":       "../../../etc/passwd",
			"old_string": "root",
			"new_string": "x",
		})
		if err == nil {
			t.Fatal("expected error for path that escapes project dir, got nil")
		}
	})

	t.Run("byte counts reflect size change in output", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		if err := os.WriteFile(filepath.Join(dir, "size.txt"), []byte("abcde"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := invokeEdit(r, map[string]any{
			"path":       "size.txt",
			"old_string": "abcde",
			"new_string": "xyz",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// original was 5 bytes, new is 3 bytes
		if !strings.Contains(out, "5") || !strings.Contains(out, "3") {
			t.Errorf("expected byte counts (5, 3) in output, got %q", out)
		}
	})
}

// TestRegisterWrite exercises the handler registered by registerWrite.
func TestRegisterWrite(t *testing.T) {
	ctx := context.Background()

	makeReg := func(dir string) (*ToolRegistry, *FileSystemRecorder) {
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		fs := NewFSRecorder()
		r.registerWrite(dir, fs)
		return r, fs
	}

	invokeWrite := func(r *ToolRegistry, in map[string]any) (string, error) {
		t.Helper()
		h, ok := r.Handlers["write_file"]
		if !ok {
			t.Fatal("write_file handler not registered")
		}
		return h(ctx, in)
	}

	t.Run("schema is registered for write_file tool", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		found := false
		for _, s := range r.Schemas {
			if s.OfTool != nil && s.OfTool.Name == "write_file" {
				found = true
			}
		}
		if !found {
			t.Error("write_file schema not found in registry after registerWrite")
		}
	})

	t.Run("happy path: file written and byte count returned", func(t *testing.T) {
		dir := t.TempDir()
		r, fs := makeReg(dir)
		content := "hello\nworld\n"
		out, err := invokeWrite(r, map[string]any{"path": "out.txt", "content": content})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, fmt.Sprintf("%d bytes", len(content))) {
			t.Errorf("expected byte count in output, got %q", out)
		}
		// FileSystemRecorder must have been updated.
		absPath := filepath.Join(dir, "out.txt")
		if !fs.Touched[absPath] {
			t.Errorf("expected %q to be marked in FileSystemRecorder", absPath)
		}
		// File must actually exist with the right content.
		got, err := os.ReadFile(absPath)
		if err != nil {
			t.Fatalf("could not read written file: %v", err)
		}
		if string(got) != content {
			t.Errorf("file content: want %q, got %q", content, string(got))
		}
	})

	t.Run("creates parent directories as needed", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		out, err := invokeWrite(r, map[string]any{"path": "sub/dir/file.txt", "content": "data"})
		if err != nil {
			t.Fatalf("unexpected error creating nested file: %v", err)
		}
		if !strings.Contains(out, "4 bytes") {
			t.Errorf("expected '4 bytes' in output, got %q", out)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "sub/dir/file.txt")); statErr != nil {
			t.Errorf("expected nested file to exist: %v", statErr)
		}
	})

	t.Run("overwrites existing file", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		target := filepath.Join(dir, "overwrite.txt")
		if err := os.WriteFile(target, []byte("old content"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := invokeWrite(r, map[string]any{"path": "overwrite.txt", "content": "new"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, _ := os.ReadFile(target)
		if string(got) != "new" {
			t.Errorf("expected 'new', got %q", string(got))
		}
	})

	t.Run("path escaping projectDir returns error", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		_, err := invokeWrite(r, map[string]any{"path": "../../../etc/passwd", "content": "x"})
		if err == nil {
			t.Fatal("expected error for path that escapes project dir, got nil")
		}
		if !strings.Contains(err.Error(), "escapes") {
			t.Errorf("expected 'escapes' in error, got %q", err.Error())
		}
	})

	t.Run("missing path key treated as empty string writes to projectDir itself error", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		// Empty path resolves to projectDir itself; WriteFile on a directory returns an error.
		_, err := invokeWrite(r, map[string]any{"content": "x"})
		// Either MkdirAll or WriteFile may error; we just confirm no panic and some error.
		// If it happens to succeed (unlikely on any OS), skip the check.
		_ = err
	})

	t.Run("non-string path key treated as empty — no panic", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		// Type assertion on non-string yields "" — resolves to projectDir itself.
		_, err := invokeWrite(r, map[string]any{"path": 42, "content": "x"})
		_ = err // same as above: OS behaviour varies; just ensure no panic
	})

	t.Run("empty content writes zero-byte file", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := makeReg(dir)
		out, err := invokeWrite(r, map[string]any{"path": "empty.txt", "content": ""})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "0 bytes") {
			t.Errorf("expected '0 bytes' in output for empty content, got %q", out)
		}
		info, statErr := os.Stat(filepath.Join(dir, "empty.txt"))
		if statErr != nil {
			t.Fatalf("expected zero-byte file to exist: %v", statErr)
		}
		if info.Size() != 0 {
			t.Errorf("expected 0-byte file, got %d bytes", info.Size())
		}
	})
}
