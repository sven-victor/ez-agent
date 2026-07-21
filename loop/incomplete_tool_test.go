package loop_test

import (
	"context"
	"testing"

	"github.com/sven-victor/ez-agent/loop"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func TestSanitizeIncompleteToolUseJSON(t *testing.T) {
	var executed bool
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return model.Result{
				Message: message.Message{
					Role: message.RoleAssistant,
					Parts: []message.Part{
						message.ToolUse{ID: "c1", Name: "echo", Input: []byte(`{"q":"trunc`)},
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
			executed = true
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	})
	l := loop.New(loop.Config{Provider: p, Tools: reg})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("incomplete tool use must not execute")
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	found := false
	for _, m := range res.Messages {
		if message.TextContent(m) == "[incomplete tool call omitted]" {
			found = true
		}
		if len(message.ToolUses(m)) > 0 && m.Role == message.RoleAssistant {
			// incomplete use should have been stripped from the stored message
			for _, u := range message.ToolUses(m) {
				if u.ID == "c1" {
					t.Fatal("incomplete tool use still present")
				}
			}
		}
	}
	if !found {
		t.Fatal("expected omission note")
	}
}

func TestSanitizeIncompleteToolUseMaxTokens(t *testing.T) {
	var executed bool
	p := &scriptedProvider{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{
			Message: message.Message{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.Text{Text: "calling"},
					message.ToolUse{ID: "c1", Name: "echo", Input: []byte(`{"q":"ok"}`)},
				},
			},
			StopReason: run.StopMaxTokens,
		}, nil
	}}
	reg := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "echo", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			executed = true
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	})
	l := loop.New(loop.Config{Provider: p, Tools: reg})
	res, err := l.Run(context.Background(), newState(message.NewUserText("hi")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("max_tokens tool use must not execute")
	}
	if res.StopReason != run.StopMaxTokens {
		t.Fatalf("stop=%s", res.StopReason)
	}
}
