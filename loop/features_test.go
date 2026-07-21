package loop_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/loop"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func TestMaxOutputTokensPassedToRequest(t *testing.T) {
	var saw int
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		saw = req.MaxTokens
		return model.Result{
			Message:    message.NewAssistantText("ok"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	l := loop.New(loop.Config{
		Provider: p,
		Limits:   run.Limits{MaxOutputTokens: 128},
	})
	_, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if saw != 128 {
		t.Fatalf("MaxTokens=%d", saw)
	}
}

func TestRetryPolicyOnThrottled(t *testing.T) {
	var calls atomic.Int32
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		calls.Add(1)
		if n < 3 {
			return model.Result{}, model.ErrThrottled
		}
		return model.Result{
			Message:    message.NewAssistantText("ok"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	l := loop.New(loop.Config{
		Provider: p,
		RetryPolicy: model.ExponentialBackoff{
			MaxAttempts: 5,
			Initial:     time.Millisecond,
			Max:         5 * time.Millisecond,
		},
	})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestHITLInterruptAndResume(t *testing.T) {
	var phase atomic.Int32
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if phase.Load() == 0 {
			return model.Result{
				Message: message.Message{
					Role: message.RoleAssistant,
					Parts: []message.Part{
						message.ToolUse{ID: "c1", Name: "echo", Input: []byte(`{}`)},
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
		Spec: tool.Spec{Name: "echo", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ToolUseID: call.ID,
				OK:        true,
				Content:   []message.Part{message.Text{Text: "echoed"}},
			}, nil
		},
	})
	l := loop.New(loop.Config{
		Provider: p,
		Tools:    reg,
		Interrupter: loop.FuncInterrupter(func(ctx context.Context, st *run.State, calls []tool.Call) (bool, error) {
			return phase.Load() == 0, nil
		}),
	})
	st := newState(message.NewUserText("hi"))
	res, err := l.Run(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopInterrupted {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if res.Pending == nil || len(res.Pending.Calls) != 1 {
		t.Fatalf("pending=%v", res.Pending)
	}
	phase.Store(1)
	st2 := run.StateFromResult(res)
	res2, err := l.Run(context.Background(), st2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.StopReason != run.StopEndTurn {
		t.Fatalf("resume stop=%s", res2.StopReason)
	}
	assertNoDanglingToolUse(t, res2.Messages)
}

func TestRetryPolicyGivesUp(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{}, model.ErrThrottled
	}}
	l := loop.New(loop.Config{
		Provider: p,
		RetryPolicy: model.ExponentialBackoff{
			MaxAttempts: 2,
			Initial:     time.Millisecond,
		},
	})
	_, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if !errors.Is(err, model.ErrThrottled) {
		t.Fatalf("err=%v", err)
	}
}
