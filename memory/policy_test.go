package memory_test

import (
	"context"
	"testing"

	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

func TestWindowPreservesToolPairs(t *testing.T) {
	w := &memory.Window{MaxMessages: 2}
	st := &run.State{
		Messages: []message.Message{
			message.NewUserText("u1"),
			{
				Role: message.RoleAssistant,
				Parts: []message.Part{
					message.ToolUse{ID: "1", Name: "echo", Input: []byte(`{}`)},
				},
			},
			message.NewToolResultMessage(message.ToolResult{
				ToolUseID: "1", OK: true, Parts: []message.Part{message.Text{Text: "ok"}},
			}),
			message.NewUserText("u2"),
		},
	}
	if err := w.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) > 2 {
		t.Fatalf("expected <=2 messages, got %d", len(st.Messages))
	}
	// Ensure no dangling tool use without result.
	for i, m := range st.Messages {
		if message.HasToolUse(m) {
			if i+1 >= len(st.Messages) || !isToolResultMsg(st.Messages[i+1]) {
				t.Fatalf("unpaired tool use at index %d", i)
			}
		}
	}
}

func TestWindowReduceShrinks(t *testing.T) {
	w := &memory.Window{MaxMessages: 100}
	st := &run.State{
		Messages: []message.Message{
			message.NewUserText("a"),
			message.NewAssistantText("b"),
			message.NewUserText("c"),
		},
	}
	before := len(st.Messages)
	if err := w.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) >= before {
		t.Fatalf("expected shrink, before=%d after=%d", before, len(st.Messages))
	}
}

func TestNoopReduceErrors(t *testing.T) {
	err := memory.Noop{}.Reduce(context.Background(), &run.State{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func isToolResultMsg(m message.Message) bool {
	for _, p := range m.Parts {
		if _, ok := p.(message.ToolResult); !ok {
			return false
		}
	}
	return len(m.Parts) > 0
}
