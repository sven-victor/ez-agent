package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

const (
	defaultAsToolMaxDepth  = 4
	defaultAsToolPromptKey = "prompt"
)

var defaultAsToolInputSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"prompt": {
			"type": "string",
			"description": "The task or question to send to the sub-agent"
		}
	},
	"required": ["prompt"]
}`)

type asToolDepthKey struct{}

// ToolMeta describes how a child Agent is exposed as a tool to a parent Agent.
type ToolMeta struct {
	Name        string
	Description string
	// InputSchema overrides the default {prompt: string} schema.
	// The handler still reads a "prompt" string from the JSON object.
	InputSchema json.RawMessage
}

// AsToolOption customizes AsTool.
type AsToolOption func(*asToolConfig)

type asToolConfig struct {
	maxDepth int
}

// WithAsToolMaxDepth caps nested AsTool invocations (default 4).
// Depth is counted along the context chain from parent Handler to child Run.
func WithAsToolMaxDepth(n int) AsToolOption {
	return func(c *asToolConfig) {
		c.maxDepth = n
	}
}

// AsTool wraps child as a tool.Registration for a parent Agent.
//
// The child is invoked with a fresh run.Input from the tool's "prompt" field;
// the parent's transcript and session are not forwarded.
func AsTool(child *Agent, meta ToolMeta, opts ...AsToolOption) tool.Registration {
	cfg := asToolConfig{maxDepth: defaultAsToolMaxDepth}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.maxDepth <= 0 {
		cfg.maxDepth = defaultAsToolMaxDepth
	}

	schema := meta.InputSchema
	if len(schema) == 0 {
		schema = append(json.RawMessage(nil), defaultAsToolInputSchema...)
	}
	desc := meta.Description
	if desc == "" {
		desc = "Delegate a task to a specialized sub-agent."
	}
	name := meta.Name
	if name == "" {
		name = "sub_agent"
	}

	return tool.Registration{
		Spec: tool.Spec{
			Name:        name,
			Description: desc,
			InputSchema: schema,
		},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if child == nil {
				return asToolFail(call.ID, "sub-agent is nil"), nil
			}
			depth := asToolDepth(ctx)
			if depth >= cfg.maxDepth {
				return asToolFail(call.ID, fmt.Sprintf("max AsTool nesting depth %d exceeded", cfg.maxDepth)), nil
			}
			prompt, err := promptFromInput(call.Input)
			if err != nil {
				return asToolFail(call.ID, err.Error()), nil
			}
			if prompt == "" {
				return asToolFail(call.ID, "prompt is required"), nil
			}

			ctx = withAsToolDepth(ctx, depth+1)
			res, err := child.Run(ctx, run.Input{Text: prompt})
			if err != nil {
				return asToolFail(call.ID, err.Error()), nil
			}
			if res != nil && (res.StopReason == run.StopInterrupted || res.StopReason == run.StopError) {
				text := lastAssistantText(res)
				if text == "" {
					text = string(res.StopReason)
				}
				return asToolFail(call.ID, text), nil
			}
			text := lastAssistantText(res)
			if text == "" {
				text = "(empty sub-agent response)"
			}
			return tool.Result{
				ToolUseID: call.ID,
				OK:        true,
				Content:   []message.Part{message.Text{Text: text}},
			}, nil
		},
	}
}

func asToolDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	n, _ := ctx.Value(asToolDepthKey{}).(int)
	return n
}

func withAsToolDepth(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, asToolDepthKey{}, n)
}

func promptFromInput(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("invalid tool input: %w", err)
	}
	if v, ok := obj[defaultAsToolPromptKey]; ok {
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("prompt must be a string")
		}
		return s, nil
	}
	return "", nil
}

func lastAssistantText(res *run.Result) string {
	if res == nil {
		return ""
	}
	for i := len(res.Messages) - 1; i >= 0; i-- {
		m := res.Messages[i]
		if m.Role != message.RoleAssistant {
			continue
		}
		if t := message.TextContent(m); t != "" {
			return t
		}
	}
	return ""
}

func asToolFail(id, text string) tool.Result {
	return tool.Result{
		ToolUseID: id,
		OK:        false,
		Content:   []message.Part{message.Text{Text: text}},
	}
}
