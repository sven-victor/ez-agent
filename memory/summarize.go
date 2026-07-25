package memory

import (
	"context"
	"fmt"

	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

const (
	defaultSummaryRatio = 0.3
	defaultKeepRecent   = 10
	minSummaryRatio     = 0.1
	maxSummaryRatio     = 0.8
)

// Summarizer turns a message slice into a single summary message.
// Usage covers model call(s) made during summarization (zero when no LLM was used).
type Summarizer interface {
	Summarize(ctx context.Context, msgs []message.Message) (message.Message, run.Usage, error)
}

// Summarize replaces the oldest fraction of Messages with an LLM summary.
//
// Reduce always attempts summarization (reactive overflow recovery), then
// optional Segmented, then Fallback (default: drop oldest tool-aware group).
// When ProactiveMaxMessages / ProactiveMaxTokens thresholds are exceeded,
// Prepare also summarizes (best-effort: failures are ignored).
type Summarize struct {
	Inner      ContextPolicy // optional; run after summarization attempts
	Summarizer Summarizer
	// Segmented is an optional second-stage summarizer used on Reduce when
	// Summarizer fails (not used during Prepare).
	Segmented Summarizer

	Ratio                float64 // default 0.3, clamped to [0.1, 0.8]
	KeepRecent           int     // default 10
	ProactiveMaxMessages int     // 0 = disabled; Prepare when len > this
	ProactiveMaxTokens   int     // 0 = disabled; Prepare when EstimateTokens > this
	MaxMessageBytes      int     // 0 = off; condense oversized parts before summarize
	// OmitLatestIntent skips injecting the latest user message into summarizer input.
	OmitLatestIntent bool
	// Fallback runs when summarization fails or does not shrink the window.
	// nil → drop oldest tool-aware message group once.
	Fallback ContextPolicy
	// Logger receives summarization diagnostics; nil is a no-op.
	// agent.WithLogger also fills this via SetLogger when unset.
	Logger logger.Logger
	// Hooks receives BeforeSummary / AfterSummary around summarizer calls.
	// agent.New fills this via SetHooks when unset.
	Hooks *hook.Registry
}

func (s *Summarize) ratio() float64 {
	r := s.Ratio
	if r == 0 {
		r = defaultSummaryRatio
	}
	if r < minSummaryRatio {
		r = minSummaryRatio
	}
	if r > maxSummaryRatio {
		r = maxSummaryRatio
	}
	return r
}

func (s *Summarize) keepRecent() int {
	if s.KeepRecent <= 0 {
		return defaultKeepRecent
	}
	return s.KeepRecent
}

func (s *Summarize) needsProactive(st *run.State) bool {
	if s.ProactiveMaxMessages > 0 && len(st.Messages) > s.ProactiveMaxMessages {
		return true
	}
	if s.ProactiveMaxTokens > 0 && EstimateTokens(st.Messages) > s.ProactiveMaxTokens {
		return true
	}
	return false
}

func (s *Summarize) Prepare(ctx context.Context, st *run.State) error {
	log := policyLog(s.Logger)
	if s.MaxMessageBytes > 0 {
		before := len(st.Messages)
		st.Messages = CondenseOversizedParts(st.Messages, s.MaxMessageBytes)
		log.Debug("condensed oversized parts before prepare", "messages", before, "max_bytes", s.MaxMessageBytes)
	}
	if s.needsProactive(st) {
		before := len(st.Messages)
		est := EstimateTokens(st.Messages)
		log.Info("proactive summarize",
			"messages", before,
			"est_tokens", est,
			"max_messages", s.ProactiveMaxMessages,
			"max_tokens", s.ProactiveMaxTokens,
		)
		if err := s.summarizeOldest(ctx, st, false); err != nil {
			log.Warn("proactive summarize failed (ignored)", "err", err, "messages", before)
		} else {
			log.Info("proactive summarize done", "before", before, "after", len(st.Messages))
		}
	}
	if s.Inner != nil {
		return s.Inner.Prepare(ctx, st)
	}
	return nil
}

func (s *Summarize) Reduce(ctx context.Context, st *run.State) error {
	log := policyLog(s.Logger)
	before := len(st.Messages)
	log.Info("reactive summarize (overflow reduce)", "messages", before)
	if s.MaxMessageBytes > 0 {
		st.Messages = CondenseOversizedParts(st.Messages, s.MaxMessageBytes)
	}

	sumErr := s.summarizeOldest(ctx, st, true)
	if sumErr != nil {
		log.Warn("summarize reduce primary failed", "err", sumErr, "messages", len(st.Messages))
	} else if len(st.Messages) < before {
		log.Info("summarize reduce done", "before", before, "after", len(st.Messages))
	}
	if sumErr != nil || len(st.Messages) >= before {
		fbBefore := len(st.Messages)
		if ferr := s.applyFallback(ctx, st); ferr != nil && len(st.Messages) >= before {
			log.Error("summarize reduce fallback failed", "err", ferr, "sum_err", sumErr)
			if sumErr != nil {
				return fmt.Errorf("summarize reduce: %w (fallback: %v)", sumErr, ferr)
			}
			return ferr
		}
		if len(st.Messages) < fbBefore {
			log.Info("summarize reduce fallback applied", "before", fbBefore, "after", len(st.Messages))
		}
	}

	if s.Inner != nil {
		if err := s.Inner.Reduce(ctx, st); err != nil {
			if len(st.Messages) >= before {
				log.Error("summarize inner reduce failed", "err", err)
				return err
			}
			log.Warn("summarize inner reduce error after shrink (ignored)", "err", err)
		}
	}
	if len(st.Messages) >= before {
		err := fmt.Errorf("summarize reduce: failed to reduce context")
		log.Error("summarize reduce failed", "err", err, "messages", before)
		return err
	}
	return nil
}

func (s *Summarize) applyFallback(ctx context.Context, st *run.State) error {
	log := policyLog(s.Logger)
	if s.Fallback != nil {
		log.Debug("applying summarize fallback policy")
		return s.Fallback.Reduce(ctx, st)
	}
	before := len(st.Messages)
	if before == 0 {
		return fmt.Errorf("summarize fallback: no messages")
	}
	log.Debug("dropping oldest message group as fallback", "messages", before)
	st.Messages = dropOldestGroup(st.Messages)
	if len(st.Messages) >= before {
		return fmt.Errorf("summarize fallback: failed to drop messages")
	}
	return nil
}

func (s *Summarize) summarizeOldest(ctx context.Context, st *run.State, allowSegmented bool) error {
	log := policyLog(s.Logger)
	if s.Summarizer == nil {
		return fmt.Errorf("summarize: Summarizer is required")
	}
	keep := s.keepRecent()
	n := len(st.Messages)
	if n <= keep {
		return fmt.Errorf("summarize: insufficient messages (have %d, keep %d)", n, keep)
	}

	count := max(1, int(float64(n)*s.ratio()))
	if count > n-keep {
		count = n - keep
	}
	count = adjustSplitPointForToolPairs(st.Messages, count)
	if count <= 0 {
		return fmt.Errorf("summarize: cannot split without breaking tool pairs")
	}

	toSummarize := st.Messages[:count]
	remaining := st.Messages[count:]
	input := s.withIntent(st.Messages, toSummarize)
	// allowSegmented is only true on Reduce (overflow); Prepare uses false.
	reactive := allowSegmented

	if err := s.fireBeforeSummary(ctx, st, &input, reactive); err != nil {
		return err
	}

	log.Info("summarizing oldest messages",
		"total", n,
		"summarize", count,
		"keep", len(remaining),
		"allow_segmented", allowSegmented,
		"input", len(input),
	)

	summary, usage, err := s.Summarizer.Summarize(ctx, input)
	if err != nil && allowSegmented && s.Segmented != nil {
		log.Warn("primary summarizer failed; trying segmented", "err", err)
		var segUsage run.Usage
		summary, segUsage, err = s.Segmented.Summarize(ctx, input)
		usage.Add(segUsage)
	}
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}
	if summary.Role == "" {
		summary.Role = message.RoleUser
	}
	message.EnsureID(&summary)
	st.Messages = append([]message.Message{summary}, remaining...)
	st.Usage.Add(usage)
	log.Debug("summary message applied",
		"remaining", len(remaining),
		"summary_id", summary.ID,
		"usage_total", usage.TotalTokens,
		"state_usage_total", st.Usage.TotalTokens,
	)

	if err := s.fireAfterSummary(ctx, st, summary, input, reactive, n, usage); err != nil {
		return err
	}
	return nil
}

func (s *Summarize) fireBeforeSummary(ctx context.Context, st *run.State, input *[]message.Message, reactive bool) error {
	if s.Hooks == nil {
		return nil
	}
	e := &hook.BeforeSummary{
		State:    st,
		Input:    append([]message.Message(nil), (*input)...),
		Reactive: reactive,
	}
	if err := s.Hooks.FireBeforeSummary(ctx, e); err != nil {
		return err
	}
	if e.Skip {
		return fmt.Errorf("summarize: skipped by BeforeSummary hook")
	}
	if e.Input != nil {
		*input = e.Input
	}
	return nil
}

func (s *Summarize) fireAfterSummary(ctx context.Context, st *run.State, summary message.Message, input []message.Message, reactive bool, beforeCount int, usage run.Usage) error {
	if s.Hooks == nil {
		return nil
	}
	e := &hook.AfterSummary{
		State:       st,
		Summary:     summary,
		Input:       input,
		Reactive:    reactive,
		BeforeCount: beforeCount,
		AfterCount:  len(st.Messages),
		Usage:       usage,
	}
	if err := s.Hooks.FireAfterSummary(ctx, e); err != nil {
		return err
	}
	if e.ReplaceSummary != nil {
		rs := *e.ReplaceSummary
		if rs.Role == "" {
			rs.Role = message.RoleUser
		}
		message.EnsureID(&rs)
		if len(st.Messages) > 0 {
			st.Messages[0] = rs
		}
	}
	return nil
}

func (s *Summarize) withIntent(full, toSummarize []message.Message) []message.Message {
	if s.OmitLatestIntent {
		return toSummarize
	}
	intent := lastUserText(full, defaultIntentBytes)
	if intent == "" {
		return toSummarize
	}
	// Avoid duplicating if the oldest slice already ends with the same user text.
	note := message.NewUserText("[Context for summarizer] Latest user message:\n" + intent)
	out := make([]message.Message, 0, len(toSummarize)+1)
	out = append(out, note)
	out = append(out, toSummarize...)
	return out
}

// adjustSplitPointForToolPairs moves the split forward so we do not orphan a ToolUse.
func adjustSplitPointForToolPairs(msgs []message.Message, split int) int {
	if split <= 0 || split >= len(msgs) {
		return split
	}
	for split < len(msgs) {
		if split > 0 && message.HasToolUse(msgs[split-1]) {
			if isToolResultMessage(msgs[split]) {
				split++
				continue
			}
		}
		if isToolResultMessage(msgs[split]) && split+1 <= len(msgs) {
			split++
			continue
		}
		break
	}
	if split >= len(msgs) {
		return len(msgs) - 1
	}
	return split
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ ContextPolicy = (*Summarize)(nil)
