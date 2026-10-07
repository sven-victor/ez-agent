package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

type promptEchoProvider struct{}

func (promptEchoProvider) Name() string { return "prompt-echo" }
func (promptEchoProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (p promptEchoProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	text := "empty"
	if n := len(req.Messages); n > 0 {
		if t := message.TextContent(req.Messages[n-1]); t != "" {
			text = t
		}
	}
	res := model.Result{
		Message:    message.NewAssistantText("child:" + text),
		StopReason: run.StopEndTurn,
	}
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason})
	}
	return res, nil
}
func (p promptEchoProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return p.Stream(ctx, req, nil)
}

type scriptedParentProvider struct {
	calls int
}

func (p *scriptedParentProvider) Name() string { return "parent-scripted" }
func (p *scriptedParentProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (p *scriptedParentProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	p.calls++
	var res model.Result
	if p.calls == 1 {
		res = model.Result{
			Message: message.Message{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.ToolUse{ID: "c1", Name: "researcher", Input: []byte(`{"prompt":"find X"}`)},
				},
			},
			StopReason: run.StopToolUse,
		}
	} else {
		res = model.Result{
			Message:    message.NewAssistantText("parent-done"),
			StopReason: run.StopEndTurn,
		}
	}
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason})
	}
	return res, nil
}
func (p *scriptedParentProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return p.Stream(ctx, req, nil)
}

func TestAsToolDelegatesPrompt(t *testing.T) {
	child, err := agent.New(agent.WithProvider(promptEchoProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := agent.New(
		agent.WithProvider(&scriptedParentProvider{}),
		agent.WithTools(agent.AsTool(child, agent.ToolMeta{
			Name:        "researcher",
			Description: "research helper",
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := parent.Run(context.Background(), run.Input{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	var sawChild bool
	for _, m := range res.Messages {
		if m.Role != message.RoleUser {
			continue
		}
		for _, p := range m.Parts {
			tr, ok := p.(message.ToolResult)
			if !ok || !tr.OK {
				continue
			}
			if strings.Contains(message.TextContent(message.Message{Parts: tr.Parts}), "child:find X") {
				sawChild = true
			}
		}
	}
	if !sawChild {
		t.Fatalf("expected child tool result, messages=%#v", res.Messages)
	}
}

func TestAsToolMaxDepth(t *testing.T) {
	leaf, err := agent.New(agent.WithProvider(promptEchoProvider{}))
	if err != nil {
		t.Fatal(err)
	}

	var failText string
	h := hook.NewRegistry()
	h.OnAfterTool(func(ctx context.Context, e *hook.AfterTool) error {
		if e.Result.OK {
			return nil
		}
		failText = message.TextContent(message.Message{Parts: e.Result.Content})
		return nil
	})

	mid, err := agent.New(
		agent.WithProvider(&alwaysToolProvider{name: "leaf"}),
		agent.WithHooks(h),
		agent.WithTools(agent.AsTool(leaf, agent.ToolMeta{Name: "leaf"}, agent.WithAsToolMaxDepth(1))),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Depth 0: mid.Run → leaf handler sees 0 < 1 → allowed.
	res, err := mid.Run(context.Background(), run.Input{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("mid stop=%s", res.StopReason)
	}
	if failText != "" {
		t.Fatalf("depth-0 leaf tool should succeed, got %q", failText)
	}

	// AsTool(mid) increments depth before mid.Run, so leaf's handler sees 1 >= 1.
	mid2, err := agent.New(
		agent.WithProvider(&alwaysToolProvider{name: "leaf"}),
		agent.WithHooks(h),
		agent.WithTools(agent.AsTool(leaf, agent.ToolMeta{Name: "leaf"}, agent.WithAsToolMaxDepth(1))),
	)
	if err != nil {
		t.Fatal(err)
	}
	failText = ""
	wrap := agent.AsTool(mid2, agent.ToolMeta{Name: "mid"}, agent.WithAsToolMaxDepth(1))
	out, err := wrap.Handler(context.Background(), tool.Call{
		ID:    "t1",
		Name:  "mid",
		Input: []byte(`{"prompt":"go"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("outer AsTool should succeed: %+v", out)
	}
	if !strings.Contains(failText, "nesting depth") {
		t.Fatalf("expected nested depth error, got %q", failText)
	}
}

func TestAsToolMissingPrompt(t *testing.T) {
	child, err := agent.New(agent.WithProvider(echoProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	reg := agent.AsTool(child, agent.ToolMeta{Name: "x"})
	res, err := reg.Handler(context.Background(), tool.Call{ID: "c", Name: "x", Input: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected failure")
	}
}

func TestAsToolNilChild(t *testing.T) {
	reg := agent.AsTool(nil, agent.ToolMeta{Name: "x"})
	res, err := reg.Handler(context.Background(), tool.Call{ID: "c", Name: "x", Input: []byte(`{"prompt":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected failure")
	}
}

type alwaysToolProvider struct {
	name  string
	calls int
}

func (p *alwaysToolProvider) Name() string { return "always-tool" }
func (p *alwaysToolProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (p *alwaysToolProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	p.calls++
	var res model.Result
	if p.calls == 1 {
		res = model.Result{
			Message: message.Message{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.ToolUse{ID: "n1", Name: p.name, Input: []byte(`{"prompt":"from-mid"}`)},
				},
			},
			StopReason: run.StopToolUse,
		}
	} else {
		res = model.Result{
			Message:    message.NewAssistantText("mid-done"),
			StopReason: run.StopEndTurn,
		}
	}
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason})
	}
	return res, nil
}
func (p *alwaysToolProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return p.Stream(ctx, req, nil)
}
