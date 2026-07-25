package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
)

// ProviderSummarizer uses a model.Provider directly (no Agent loop) to summarize.
type ProviderSummarizer struct {
	Provider model.Provider
	System   string
	// Logger receives summarizer diagnostics; nil is a no-op.
	Logger logger.Logger
}

// Summarize asks the provider to compress msgs into a single user message.
func (p *ProviderSummarizer) Summarize(ctx context.Context, msgs []message.Message) (message.Message, run.Usage, error) {
	if p == nil || p.Provider == nil {
		return message.Message{}, run.Usage{}, fmt.Errorf("provider summarizer: provider is required")
	}
	log := policyLog(p.Logger)
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
	log.Debug("provider summarizer start", "messages", len(msgs))
	res, err := p.Provider.Complete(ctx, req)
	if err != nil {
		log.Error("provider summarizer failed", "err", err)
		return message.Message{}, run.Usage{}, err
	}
	text := strings.TrimSpace(message.TextContent(res.Message))
	if text == "" {
		err := fmt.Errorf("provider summarizer: empty summary")
		log.Error("provider summarizer failed", "err", err)
		return message.Message{}, res.Usage, err
	}
	log.Info("provider summarizer ok", "messages", len(msgs), "summary_bytes", len(text), "usage_total", res.Usage.TotalTokens)
	return message.Message{
		ID:    message.NewID(),
		Role:  message.RoleUser,
		Parts: []message.Part{message.Text{Text: "[Conversation summary]\n" + text}},
	}, res.Usage, nil
}
