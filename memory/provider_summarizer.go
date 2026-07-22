package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
)

// ProviderSummarizer uses a model.Provider directly (no Agent loop) to summarize.
type ProviderSummarizer struct {
	Provider model.Provider
	System   string
}

// Summarize asks the provider to compress msgs into a single user message.
func (p *ProviderSummarizer) Summarize(ctx context.Context, msgs []message.Message) (message.Message, error) {
	if p == nil || p.Provider == nil {
		return message.Message{}, fmt.Errorf("provider summarizer: provider is required")
	}
	sys := p.System
	if sys == "" {
		sys = "You summarize conversation history for an AI agent. Preserve key facts, decisions, tool outcomes, and open tasks. Be concise."
	}
	reqMsgs := append([]message.Message(nil), msgs...)
	reqMsgs = append(reqMsgs, message.NewUserText("Please summarize this conversation."))

	req := model.Request{
		Messages: reqMsgs,
		System:   model.System{Text: sys},
	}
	res, err := p.Provider.Complete(ctx, req)
	if err != nil {
		return message.Message{}, err
	}
	text := strings.TrimSpace(message.TextContent(res.Message))
	if text == "" {
		return message.Message{}, fmt.Errorf("provider summarizer: empty summary")
	}
	return message.Message{
		ID:    message.NewID(),
		Role:  message.RoleUser,
		Parts: []message.Part{message.Text{Text: "[Conversation summary]\n" + text}},
	}, nil
}
