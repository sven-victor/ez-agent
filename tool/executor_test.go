package tool_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/tool"
)

type countingHooks struct {
	beforeErr error
	afterN    atomic.Int32
	lastOK    atomic.Bool
}

func (h *countingHooks) BeforeTool(ctx context.Context, call tool.Call) (tool.BeforeAction, error) {
	return tool.BeforeAction{}, h.beforeErr
}

func (h *countingHooks) AfterTool(ctx context.Context, call tool.Call, result tool.Result) (tool.Result, error) {
	h.afterN.Add(1)
	h.lastOK.Store(result.OK)
	return result, nil
}

func TestUnknownToolDoesNotAbortBatch(t *testing.T) {
	reg := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "ok", Description: "ok", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ToolUseID: call.ID, OK: true, Content: []message.Part{message.Text{Text: "yes"}}}, nil
		},
	})
	exec := &tool.Concurrent{Registry: reg}
	results, err := exec.Execute(context.Background(), []tool.Call{
		{ID: "1", Name: "missing", Input: []byte(`{}`)},
		{ID: "2", Name: "ok", Input: []byte(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results", len(results))
	}
	if results[0].OK {
		t.Fatal("expected unknown tool OK=false")
	}
	if !results[1].OK {
		t.Fatal("expected ok tool success")
	}
}

func TestConcurrentOneFailureOthersSucceed(t *testing.T) {
	reg := tool.NewRegistry(
		tool.Registration{
			Spec: tool.Spec{Name: "fail", InputSchema: []byte(`{}`)},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{}, context.DeadlineExceeded
			},
		},
		tool.Registration{
			Spec: tool.Spec{Name: "ok", InputSchema: []byte(`{}`)},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{ToolUseID: call.ID, OK: true}, nil
			},
		},
	)
	exec := &tool.Concurrent{Registry: reg}
	results, err := exec.Execute(context.Background(), []tool.Call{
		{ID: "1", Name: "fail"},
		{ID: "2", Name: "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].OK {
		t.Fatal("expected fail")
	}
	if !results[1].OK {
		t.Fatal("expected ok")
	}
}

func TestAfterToolOnHandlerError(t *testing.T) {
	h := &countingHooks{}
	reg := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "fail", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{}, errors.New("boom")
		},
	})
	exec := &tool.Sequential{Registry: reg, Hooks: h}
	results, err := exec.Execute(context.Background(), []tool.Call{{ID: "1", Name: "fail"}})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].OK {
		t.Fatal("expected OK=false")
	}
	if h.afterN.Load() != 1 {
		t.Fatalf("AfterTool calls=%d", h.afterN.Load())
	}
}

func TestAfterToolSkippedWhenBeforeToolFails(t *testing.T) {
	h := &countingHooks{beforeErr: errors.New("before failed")}
	reg := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "ok", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	})
	exec := &tool.Sequential{Registry: reg, Hooks: h}
	results, err := exec.Execute(context.Background(), []tool.Call{{ID: "1", Name: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].OK {
		t.Fatal("expected OK=false when BeforeTool fails")
	}
	if h.afterN.Load() != 0 {
		t.Fatalf("AfterTool must not run when BeforeTool fails: calls=%d", h.afterN.Load())
	}
}

func TestAfterToolOnUnknownTool(t *testing.T) {
	h := &countingHooks{}
	exec := &tool.Sequential{Registry: tool.NewRegistry(), Hooks: h}
	results, err := exec.Execute(context.Background(), []tool.Call{{ID: "1", Name: "missing"}})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].OK {
		t.Fatal("expected OK=false")
	}
	if h.afterN.Load() != 1 {
		t.Fatalf("AfterTool calls=%d", h.afterN.Load())
	}
}

func TestCancelFillsResults(t *testing.T) {
	var started atomic.Int32
	reg := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "slow", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			started.Add(1)
			select {
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			case <-time.After(2 * time.Second):
				return tool.Result{ToolUseID: call.ID, OK: true}, nil
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	exec := &tool.Concurrent{Registry: reg}
	done := make(chan []tool.Result, 1)
	go func() {
		res, _ := exec.Execute(ctx, []tool.Call{{ID: "1", Name: "slow"}, {ID: "2", Name: "slow"}})
		done <- res
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	results := <-done
	if len(results) != 2 {
		t.Fatalf("got %d", len(results))
	}
	for _, r := range results {
		if r.ToolUseID == "" {
			t.Fatal("empty tool use id")
		}
		if r.OK {
			t.Fatal("expected not OK on cancel")
		}
	}
}
