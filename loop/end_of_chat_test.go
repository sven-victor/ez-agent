package loop_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/loop"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func endOfChatProvider() *scriptedProvider {
	return &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message: message.Message{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.ToolUse{ID: "c1", Name: "bye", Input: []byte(`{}`)},
				},
			},
			StopReason: run.StopToolUse,
		}, nil
	}}
}

func byeTool(handler tool.Handler) tool.Registry {
	return tool.NewRegistry(tool.Registration{
		Spec:    tool.Spec{Name: "bye", InputSchema: []byte(`{}`)},
		Handler: handler,
	})
}

func endOfChatHandler(text string) tool.Handler {
	return func(ctx context.Context, call tool.Call) (tool.Result, error) {
		return tool.Result{
			ToolUseID: call.ID,
			OK:        true,
			Content:   []message.Part{message.Text{Text: text}},
			Err:       tool.ErrEndOfChat,
		}, tool.ErrEndOfChat
	}
}

func TestEndOfChatStopsWithoutToolResult(t *testing.T) {
	p := endOfChatProvider()
	l := loop.New(loop.Config{
		Provider: p,
		Tools:    byeTool(endOfChatHandler("observer only")),
		// MaxTurns would stop the next cycle; end_of_chat must win first.
		Limits: run.Limits{MaxTurns: 1},
	})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
	if res.StopReason != run.StopEndOfChat {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if countToolResults(res.Messages) != 0 {
		t.Fatalf("tool results leaked: %#v", res.Messages)
	}
	for _, m := range res.Messages {
		if m.Role == message.RoleAssistant {
			t.Fatalf("tool-only assistant message kept: %#v", res.Messages)
		}
	}
	if len(res.Messages) != 1 || message.TextContent(res.Messages[0]) != "hi" {
		t.Fatalf("messages=%#v", res.Messages)
	}
}

func TestEndOfChatZeroResult(t *testing.T) {
	p := endOfChatProvider()
	var finished tool.Result
	l := loop.New(loop.Config{
		Provider: p,
		Tools: byeTool(func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{}, tool.ErrEndOfChat
		}),
	})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), func(ev event.Event) error {
		if tf, ok := ev.(event.ToolFinished); ok {
			finished = tf.Result
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndOfChat || p.calls != 1 {
		t.Fatalf("stop=%s calls=%d", res.StopReason, p.calls)
	}
	if !finished.OK || len(finished.Content) != 0 || !errors.Is(finished.Err, tool.ErrEndOfChat) {
		t.Fatalf("finished=%+v", finished)
	}
	if countToolResults(res.Messages) != 0 || message.HasToolUse(res.Messages[len(res.Messages)-1]) {
		t.Fatalf("history=%#v", res.Messages)
	}
}

func TestEndOfChatKeepsAssistantText(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message: message.Message{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.Text{Text: "bye now"},
					message.ToolUse{ID: "c1", Name: "bye", Input: []byte(`{}`)},
				},
			},
			StopReason: run.StopToolUse,
		}, nil
	}}
	l := loop.New(loop.Config{Provider: p, Tools: byeTool(endOfChatHandler("local"))})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndOfChat {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("messages=%d %#v", len(res.Messages), res.Messages)
	}
	last := res.Messages[1]
	if message.TextContent(last) != "bye now" {
		t.Fatalf("text=%q", message.TextContent(last))
	}
	if message.HasToolUse(last) {
		t.Fatal("tool use kept")
	}
	if countToolResults(res.Messages) != 0 {
		t.Fatal("tool result written")
	}
}

func TestEndOfChatEventOrder(t *testing.T) {
	p := endOfChatProvider()
	l := loop.New(loop.Config{Provider: p, Tools: byeTool(endOfChatHandler("observer only"))})
	var evs []event.Event
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), func(ev event.Event) error {
		evs = append(evs, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndOfChat {
		t.Fatalf("stop=%s", res.StopReason)
	}
	var sawStart, sawFinish, sawRun bool
	for _, ev := range evs {
		switch tf := ev.(type) {
		case event.RunError:
			t.Fatalf("RunError: %v", tf.Err)
		case event.Cancelled:
			t.Fatal("Cancelled")
		case event.ToolStarted:
			if sawFinish || sawRun {
				t.Fatal("ToolStarted out of order")
			}
			sawStart = true
		case event.ToolFinished:
			if !sawStart || sawRun {
				t.Fatal("ToolFinished out of order")
			}
			if message.TextContent(message.Message{Parts: tf.Result.Content}) != "observer only" {
				t.Fatalf("content=%v", tf.Result.Content)
			}
			sawFinish = true
		case event.RunFinished:
			if !sawFinish {
				t.Fatal("RunFinished before ToolFinished")
			}
			if tf.Result == nil || tf.Result.StopReason != run.StopEndOfChat {
				t.Fatalf("finished=%v", tf.Result)
			}
			sawRun = true
		}
	}
	if !sawStart || !sawFinish || !sawRun {
		t.Fatalf("events missing: start=%v finish=%v run=%v", sawStart, sawFinish, sawRun)
	}
}

func TestEndOfChatMixedBatchDropsAllResults(t *testing.T) {
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message: message.Message{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.ToolUse{ID: "a", Name: "echo", Input: []byte(`{}`)},
					message.ToolUse{ID: "b", Name: "bye", Input: []byte(`{}`)},
				},
			},
			StopReason: run.StopToolUse,
		}, nil
	}}
	var echoRan atomic.Bool
	reg := tool.NewRegistry(
		tool.Registration{
			Spec: tool.Spec{Name: "echo", InputSchema: []byte(`{}`)},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				select {
				case <-ctx.Done():
					return tool.Result{}, ctx.Err()
				case <-time.After(30 * time.Millisecond):
				}
				echoRan.Store(true)
				return tool.Result{
					ToolUseID: call.ID,
					OK:        true,
					Content:   []message.Part{message.Text{Text: "echoed"}},
				}, nil
			},
		},
		tool.Registration{
			Spec:    tool.Spec{Name: "bye", InputSchema: []byte(`{}`)},
			Handler: endOfChatHandler("stop"),
		},
	)
	l := loop.New(loop.Config{Provider: p, Tools: reg})
	var finished []string
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), func(ev event.Event) error {
		if tf, ok := ev.(event.ToolFinished); ok {
			finished = append(finished, tf.Call.Name)
		}
		if _, ok := ev.(event.RunError); ok {
			t.Fatal("RunError")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
	if res.StopReason != run.StopEndOfChat {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if !echoRan.Load() {
		t.Fatal("sibling tool did not finish")
	}
	if len(finished) != 2 || finished[0] != "echo" || finished[1] != "bye" {
		t.Fatalf("finished=%v", finished)
	}
	if countToolResults(res.Messages) != 0 {
		t.Fatalf("results leaked: %#v", res.Messages)
	}
	for _, m := range res.Messages {
		if message.HasToolUse(m) {
			t.Fatalf("tool use kept: %#v", m)
		}
	}
}

func TestEndOfChatSessionHasNoDanglingToolUse(t *testing.T) {
	p := endOfChatProvider()
	store := memory.NewMemStore()
	const sid = "sess-end"
	l := loop.New(loop.Config{
		Provider:     p,
		Tools:        byeTool(endOfChatHandler("local")),
		SessionStore: store,
		SessionID:    sid,
	})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndOfChat {
		t.Fatalf("stop=%s", res.StopReason)
	}
	sess, err := store.Load(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	assertNoDanglingToolUse(t, sess.Messages)
	if countToolResults(sess.Messages) != 0 {
		t.Fatalf("session tool results: %#v", sess.Messages)
	}
	assertNoDanglingToolUse(t, res.Messages)
	if countToolResults(res.Messages) != 0 {
		t.Fatal("result tool results")
	}
}

func TestEndOfChatOnResume(t *testing.T) {
	p := endOfChatProvider()
	l := loop.New(loop.Config{Provider: p, Tools: byeTool(endOfChatHandler("local"))})
	st := newState(
		message.NewUserText("hi"),
		message.Message{
			Role: message.RoleAssistant,
			Parts: []message.Part{
				message.ToolUse{ID: "c1", Name: "bye", Input: []byte(`{}`)},
			},
		},
	)
	st.Pending = &run.PendingTools{Calls: []run.PendingCall{{
		ID: "c1", Name: "bye", Input: []byte(`{}`),
	}}}
	res, err := l.Run(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatalf("model calls=%d", p.calls)
	}
	if res.StopReason != run.StopEndOfChat {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if res.Pending != nil {
		t.Fatalf("pending=%v", res.Pending)
	}
	if countToolResults(res.Messages) != 0 {
		t.Fatal("tool result written")
	}
	for _, m := range res.Messages {
		if message.HasToolUse(m) {
			t.Fatal("tool use kept")
		}
	}
}

func TestInterruptBeforeEndOfChat(t *testing.T) {
	var ran atomic.Bool
	p := endOfChatProvider()
	l := loop.New(loop.Config{
		Provider: p,
		Tools: byeTool(func(ctx context.Context, call tool.Call) (tool.Result, error) {
			ran.Store(true)
			return tool.Result{}, tool.ErrEndOfChat
		}),
		Interrupter: loop.FuncInterrupter(func(ctx context.Context, st *run.State, calls []tool.Call) (bool, error) {
			return true, nil
		}),
	})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ran.Load() {
		t.Fatal("tool ran despite interrupt")
	}
	if res.StopReason != run.StopInterrupted {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if res.Pending == nil || len(res.Pending.Calls) != 1 {
		t.Fatalf("pending=%v", res.Pending)
	}
}

func TestEndOfChatHookCannotClearSignal(t *testing.T) {
	h := hook.NewRegistry()
	h.OnAfterTool(func(ctx context.Context, e *hook.AfterTool) error {
		e.ReplaceResult = &tool.Result{
			ToolUseID: e.Call.ID,
			OK:        true,
			Content:   []message.Part{message.Text{Text: "from hook"}},
		}
		return nil
	})
	p := endOfChatProvider()
	l := loop.New(loop.Config{Provider: p, Tools: byeTool(endOfChatHandler("local")), Hooks: h})
	var content string
	var ferr error
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), func(ev event.Event) error {
		if tf, ok := ev.(event.ToolFinished); ok {
			content = message.TextContent(message.Message{Parts: tf.Result.Content})
			ferr = tf.Result.Err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndOfChat {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if content != "from hook" {
		t.Fatalf("content=%q", content)
	}
	if !errors.Is(ferr, tool.ErrEndOfChat) {
		t.Fatalf("err=%v", ferr)
	}
	if countToolResults(res.Messages) != 0 {
		t.Fatal("tool result written")
	}
}

func countToolResults(msgs []message.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Parts {
			if _, ok := p.(message.ToolResult); ok {
				n++
			}
		}
	}
	return n
}
