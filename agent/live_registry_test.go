package agent_test

import (
	"context"
	"testing"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

type captureProvider struct {
	lastTools []tool.Spec
}

func (p *captureProvider) Name() string { return "capture" }
func (p *captureProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (p *captureProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	p.lastTools = append([]tool.Spec(nil), req.Tools...)
	res := model.Result{Message: message.NewAssistantText("ok"), StopReason: run.StopEndTurn}
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason})
	}
	return res, nil
}
func (p *captureProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return p.Stream(ctx, req, nil)
}

type liveReg struct {
	specs []tool.Spec
}

func (r *liveReg) Specs() []tool.Spec { return r.specs }
func (r *liveReg) Lookup(name string) (tool.Handler, bool) {
	for _, s := range r.specs {
		if s.Name == name {
			return func(ctx context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{ToolUseID: call.ID, OK: true}, nil
			}, true
		}
	}
	return nil, false
}

func TestToolRegistryLiveRefresh(t *testing.T) {
	p := &captureProvider{}
	dyn := &liveReg{}
	a, err := agent.New(agent.WithProvider(p), agent.WithToolRegistry(dyn))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), run.Input{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.lastTools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(p.lastTools))
	}
	dyn.specs = []tool.Spec{{Name: "mcp_tool", InputSchema: []byte(`{}`)}}
	_, err = a.Run(context.Background(), run.Input{Text: "hi again"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.lastTools) != 1 || p.lastTools[0].Name != "mcp_tool" {
		t.Fatalf("tools=%v", p.lastTools)
	}
}
