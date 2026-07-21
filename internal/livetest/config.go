// Package livetest loads credentials for live (network) tests and demos.
//
// Expected .env / environment keys:
//
//	API_KEY=...
//	OPENAI_BASE_URL=https://api.openai.com/v1
//	ANTHROPIC_BASE_URL=https://api.anthropic.com
//
// Optional:
//
//	LIVE_MODEL / LIVE_OPENAI_MODEL / LIVE_ANTHROPIC_MODEL
package livetest

import (
	"os"
	"testing"

	"github.com/sven-victor/ez-agent/internal/envfile"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/model/anthropic"
	"github.com/sven-victor/ez-agent/model/openai"
)

const DefaultModel = "claude-sonnet-4-20250514"

// Env is the resolved live-test configuration.
type Env struct {
	APIKey           string
	OpenAIBaseURL    string
	AnthropicBaseURL string
	OpenAIModel      string
	AnthropicModel   string
}

// Load finds the nearest .env from dir (or process env) and returns credentials.
// Missing keys are left empty; callers decide whether to skip.
func Load(dir string) (Env, error) {
	vars, err := envfile.LoadNearest(dir, ".env")
	if err != nil {
		return Env{}, err
	}
	get := func(key string) string {
		if v := vars[key]; v != "" {
			return v
		}
		return os.Getenv(key)
	}
	e := Env{
		APIKey:           get("API_KEY"),
		OpenAIBaseURL:    get("OPENAI_BASE_URL"),
		AnthropicBaseURL: get("ANTHROPIC_BASE_URL"),
	}
	e.OpenAIModel = firstNonEmpty(get("LIVE_OPENAI_MODEL"), get("LIVE_MODEL"), DefaultModel)
	e.AnthropicModel = firstNonEmpty(get("LIVE_ANTHROPIC_MODEL"), get("LIVE_MODEL"), DefaultModel)
	return e, nil
}

// LoadFromWD loads using the process working directory.
func LoadFromWD() (Env, error) {
	wd, err := os.Getwd()
	if err != nil {
		return Env{}, err
	}
	return Load(wd)
}

// RequireOpenAI skips the test when OpenAI-compatible credentials are missing.
func RequireOpenAI(t *testing.T) Env {
	t.Helper()
	e, err := LoadFromWD()
	if err != nil {
		t.Fatal(err)
	}
	if e.APIKey == "" || e.OpenAIBaseURL == "" {
		t.Skip("live test skipped: API_KEY and OPENAI_BASE_URL not configured")
	}
	return e
}

// RequireAnthropic skips the test when Anthropic credentials are missing.
func RequireAnthropic(t *testing.T) Env {
	t.Helper()
	e, err := LoadFromWD()
	if err != nil {
		t.Fatal(err)
	}
	if e.APIKey == "" || e.AnthropicBaseURL == "" {
		t.Skip("live test skipped: API_KEY and ANTHROPIC_BASE_URL not configured")
	}
	return e
}

// NewOpenAI builds an OpenAI-compatible provider from Env.
func NewOpenAI(e Env) model.Provider {
	return openai.New(
		openai.WithAPIKey(e.APIKey),
		openai.WithBaseURL(e.OpenAIBaseURL),
		openai.WithModel(e.OpenAIModel),
	)
}

// NewAnthropic builds an Anthropic Messages provider from Env.
func NewAnthropic(e Env) model.Provider {
	return anthropic.New(
		anthropic.WithAPIKey(e.APIKey),
		anthropic.WithBaseURL(e.AnthropicBaseURL),
		anthropic.WithModel(e.AnthropicModel),
	)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
