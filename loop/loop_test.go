package loop_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/loop"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

type scriptedProvider struct {
	calls int
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
	p.calls++
	n := p.calls
	res, err := p.fn(n, req)
	if err != nil {
		return model.Result{}, err
	}
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason, Usage: res.Usage})
	}
	return res, nil
}
func (p *scriptedProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return p.Stream(ctx, req, nil)
}

func newState(msgs ...message.Message) *run.State {
	st := &run.State{Values: map[string]any{}}
	st.InitHistory(msgs)
	return st
}

func TestNoToolsSingleModelCall(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message:    message.NewAssistantText("hello"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	l := loop.New(loop.Config{Provider: p})
	st := newState(message.NewUserText("hi"))
	res, err := l.Run(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("messages=%d", len(res.Messages))
	}
}

func TestOneToolRoundTrip(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return model.Result{
				Message: message.Message{
					Role: message.RoleAssistant,
					Parts: []message.Part{
						message.ToolUse{ID: "call1", Name: "echo", Input: []byte(`{"text":"x"}`)},
					},
				},
				StopReason: run.StopToolUse,
			}, nil
		}
		return model.Result{
			Message:    message.NewAssistantText("done"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	reg := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "echo", Description: "echo", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ToolUseID: call.ID,
				OK:        true,
				Content:   []message.Part{message.Text{Text: "x"}},
			}, nil
		},
	})
	l := loop.New(loop.Config{Provider: p, Tools: reg})
	st := newState(message.NewUserText("hi"))
	res, err := l.Run(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatalf("calls=%d", p.calls)
	}
	if res.StopReason == run.StopToolUse {
		t.Fatal("final stop must not be tool_use")
	}
	// user, assistant tooluse, user toolresult, assistant text
	if len(res.Messages) != 4 {
		t.Fatalf("messages=%d %#v", len(res.Messages), res.Messages)
	}
	uses := message.ToolUses(res.Messages[1])
	if len(uses) != 1 {
		t.Fatal("expected tool use")
	}
	if !hasToolResult(res.Messages[2], "call1") {
		t.Fatal("expected paired tool result")
	}
}

func TestMaxTurnsStopsAfterTools(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message: message.Message{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.ToolUse{ID: "c1", Name: "echo", Input: []byte(`{}`)},
				},
			},
			StopReason: run.StopToolUse,
		}, nil
	}}
	reg := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "echo", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	})
	l := loop.New(loop.Config{
		Provider: p,
		Tools:    reg,
		Limits:   run.Limits{MaxTurns: 1},
	})
	st := newState(message.NewUserText("hi"))
	res, err := l.Run(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopMaxTurns {
		t.Fatalf("got %s", res.StopReason)
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 model call, got %d", p.calls)
	}
	// tools executed and paired
	if !hasToolResult(res.Messages[len(res.Messages)-1], "c1") {
		t.Fatal("expected tool result before stop")
	}
}

func TestCancelNoDanglingToolUse(t *testing.T) {
	block := make(chan struct{})
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		<-block
		return model.Result{}, context.Canceled
	}}
	ctx, cancel := context.WithCancel(context.Background())
	l := loop.New(loop.Config{Provider: p})
	st := newState(message.NewUserText("hi"))

	// Cancel before model returns; also append an unpaired tool use mid-flight via race-free path:
	// Simulate cancel during model by canceling ctx while stream waits.
	done := make(chan struct{})
	var res *run.Result
	var err error
	go func() {
		defer close(done)
		res, err = l.Run(ctx, st, nil)
	}()
	cancel()
	close(block)
	<-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if res == nil || res.StopReason != run.StopCanceled {
		t.Fatalf("res=%v", res)
	}
	assertNoDanglingToolUse(t, res.Messages)
}

func TestOverflowReduceAndRetry(t *testing.T) {
	var reduceCalls atomic.Int32
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return model.Result{}, model.ErrOverflow
		}
		return model.Result{
			Message:    message.NewAssistantText("ok"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	policy := &countingPolicy{inner: &memory.Window{MaxMessages: 10}, reduce: &reduceCalls}
	st := newState(
		message.NewUserText("a"),
		message.NewAssistantText("b"),
		message.NewUserText("c"),
	)
	l := loop.New(loop.Config{Provider: p, Policy: policy})
	res, err := l.Run(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reduceCalls.Load() < 1 {
		t.Fatal("expected Reduce call")
	}
	if p.calls < 2 {
		t.Fatalf("expected retry, calls=%d", p.calls)
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

func hasToolResult(m message.Message, id string) bool {
	for _, p := range m.Parts {
		if tr, ok := p.(message.ToolResult); ok && tr.ToolUseID == id {
			return true
		}
	}
	return false
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

func TestHookAfterModelRetry(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message:    message.NewAssistantText("x"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	h := hook.NewRegistry()
	var retries int
	h.OnAfterModel(func(ctx context.Context, e *hook.AfterModel) error {
		if retries == 0 {
			retries++
			e.Retry = true
		}
		return nil
	})
	l := loop.New(loop.Config{Provider: p, Hooks: h})
	st := newState(message.NewUserText("hi"))
	res, err := l.Run(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatalf("calls=%d", p.calls)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatal(res.StopReason)
	}
}

func TestHookAfterModelOnProviderError(t *testing.T) {
	boom := errors.New("provider down")
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{}, boom
	}}
	h := hook.NewRegistry()
	var afterErr error
	var afterCalls int
	h.OnAfterModel(func(ctx context.Context, e *hook.AfterModel) error {
		afterCalls++
		afterErr = e.Err
		e.Retry = true // ignored on failure
		return nil
	})
	l := loop.New(loop.Config{Provider: p, Hooks: h, MaxModelRetry: 1})
	st := newState(message.NewUserText("hi"))
	_, err := l.Run(context.Background(), st, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if afterCalls != 1 {
		t.Fatalf("AfterModel calls=%d", afterCalls)
	}
	if !errors.Is(afterErr, boom) {
		t.Fatalf("AfterModel.Err=%v", afterErr)
	}
	if p.calls != 1 {
		t.Fatalf("provider should not retry from AfterModel.Retry on failure: calls=%d", p.calls)
	}
}

func TestPreferComplete(t *testing.T) {
	var streamed bool
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message:    message.NewAssistantText("ok"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	// Wrap to detect Stream vs Complete: override by composing — scriptedProvider.Complete
	// calls Stream with nil yield. PreferComplete should still work end-to-end.
	l := loop.New(loop.Config{Provider: p, PreferComplete: true})
	st := newState(message.NewUserText("hi"))
	var finished bool
	res, err := l.Run(context.Background(), st, func(ev event.Event) error {
		switch ev.(type) {
		case event.ModelDelta:
			streamed = true
		case event.ModelFinished:
			finished = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamed {
		t.Fatal("PreferComplete should not emit ModelDelta")
	}
	if !finished {
		t.Fatal("expected ModelFinished")
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatal(res.StopReason)
	}
	if p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
}
