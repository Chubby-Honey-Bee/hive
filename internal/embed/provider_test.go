package embed

import (
	"os"
	"strings"
	"testing"
)

func TestDetect_ExplicitProvider_Stub(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "stub")
	p, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if p.Name() != "stub/v1" {
		t.Fatalf("explicit stub override ignored, got %s", p.Name())
	}
}

// A misspelt provider is refused rather than falling through to
// auto-detection, which would embed with whichever provider the environment
// happened to allow.
func TestDetect_ExplicitProvider_UnknownRefused(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "opanai")
	if _, err := Detect(); err == nil {
		t.Fatal("Detect accepted an unknown HIVE_EMBED_PROVIDER")
	}
}

func TestDetect_AutoFallback_Stub(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("HIVE_LOCAL_EMBED_URL", "")
	p, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if p.Name() != "stub/v1" {
		t.Fatalf("auto-fallback should pick stub when nothing else is set, got %s", p.Name())
	}
}

func TestSelectByName_RejectsUnknown(t *testing.T) {
	if _, err := SelectByName("madeup"); err == nil {
		t.Fatalf("expected error for unknown provider")
	}
}

func TestSelectByName_OpenAI_RequiresKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := SelectByName("openai"); err == nil {
		t.Fatalf("expected OpenAI selection without key to error")
	}
}

func TestSelectByName_LocalHTTP_RequiresURL(t *testing.T) {
	t.Setenv("HIVE_LOCAL_EMBED_URL", "")
	if _, err := SelectByName("localhttp"); err == nil {
		t.Fatalf("expected localhttp selection without URL to error")
	}
}

func TestModelOrDefault(t *testing.T) {
	if got := modelOrDefault("custom", "fallback"); got != "custom" {
		t.Fatalf("explicit value should win, got %q", got)
	}
	if got := modelOrDefault("", "fallback"); got != "fallback" {
		t.Fatalf("empty should fall back, got %q", got)
	}
	// Unused but keeps `os` referenced in the test file.
	_ = os.Getenv("HOME")
}

func TestDetect_ExplicitProvider_OpenAI_WithKey(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "test-key")
	p, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "openai/") {
		t.Fatalf("expected openai/ prefix, got %s", p.Name())
	}
}

func TestDetect_ExplicitProvider_OpenAI_NoKey(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := Detect(); err == nil {
		t.Fatal("expected error when OPENAI_API_KEY is empty")
	}
}

func TestDetect_ExplicitProvider_LocalHTTP_WithURL(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "localhttp")
	t.Setenv("HIVE_LOCAL_EMBED_URL", "http://localhost:11434")
	p, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "localhttp/") {
		t.Fatalf("expected localhttp/ prefix, got %s", p.Name())
	}
}

func TestDetect_ExplicitProvider_LocalHTTP_NoURL(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "localhttp")
	t.Setenv("HIVE_LOCAL_EMBED_URL", "")
	if _, err := Detect(); err == nil {
		t.Fatal("expected error when HIVE_LOCAL_EMBED_URL is empty")
	}
}

func TestDetect_AutoDetect_OpenAI(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "")
	t.Setenv("OPENAI_API_KEY", "auto-test-key")
	t.Setenv("HIVE_LOCAL_EMBED_URL", "")
	p, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "openai/") {
		t.Fatalf("auto-detect should pick openai when key is set, got %s", p.Name())
	}
}

// A local embedding URL wins over OPENAI_API_KEY, which a local
// OpenAI-compatible server's model calls also need set: with both, the
// provider is the local one at that URL, on its model.
func TestDetect_AutoDetect_LocalURLWinsOverTheKey(t *testing.T) {
	const local = "http://127.0.0.1:11434"
	t.Setenv("HIVE_EMBED_PROVIDER", "")
	t.Setenv("HIVE_EMBED_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "local")
	t.Setenv("OPENAI_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("HIVE_LOCAL_EMBED_URL", local)
	p, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	lp, ok := p.(*LocalHTTPProvider)
	if !ok {
		t.Fatalf("Detect picked %s with HIVE_LOCAL_EMBED_URL set, want the local provider", p.Name())
	}
	if lp.baseURL != local || p.Name() != "localhttp/"+defaultLocalModel {
		t.Errorf("provider %s at %s, want localhttp/%s at %s", p.Name(), lp.baseURL, defaultLocalModel, local)
	}
}

func TestDetect_AutoDetect_LocalHTTP(t *testing.T) {
	t.Setenv("HIVE_EMBED_PROVIDER", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("HIVE_LOCAL_EMBED_URL", "http://localhost:11434")
	p, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "localhttp/") {
		t.Fatalf("auto-detect should pick localhttp when URL is set, got %s", p.Name())
	}
}

func TestSelectByName_LocalHTTP(t *testing.T) {
	t.Setenv("HIVE_LOCAL_EMBED_URL", "http://localhost:11434")
	p, err := SelectByName("localhttp")
	if err != nil {
		t.Fatalf("SelectByName localhttp: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "localhttp/") {
		t.Errorf("Name = %q; want localhttp/ prefix", p.Name())
	}
}

func TestSelectByName_OllamaAlias(t *testing.T) {
	t.Setenv("HIVE_LOCAL_EMBED_URL", "http://localhost:11434")
	p, err := SelectByName("ollama")
	if err != nil {
		t.Fatalf("SelectByName ollama: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "localhttp/") {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestSelectByName_StubAndCaseInsensitive(t *testing.T) {
	p, err := SelectByName("STUB")
	if err != nil {
		t.Fatalf("SelectByName STUB: %v", err)
	}
	if !strings.Contains(p.Name(), "stub") {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestSelectByName_LocalHTTPNoURL(t *testing.T) {
	t.Setenv("HIVE_LOCAL_EMBED_URL", "")
	_, err := SelectByName("localhttp")
	if err == nil {
		t.Error("expected error for localhttp without URL")
	}
}

func TestSelectByName_OpenAINoKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	_, err := SelectByName("openai")
	if err == nil {
		t.Error("expected error for openai without key")
	}
}
