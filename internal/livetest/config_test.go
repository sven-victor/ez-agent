package livetest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sven-victor/ez-agent/internal/livetest"
)

func TestLoadFromEnvFile(t *testing.T) {
	dir := t.TempDir()
	content := "API_KEY=k1\nOPENAI_BASE_URL=https://o.example\nANTHROPIC_BASE_URL=https://a.example\nLIVE_MODEL=m1\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := livetest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e.APIKey != "k1" || e.OpenAIBaseURL != "https://o.example" || e.AnthropicBaseURL != "https://a.example" {
		t.Fatalf("env=%+v", e)
	}
	if e.OpenAIModel != "m1" || e.AnthropicModel != "m1" {
		t.Fatalf("models openai=%q anthropic=%q", e.OpenAIModel, e.AnthropicModel)
	}
}

func TestLoadPrefersProviderSpecificModel(t *testing.T) {
	dir := t.TempDir()
	content := "API_KEY=k\nOPENAI_BASE_URL=https://o\nANTHROPIC_BASE_URL=https://a\nLIVE_MODEL=shared\nLIVE_OPENAI_MODEL=o-model\nLIVE_ANTHROPIC_MODEL=a-model\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := livetest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e.OpenAIModel != "o-model" || e.AnthropicModel != "a-model" {
		t.Fatalf("models openai=%q anthropic=%q", e.OpenAIModel, e.AnthropicModel)
	}
}
