package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/plugin"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

// scriptedProvider returns scripted model results by call index (1-based).
type scriptedProvider struct {
	calls atomic.Int32
	fn    func(n int, req model.Request) (model.Result, error)
	caps  model.Capabilities
}

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Caps() model.Capabilities {
	if p.caps.TextIn || p.caps.Tools {
		return p.caps
	}
	return model.Capabilities{TextIn: true, ImageIn: true, Tools: true, Streaming: true}
}
func (p *scriptedProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	n := int(p.calls.Add(1))
	res, err := p.fn(n, req)
	if err != nil {
		return model.Result{}, err
	}
	if yield != nil {
		if text := message.TextContent(res.Message); text != "" {
			_ = yield(event.ModelDelta{TextDelta: text})
		}
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason, Usage: res.Usage})
	}
	return res, nil
}
func (p *scriptedProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return p.Stream(ctx, req, nil)
}

func toolUseResult(id, name string, input json.RawMessage) model.Result {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	return model.Result{
		Message: message.Message{
			Role: message.RoleAssistant,
			Parts: []message.Part{
				message.ToolUse{ID: id, Name: name, Input: input},
			},
		},
		StopReason: run.StopToolUse,
		Usage:      run.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}
}

func textResult(s string) model.Result {
	return model.Result{
		Message:    message.NewAssistantText(s),
		StopReason: run.StopEndTurn,
		Usage:      run.Usage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12},
	}
}

func echoTool() tool.Registration {
	return tool.Registration{
		Spec: tool.Spec{
			Name:        "echo",
			Description: "Echo input",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var in struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(call.Input, &in)
			if in.Text == "" {
				in.Text = string(call.Input)
			}
			return tool.Result{
				ToolUseID: call.ID,
				OK:        true,
				Content:   []message.Part{message.Text{Text: in.Text}},
			}, nil
		},
	}
}

func assertNoDanglingToolUse(t *testing.T, msgs []message.Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch v := p.(type) {
			case message.ToolUse:
				pending[v.ID] = true
			case message.ToolResult:
				delete(pending, v.ToolUseID)
			}
		}
	}
	if len(pending) > 0 {
		t.Fatalf("dangling tool uses: %v", pending)
	}
}

func TestAgentPlainTextLoop(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n != 1 {
			t.Fatalf("unexpected call %d", n)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("messages=%d", len(req.Messages))
		}
		return textResult("hello"), nil
	}}
	a, err := agent.New(agent.WithProvider(p), agent.WithSystem("sys"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d", p.calls.Load())
	}
	if len(res.Messages) != 2 {
		t.Fatalf("messages=%d", len(res.Messages))
	}
	if message.TextContent(res.Messages[1]) != "hello" {
		t.Fatalf("got %q", message.TextContent(res.Messages[1]))
	}
}

func TestAgentToolRoundTrip(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		switch n {
		case 1:
			return toolUseResult("c1", "echo", json.RawMessage(`{"text":"ping"}`)), nil
		case 2:
			// Second call must see the tool result.
			found := false
			for _, m := range req.Messages {
				for _, part := range m.Parts {
					if tr, ok := part.(message.ToolResult); ok && tr.ToolUseID == "c1" {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("second model call missing tool result")
			}
			return textResult("pong"), nil
		default:
			return model.Result{}, fmt.Errorf("unexpected call %d", n)
		}
	}}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithTools(echoTool()),
		agent.WithLimits(run.Limits{MaxTurns: 5}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "use echo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("calls=%d", p.calls.Load())
	}
	// user + assistant(tool) + user(result) + assistant(text)
	if len(res.Messages) != 4 {
		t.Fatalf("messages=%d", len(res.Messages))
	}
	assertNoDanglingToolUse(t, res.Messages)
	if res.StopReason == run.StopToolUse {
		t.Fatal("final stop must not be tool_use")
	}
}

func TestAgentMultiToolParallelInOneTurn(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return model.Result{
				Message: message.Message{
					Role: message.RoleAssistant,
					Parts: []message.Part{
						message.ToolUse{ID: "a", Name: "echo", Input: []byte(`{"text":"A"}`)},
						message.ToolUse{ID: "b", Name: "echo", Input: []byte(`{"text":"B"}`)},
					},
				},
				StopReason: run.StopToolUse,
			}, nil
		}
		return textResult("done"), nil
	}}
	var started atomic.Int32
	reg := tool.Registration{
		Spec: tool.Spec{Name: "echo", InputSchema: json.RawMessage(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			started.Add(1)
			time.Sleep(30 * time.Millisecond)
			return tool.Result{
				ToolUseID: call.ID,
				OK:        true,
				Content:   []message.Part{message.Text{Text: string(call.Input)}},
			}, nil
		},
	}
	a, err := agent.New(agent.WithProvider(p), agent.WithTools(reg))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "both"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Load() != 2 {
		t.Fatalf("started=%d", started.Load())
	}
	assertNoDanglingToolUse(t, res.Messages)
	// Both results should be in the same tool-result message.
	var ids []string
	for _, m := range res.Messages {
		for _, part := range m.Parts {
			if tr, ok := part.(message.ToolResult); ok {
				ids = append(ids, tr.ToolUseID)
			}
		}
	}
	if len(ids) != 2 {
		t.Fatalf("tool results=%v", ids)
	}
}

func TestAgentUnknownToolStillPairs(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return toolUseResult("u1", "missing_tool", nil), nil
		}
		return textResult("recovered"), nil
	}}
	a, err := agent.New(agent.WithProvider(p), agent.WithTools(echoTool()))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "call missing"})
	if err != nil {
		t.Fatal(err)
	}
	assertNoDanglingToolUse(t, res.Messages)
	found := false
	for _, m := range res.Messages {
		for _, part := range m.Parts {
			if tr, ok := part.(message.ToolResult); ok && tr.ToolUseID == "u1" {
				found = true
				if tr.OK {
					t.Fatal("unknown tool should be OK=false")
				}
			}
		}
	}
	if !found {
		t.Fatal("expected error tool result")
	}
}

func TestAgentMaxTurnsStopsAfterToolCycle(t *testing.T) {
	// MaxTurns=1: first cycle may call tools; after Turn++ the next cycle top stops.
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return toolUseResult(fmt.Sprintf("c%d", n), "echo", json.RawMessage(`{"text":"x"}`)), nil
	}}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithTools(echoTool()),
		agent.WithLimits(run.Limits{MaxTurns: 1}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "loop"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopMaxTurns {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("expected 1 model call, got %d", p.calls.Load())
	}
	assertNoDanglingToolUse(t, res.Messages)
}

func TestAgentMaxTurnsZeroMeansUnlimited(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n < 3 {
			return toolUseResult(fmt.Sprintf("c%d", n), "echo", json.RawMessage(`{"text":"x"}`)), nil
		}
		return textResult("done"), nil
	}}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithTools(echoTool()),
		agent.WithLimits(run.Limits{MaxTurns: 0}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if p.calls.Load() != 3 {
		t.Fatalf("calls=%d", p.calls.Load())
	}
}

func TestAgentMaxTotalTokens(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message:    message.NewAssistantText("chunk"),
			StopReason: run.StopEndTurn,
			Usage:      run.Usage{InputTokens: 40, OutputTokens: 10, TotalTokens: 50},
		}, nil
	}}
	// First call accumulates 50 tokens and ends with end_turn — MaxTotalTokens is
	// checked at cycle top, so a single end_turn run finishes normally.
	// Force a tool cycle so the limit is checked on the next cycle.
	p.fn = func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			r := toolUseResult("t1", "echo", json.RawMessage(`{"text":"x"}`))
			r.Usage = run.Usage{InputTokens: 40, OutputTokens: 10, TotalTokens: 50}
			return r, nil
		}
		t.Fatal("second model call should not happen")
		return model.Result{}, nil
	}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithTools(echoTool()),
		agent.WithLimits(run.Limits{MaxTotalTokens: 50}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "tokens"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopMaxTokens {
		t.Fatalf("stop=%s usage=%+v", res.StopReason, res.Usage)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d", p.calls.Load())
	}
	assertNoDanglingToolUse(t, res.Messages)
}

func TestAgentLoopMultiTurnToolsThenAnswer(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		switch n {
		case 1:
			return toolUseResult("s1", "echo", json.RawMessage(`{"text":"step1"}`)), nil
		case 2:
			return toolUseResult("s2", "echo", json.RawMessage(`{"text":"step2"}`)), nil
		case 3:
			return textResult("final answer"), nil
		default:
			return model.Result{}, fmt.Errorf("extra call %d", n)
		}
	}}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithTools(echoTool()),
		agent.WithLimits(run.Limits{MaxTurns: 10}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "multi"})
	if err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 3 {
		t.Fatalf("calls=%d", p.calls.Load())
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	// user + (asst+result)*2 + asst = 6
	if len(res.Messages) != 6 {
		t.Fatalf("messages=%d", len(res.Messages))
	}
	assertNoDanglingToolUse(t, res.Messages)
}

func TestAgentMemoryWindowPrepare(t *testing.T) {
	var seenLens []int
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		seenLens = append(seenLens, len(req.Messages))
		return textResult("ok"), nil
	}}
	// Seed history longer than the window.
	history := []message.Message{
		message.NewUserText("m1"),
		message.NewAssistantText("a1"),
		message.NewUserText("m2"),
		message.NewAssistantText("a2"),
		message.NewUserText("m3"),
	}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithContextPolicy(&memory.Window{MaxMessages: 3}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	if len(seenLens) != 1 || seenLens[0] > 3 {
		t.Fatalf("model saw %v messages; want <=3", seenLens)
	}
	// Result returns full Transcript (window trim only affects model Messages).
	if len(res.Messages) != 6 { // 5 history + assistant
		t.Fatalf("result messages=%d want 6 (full transcript)", len(res.Messages))
	}
}

func TestAgentSessionAppendAndReplaceAllAfterWindow(t *testing.T) {
	store := memory.NewMemStore()
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return textResult("ok"), nil
	}}
	history := []message.Message{
		message.NewUserText("m1"),
		message.NewAssistantText("a1"),
		message.NewUserText("m2"),
		message.NewAssistantText("a2"),
		message.NewUserText("m3"),
	}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithContextPolicy(&memory.Window{MaxMessages: 3}),
		agent.WithSession("win-sess", store),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), run.Input{Messages: history}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), "win-sess")
	if err != nil {
		t.Fatal(err)
	}
	// Effective window after Prepare trim + assistant reply should be bounded.
	if len(got.Messages) > 4 {
		t.Fatalf("session window too large: %d msgs %#v", len(got.Messages), got.Messages)
	}
	if message.TextContent(got.Messages[len(got.Messages)-1]) != "ok" {
		t.Fatalf("last=%q", message.TextContent(got.Messages[len(got.Messages)-1]))
	}
	ids := map[string]bool{}
	for _, m := range got.Messages {
		if m.ID == "" {
			t.Fatal("session message missing ID")
		}
		if ids[m.ID] {
			t.Fatalf("duplicate message ID %q", m.ID)
		}
		ids[m.ID] = true
	}

	// Second run loads the trimmed window, not the original 5+ history.
	p2 := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if len(req.Messages) > 5 {
			t.Fatalf("loaded too much history into model: %d", len(req.Messages))
		}
		return textResult("again"), nil
	}}
	a2, err := agent.New(
		agent.WithProvider(p2),
		agent.WithContextPolicy(&memory.Window{MaxMessages: 3}),
		agent.WithSession("win-sess", store),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Run(context.Background(), run.Input{Text: "next"}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentMemoryWindowPreservesToolPairs(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		assertNoDanglingToolUse(t, req.Messages)
		return textResult("ok"), nil
	}}
	history := []message.Message{
		message.NewUserText("start"),
		{
			Role: message.RoleAssistant,
			Parts: []message.Part{
				message.ToolUse{ID: "old", Name: "echo", Input: []byte(`{}`)},
			},
		},
		message.NewToolResultMessage(message.ToolResult{
			ToolUseID: "old", OK: true, Parts: []message.Part{message.Text{Text: "r"}},
		}),
		message.NewUserText("later"),
		message.NewAssistantText("mid"),
		message.NewUserText("now"),
	}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithContextPolicy(&memory.Window{MaxMessages: 3}),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	assertNoDanglingToolUse(t, res.Messages)
}

func TestAgentMemoryOverflowReduceAndRetry(t *testing.T) {
	var reduceCalls atomic.Int32
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return model.Result{}, model.ErrOverflow
		}
		return textResult("after reduce"), nil
	}}
	inner := &memory.Window{MaxMessages: 100}
	policy := &countingPolicy{inner: inner, reduce: &reduceCalls}
	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithContextPolicy(policy),
	)
	if err != nil {
		t.Fatal(err)
	}
	stMsgs := []message.Message{
		message.NewUserText("a"),
		message.NewAssistantText("b"),
		message.NewUserText("c"),
	}
	res, err := a.Run(context.Background(), run.Input{Messages: stMsgs})
	if err != nil {
		t.Fatal(err)
	}
	if reduceCalls.Load() < 1 {
		t.Fatal("expected Reduce")
	}
	if p.calls.Load() < 2 {
		t.Fatalf("expected retry, calls=%d", p.calls.Load())
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
}

type countingPolicy struct {
	inner  memory.ContextPolicy
	reduce *atomic.Int32
}

func (c *countingPolicy) Prepare(ctx context.Context, st *run.State) error {
	return c.inner.Prepare(ctx, st)
}
func (c *countingPolicy) Reduce(ctx context.Context, st *run.State) error {
	c.reduce.Add(1)
	return c.inner.Reduce(ctx, st)
}

func TestAgentCancelViaContext(t *testing.T) {
	p := &blockingProvider{entered: make(chan struct{})}
	a, err := agent.New(agent.WithProvider(p))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var res *run.Result
	var runErr error
	go func() {
		defer close(done)
		res, runErr = a.Run(ctx, run.Input{Text: "hi"})
	}()
	// Wait until the provider is inside Stream, then cancel.
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not enter Stream")
	}
	cancel()
	<-done
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("err=%v", runErr)
	}
	if res == nil || res.StopReason != run.StopCanceled {
		t.Fatalf("res=%v", res)
	}
	assertNoDanglingToolUse(t, res.Messages)
}

// blockingProvider blocks inside Stream until ctx is canceled.
type blockingProvider struct {
	entered chan struct{}
	once    atomic.Bool
}

func (p *blockingProvider) Name() string { return "blocking" }
func (p *blockingProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (p *blockingProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	if p.once.CompareAndSwap(false, true) {
		close(p.entered)
	}
	<-ctx.Done()
	return model.Result{}, ctx.Err()
}
func (p *blockingProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return p.Stream(ctx, req, nil)
}

func TestAgentHooksLifecycle(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return toolUseResult("h1", "echo", json.RawMessage(`{"text":"x"}`)), nil
		}
		return textResult("ok"), nil
	}}
	h := hook.NewRegistry()
	var order []string
	h.OnBeforeRun(func(ctx context.Context, e *hook.BeforeRun) error {
		order = append(order, "before_run")
		return nil
	})
	h.OnAfterRun(func(ctx context.Context, e *hook.AfterRun) error {
		order = append(order, "after_run")
		return nil
	})
	h.OnBeforeModel(func(ctx context.Context, e *hook.BeforeModel) error {
		order = append(order, "before_model")
		return nil
	})
	h.OnAfterModel(func(ctx context.Context, e *hook.AfterModel) error {
		order = append(order, "after_model")
		return nil
	})
	h.OnBeforeTool(func(ctx context.Context, e *hook.BeforeTool) error {
		order = append(order, "before_tool")
		return nil
	})
	h.OnAfterTool(func(ctx context.Context, e *hook.AfterTool) error {
		order = append(order, "after_tool")
		return nil
	})

	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithTools(echoTool()),
		agent.WithHooks(h),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), run.Input{Text: "hooks"})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{"before_run", "before_model", "after_model", "before_tool", "after_tool"}
	if len(order) < len(wantPrefix)+1 {
		t.Fatalf("order=%v", order)
	}
	for i, w := range wantPrefix {
		if order[i] != w {
			t.Fatalf("order[%d]=%s want %s full=%v", i, order[i], w, order)
		}
	}
	if order[len(order)-1] != "after_run" {
		t.Fatalf("last=%s full=%v", order[len(order)-1], order)
	}
}

func TestAgentHookDenyTool(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return toolUseResult("d1", "echo", json.RawMessage(`{"text":"secret"}`)), nil
		}
		return textResult("denied path"), nil
	}}
	h := hook.NewRegistry()
	h.OnBeforeTool(func(ctx context.Context, e *hook.BeforeTool) error {
		e.Deny = true
		e.Reason = "blocked by policy"
		return nil
	})
	var handlerRan bool
	reg := tool.Registration{
		Spec: tool.Spec{Name: "echo", InputSchema: json.RawMessage(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			handlerRan = true
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	}
	a, err := agent.New(agent.WithProvider(p), agent.WithTools(reg), agent.WithHooks(h))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "deny"})
	if err != nil {
		t.Fatal(err)
	}
	if handlerRan {
		t.Fatal("handler should not run when denied")
	}
	assertNoDanglingToolUse(t, res.Messages)
	found := false
	for _, m := range res.Messages {
		for _, part := range m.Parts {
			if tr, ok := part.(message.ToolResult); ok && tr.ToolUseID == "d1" {
				found = true
				if tr.OK {
					t.Fatal("denied tool should be OK=false")
				}
				if message.TextContent(message.Message{Parts: tr.Parts}) != "blocked by policy" {
					t.Fatalf("reason=%q", message.TextContent(message.Message{Parts: tr.Parts}))
				}
			}
		}
	}
	if !found {
		t.Fatal("expected deny tool result")
	}
}

func TestAgentRunStreamEvents(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return toolUseResult("e1", "echo", json.RawMessage(`{"text":"x"}`)), nil
		}
		return textResult("streamed"), nil
	}}
	a, err := agent.New(agent.WithProvider(p), agent.WithTools(echoTool()))
	if err != nil {
		t.Fatal(err)
	}
	var (
		started, finished, toolStart, toolFinish, modelFin int
		gotResult                                          *run.Result
	)
	for ev := range a.RunStream(context.Background(), run.Input{Text: "stream"}) {
		switch e := ev.(type) {
		case event.RunStarted:
			started++
		case event.RunFinished:
			finished++
			gotResult = e.Result
		case event.ToolStarted:
			toolStart++
		case event.ToolFinished:
			toolFinish++
		case event.ModelFinished:
			modelFin++
		case event.RunError:
			t.Fatalf("run error: %v", e.Err)
		}
	}
	if started != 1 || finished != 1 {
		t.Fatalf("started=%d finished=%d", started, finished)
	}
	if toolStart != 1 || toolFinish != 1 {
		t.Fatalf("tool start=%d finish=%d", toolStart, toolFinish)
	}
	if modelFin < 2 {
		t.Fatalf("model finished=%d", modelFin)
	}
	if gotResult == nil || gotResult.StopReason != run.StopEndTurn {
		t.Fatalf("result=%v", gotResult)
	}
}

func TestAgentPluginRegistersTool(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return toolUseResult("p1", "from_plugin", json.RawMessage(`{}`)), nil
		}
		return textResult("ok"), nil
	}}
	pl := &toolPlugin{}
	a, err := agent.New(agent.WithProvider(p), agent.WithPlugins(pl))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), run.Input{Text: "plugin"})
	if err != nil {
		t.Fatal(err)
	}
	if !pl.called {
		t.Fatal("plugin tool not called")
	}
	assertNoDanglingToolUse(t, res.Messages)
}

type toolPlugin struct{ called bool }

func (p *toolPlugin) Name() string { return "tool_plugin" }
func (p *toolPlugin) Install(h *plugin.Host) error {
	h.RegisterTool(tool.Registration{
		Spec: tool.Spec{Name: "from_plugin", InputSchema: json.RawMessage(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			p.called = true
			return tool.Result{ToolUseID: call.ID, OK: true, Content: []message.Part{message.Text{Text: "plugin"}}}, nil
		},
	})
	return nil
}

func TestAgentInputMessagesAndText(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if len(req.Messages) != 2 {
			t.Fatalf("want 2 messages (Text+Messages), got %d", len(req.Messages))
		}
		return textResult("ok"), nil
	}}
	a, err := agent.New(agent.WithProvider(p))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), run.Input{
		Text:     "first",
		Messages: []message.Message{message.NewUserText("second")},
	})
	if err != nil {
		t.Fatal(err)
	}
}
