package memory

import (
	"context"
	"fmt"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

// ContextPolicy manages the message window sent to the model.
type ContextPolicy interface {
	Prepare(ctx context.Context, st *run.State) error
	Reduce(ctx context.Context, st *run.State) error
}

// Noop leaves messages unchanged; Reduce always errors.
type Noop struct{}

func (Noop) Prepare(context.Context, *run.State) error { return nil }

func (Noop) Reduce(context.Context, *run.State) error {
	return fmt.Errorf("noop context policy cannot reduce")
}

// Window keeps the most recent MaxMessages messages, deleting tool pairs as whole groups.
type Window struct {
	MaxMessages int
}

func (w *Window) Prepare(_ context.Context, st *run.State) error {
	return w.trim(st)
}

func (w *Window) Reduce(_ context.Context, st *run.State) error {
	before := len(st.Messages)
	if err := w.trim(st); err != nil {
		return err
	}
	if len(st.Messages) >= before {
		// Force drop at least one group from the front.
		if len(st.Messages) == 0 {
			return fmt.Errorf("window reduce: no messages to reduce")
		}
		st.Messages = dropOldestGroup(st.Messages)
		if len(st.Messages) >= before {
			return fmt.Errorf("window reduce: failed to reduce context")
		}
	}
	return nil
}

func (w *Window) trim(st *run.State) error {
	if w == nil || w.MaxMessages <= 0 || len(st.Messages) <= w.MaxMessages {
		return nil
	}
	msgs := st.Messages
	for len(msgs) > w.MaxMessages {
		msgs = dropOldestGroup(msgs)
	}
	st.Messages = msgs
	return nil
}

// dropOldestGroup removes the oldest message, and if it is an assistant tool-use,
// also removes the following user tool-result message when present.
func dropOldestGroup(msgs []message.Message) []message.Message {
	if len(msgs) == 0 {
		return msgs
	}
	if message.HasToolUse(msgs[0]) && len(msgs) > 1 {
		if isToolResultMessage(msgs[1]) {
			return msgs[2:]
		}
	}
	// If oldest is a dangling tool-result after a prior trim, drop it alone.
	return msgs[1:]
}

func isToolResultMessage(m message.Message) bool {
	if len(m.Parts) == 0 {
		return false
	}
	for _, p := range m.Parts {
		if _, ok := p.(message.ToolResult); !ok {
			return false
		}
	}
	return true
}

// Ensure Noop and Window implement ContextPolicy.
var (
	_ ContextPolicy = Noop{}
	_ ContextPolicy = (*Window)(nil)
)
