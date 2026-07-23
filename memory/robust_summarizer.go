package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
)

const (
	defaultRobustChunkBytes     = 20 * 1024
	defaultRobustMaxChunks      = 20
	defaultRobustMaxInputTokens = 6000
	defaultRobustMergeBytes     = 20 * 1024
	defaultRobustTruncateBytes  = 8 * 1024
	defaultIntentBytes          = 4000
)

// RobustSummarizer wraps a Provider with retry, map-reduce chunking, and
// deterministic truncate fallback when LLM summarization fails.
type RobustSummarizer struct {
	Provider model.Provider
	System   string
	Retry    model.RetryPolicy // nil → ExponentialBackoff for ErrThrottled

	// MaxInputTokens is the coarse budget for a one-shot summary request (0 = default).
	MaxInputTokens int
	// ChunkBytes splits oversized input for map-reduce (0 = default 20KiB).
	ChunkBytes int
	// MaxChunks caps map-reduce parts (0 = default 20).
	MaxChunks int
	// MergeThreshold: if joined partials exceed this, attempt a merge Complete (0 = default).
	MergeThreshold int
	// TruncateBytes for the final deterministic fallback (0 = default 8KiB).
	TruncateBytes int
	// DisableTruncate disables the last-resort truncated join (default: truncate allowed).
	DisableTruncate bool
	// OmitLatestIntent skips injecting the latest user intent into prompts.
	OmitLatestIntent bool
	// Logger receives summarizer diagnostics; nil is a no-op.
	Logger logger.Logger
}

// Summarize compresses msgs into a single user summary message.
func (r *RobustSummarizer) Summarize(ctx context.Context, msgs []message.Message) (message.Message, error) {
	if r == nil || r.Provider == nil {
		return message.Message{}, fmt.Errorf("robust summarizer: provider is required")
	}
	log := policyLog(r.Logger)
	if len(msgs) == 0 {
		return message.Message{}, fmt.Errorf("robust summarizer: no messages")
	}

	intent := ""
	if !r.OmitLatestIntent {
		intent = lastUserText(msgs, defaultIntentBytes)
	}

	log.Debug("robust summarizer one-shot", "messages", len(msgs), "est_tokens", EstimateTokens(msgs))
	if text, err := r.oneShot(ctx, msgs, intent); err == nil {
		log.Info("robust summarizer one-shot ok", "messages", len(msgs))
		return summaryMessage("[Conversation summary]\n" + text), nil
	} else if !shouldChunkSummarize(err) && EstimateTokens(msgs) <= r.maxInputTokens() {
		log.Warn("robust summarizer one-shot failed", "err", err)
		// Permanent non-overflow failure on a small payload: still try truncate if allowed.
		if !r.DisableTruncate {
			log.Warn("robust summarizer using truncate fallback")
			return r.truncateFallback(msgs), nil
		}
		return message.Message{}, err
	} else {
		log.Warn("robust summarizer one-shot overflow/chunk path", "err", err)
	}

	log.Info("robust summarizer map-reduce", "messages", len(msgs))
	if text, err := r.mapReduce(ctx, msgs, intent); err == nil && strings.TrimSpace(text) != "" {
		log.Info("robust summarizer map-reduce ok")
		return summaryMessage("[Conversation summary]\n" + text), nil
	} else if err != nil {
		log.Warn("robust summarizer map-reduce failed", "err", err)
	}

	if !r.DisableTruncate {
		log.Warn("robust summarizer using truncate fallback after map-reduce")
		return r.truncateFallback(msgs), nil
	}
	return message.Message{}, fmt.Errorf("robust summarizer: all strategies failed")
}

func (r *RobustSummarizer) maxInputTokens() int {
	if r.MaxInputTokens > 0 {
		return r.MaxInputTokens
	}
	return defaultRobustMaxInputTokens
}

func (r *RobustSummarizer) chunkBytes() int {
	if r.ChunkBytes > 0 {
		return r.ChunkBytes
	}
	return defaultRobustChunkBytes
}

func (r *RobustSummarizer) maxChunks() int {
	if r.MaxChunks > 0 {
		return r.MaxChunks
	}
	return defaultRobustMaxChunks
}

func (r *RobustSummarizer) mergeThreshold() int {
	if r.MergeThreshold > 0 {
		return r.MergeThreshold
	}
	return defaultRobustMergeBytes
}

func (r *RobustSummarizer) truncateBytes() int {
	if r.TruncateBytes > 0 {
		return r.TruncateBytes
	}
	return defaultRobustTruncateBytes
}

func (r *RobustSummarizer) systemPrompt() string {
	if r.System != "" {
		return r.System
	}
	return "You summarize conversation history for an AI agent. Preserve key facts, decisions, tool outcomes, and open tasks. Be concise."
}

func (r *RobustSummarizer) oneShot(ctx context.Context, msgs []message.Message, intent string) (string, error) {
	if EstimateTokens(msgs) > r.maxInputTokens() {
		return "", model.ErrOverflow
	}
	return r.completeSummary(ctx, msgs, intent, "Please summarize this conversation.")
}

func (r *RobustSummarizer) mapReduce(ctx context.Context, msgs []message.Message, intent string) (string, error) {
	log := policyLog(r.Logger)
	batches := splitMessagesForSummary(msgs, r.chunkBytes(), r.maxChunks())
	if len(batches) == 0 {
		return "", fmt.Errorf("robust summarizer: empty batches")
	}
	log.Debug("robust summarizer map-reduce batches", "batches", len(batches))
	var parts []string
	for i, batch := range batches {
		prompt := fmt.Sprintf("This is part %d/%d of a longer conversation. Summarize this part concisely.", i+1, len(batches))
		text, err := r.completeSummary(ctx, batch, intent, prompt)
		if err != nil || strings.TrimSpace(text) == "" {
			log.Warn("robust summarizer part failed; using truncate snippet", "part", i+1, "of", len(batches), "err", err)
			fallback := truncateUTF8ByBytes(flattenMessagesText(batch), 1800)
			parts = append(parts, fallback+"\n...[part not summarized]")
			continue
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	merged := strings.Join(parts, "\n\n--- part ---\n\n")
	if len(merged) <= r.mergeThreshold() {
		return merged, nil
	}
	log.Debug("robust summarizer merging partials", "bytes", len(merged))
	mergeMsgs := []message.Message{message.NewUserText(
		fmt.Sprintf("Merge these partial conversation summaries into one concise summary. Remove redundancy.\n\n%s", merged),
	)}
	text, err := r.completeSummary(ctx, mergeMsgs, intent, "")
	if err == nil && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text), nil
	}
	if err != nil {
		log.Warn("robust summarizer merge failed; returning joined parts", "err", err)
	}
	return merged, nil
}

func (r *RobustSummarizer) truncateFallback(msgs []message.Message) message.Message {
	raw := flattenMessagesText(msgs)
	body := truncateUTF8ByBytes(raw, r.truncateBytes())
	return summaryMessage("[Conversation summary — truncated]\n" + body + defaultCondenseNotice)
}

func (r *RobustSummarizer) completeSummary(ctx context.Context, msgs []message.Message, intent, userPrompt string) (string, error) {
	reqMsgs := append([]message.Message(nil), msgs...)
	if intent != "" {
		reqMsgs = append([]message.Message{message.NewUserText(
			"**Context (latest user message, for relevance):**\n" + intent,
		)}, reqMsgs...)
	}
	if userPrompt != "" {
		reqMsgs = append(reqMsgs, message.NewUserText(userPrompt))
	}
	req := model.Request{
		Messages: reqMsgs,
		System:   model.System{Text: r.systemPrompt()},
	}

	var lastErr error
	attempt := 0
	for {
		res, err := r.Provider.Complete(ctx, req)
		if err == nil {
			text := strings.TrimSpace(message.TextContent(res.Message))
			if text == "" {
				return "", fmt.Errorf("robust summarizer: empty summary")
			}
			return text, nil
		}
		lastErr = err
		attempt++
		policy := r.Retry
		if policy == nil {
			policy = model.ExponentialBackoff{}
		}
		wait, ok := policy.ShouldRetry(attempt, err)
		if !ok {
			return "", lastErr
		}
		policyLog(r.Logger).Warn("robust summarizer retry", "attempt", attempt, "wait", wait, "err", err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
}

func shouldChunkSummarize(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, model.ErrOverflow) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context") ||
		strings.Contains(msg, "token") && (strings.Contains(msg, "limit") || strings.Contains(msg, "length")) ||
		strings.Contains(msg, "too long") ||
		strings.Contains(msg, "maximum context")
}

// splitMessagesForSummary packs messages into batches under approx maxBytes,
// keeping tool-use / tool-result pairs together. Oversized single messages are
// flattened and split by UTF-8 bytes.
func splitMessagesForSummary(msgs []message.Message, maxBytes, maxChunks int) [][]message.Message {
	if maxBytes <= 0 {
		maxBytes = defaultRobustChunkBytes
	}
	if maxChunks <= 0 {
		maxChunks = defaultRobustMaxChunks
	}
	var batches [][]message.Message
	var cur []message.Message
	curBytes := 0

	flush := func() {
		if len(cur) == 0 {
			return
		}
		batches = append(batches, cur)
		cur = nil
		curBytes = 0
	}

	i := 0
	for i < len(msgs) && len(batches) < maxChunks {
		// Take tool pair when possible.
		group := []message.Message{msgs[i]}
		groupBytes := len(flattenMessageText(msgs[i]))
		if message.HasToolUse(msgs[i]) && i+1 < len(msgs) && isToolResultMessage(msgs[i+1]) {
			group = append(group, msgs[i+1])
			groupBytes += len(flattenMessageText(msgs[i+1]))
			i += 2
		} else {
			i++
		}

		if groupBytes > maxBytes && len(group) == 1 {
			flush()
			// Split single oversized message into text chunks as synthetic user messages.
			flat := flattenMessagesText(group)
			for _, piece := range splitUTF8Chunks(flat, maxBytes) {
				if len(batches) >= maxChunks {
					return batches
				}
				batches = append(batches, []message.Message{message.NewUserText(piece)})
			}
			continue
		}

		if len(cur) > 0 && curBytes+groupBytes > maxBytes {
			flush()
			if len(batches) >= maxChunks {
				return batches
			}
		}
		cur = append(cur, group...)
		curBytes += groupBytes
	}
	if len(batches) < maxChunks {
		flush()
	}
	return batches
}

func splitUTF8Chunks(s string, maxBytes int) []string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		if len(s) <= maxBytes {
			return append(out, s)
		}
		piece := truncateUTF8ByBytes(s, maxBytes)
		out = append(out, piece)
		s = s[len(piece):]
	}
	return out
}

var _ Summarizer = (*RobustSummarizer)(nil)
