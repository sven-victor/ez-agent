package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/plugin"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

type echoProvider struct{}

func (echoProvider) Name() string { return "echo" }
func (echoProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (echoProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	res := model.Result{
		Message:    message.NewAssistantText("ok"),
		StopReason: run.StopEndTurn,
	}
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason})
	}
	return res, nil
}
func (echoProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return echoProvider{}.Stream(ctx, req, nil)
}

type badPlugin struct{}

func (badPlugin) Name() string { return "bad" }
func (badPlugin) Install(*plugin.Host) error {
	return context.Canceled
}

func TestNewRequiresProvider(t *testing.T) {
	_, err := agent.New()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPluginInstallFailure(t *testing.T) {
	_, err := agent.New(agent.WithProvider(echoProvider{}), agent.WithPlugins(badPlugin{}))
	if err == nil {
		t.Fatal("expected plugin error")
	}
}

func TestRunAndStream(t *testing.T) {
	a, err := agent.New(
		agent.WithProvider(echoProvider{}),
		agent.WithSystem("sys"),
		agent.WithTools(tool.Registration{
			Spec: tool.Spec{Name: "noop", InputSchema: []byte(`{}`)},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{ToolUseID: call.ID, OK: true}, nil
			},
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatal(res.StopReason)
	}

	ch := a.RunStream(context.Background(), run.Input{Text: "hi"})
	var finished bool
	for ev := range ch {
		if _, ok := ev.(event.RunFinished); ok {
			finished = true
		}
	}
	if !finished {
		t.Fatal("expected RunFinished")
	}
}

func TestWithLogger(t *testing.T) {
	var msgs []string
	log := logger.Func{
		InfoFunc: func(msg string, _ ...any) {
			msgs = append(msgs, msg)
		},
	}
	a, err := agent.New(
		agent.WithProvider(echoProvider{}),
		agent.WithLogger(log),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatal(res.StopReason)
	}
	joined := strings.Join(msgs, "|")
	for _, want := range []string{"agent created", "run started", "model finished", "run finished"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in logs: %v", want, msgs)
		}
	}
}

func TestWithLoggerInjectsContextPolicy(t *testing.T) {
	log := logger.Nop()
	pol := &memory.Summarize{
		Summarizer:           &memory.ProviderSummarizer{Provider: echoProvider{}},
		KeepRecent:           2,
		ProactiveMaxMessages: 100,
	}
	_, err := agent.New(
		agent.WithProvider(echoProvider{}),
		agent.WithContextPolicy(pol),
		agent.WithLogger(log),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pol.Logger == nil {
		t.Fatal("expected SetLogger to fill Summarize.Logger")
	}
	if ps, ok := pol.Summarizer.(*memory.ProviderSummarizer); !ok || ps.Logger == nil {
		t.Fatal("expected SetLogger to fill ProviderSummarizer.Logger")
	}
}
