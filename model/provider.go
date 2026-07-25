package model

import (
	"context"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

// Provider is a model backend. Vendor wire formats stay in adapter packages.
type Provider interface {
	Name() string
	Caps() Capabilities
	// Stream runs a streaming invocation. yield may be nil to suppress deltas;
	// Result is always the authoritative outcome.
	Stream(ctx context.Context, req Request, yield func(event.Event) error) (Result, error)
	// Complete runs a non-streaming invocation with the same Request/Result contract.
	Complete(ctx context.Context, req Request) (Result, error)
}

// Capabilities declares what a provider supports.
type Capabilities struct {
	TextIn      bool
	ImageIn     bool
	AudioIn     bool
	VideoIn     bool
	DocumentIn  bool
	AudioOut    bool
	Tools       bool
	Streaming   bool
	JSONSchema  bool
	SystemParts bool
	Opaque      bool // allow message.Opaque passthrough (custom providers)
}

// System is the system prompt, optionally with extra parts.
type System struct {
	Text  string
	Parts []message.Part
}

// Request is a single model invocation.
type Request struct {
	Messages     []message.Message
	System       System
	Tools        []tool.Spec
	ToolChoice   tool.Choice
	Options      map[string]any
	OutputSchema []byte // JSON Schema for native structured output
	OutputName   string // schema name for providers that require it
	MaxTokens    int    // max generation tokens (provider max_tokens); 0 = provider default
}

// Result is the authoritative model outcome (separate from stream deltas).
type Result struct {
	Message    message.Message
	StopReason run.StopReason
	Usage      run.Usage
	// RawRequest is the provider wire JSON body sent for this invocation (when available).
	RawRequest []byte
	// RawResponse is the provider wire response for this invocation.
	// For SSE streams, adapters append each data: payload (newline-separated) as lines are parsed.
	// For Complete, it is the full response body.
	RawResponse []byte
}
