package memory

import (
	"context"
	"fmt"

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
type Summarizer interface {
	Summarize(ctx context.Context, msgs []message.Message) (message.Message, error)
}

// Summarize replaces the oldest fraction of Messages with an LLM summary.
//
// Reduce always summarizes (reactive overflow recovery).
// When ProactiveMaxMessages > 0 and len(Messages) exceeds it, Prepare also
// summarizes (best-effort: failures are ignored so the model call can proceed).
type Summarize struct {
	Inner                ContextPolicy // optional; run after summarization attempts
	Summarizer           Summarizer
	Ratio                float64 // default 0.3, clamped to [0.1, 0.8]
	KeepRecent           int     // default 10
	ProactiveMaxMessages int     // 0 = disabled; Prepare summarizes when len > this
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

func (s *Summarize) Prepare(ctx context.Context, st *run.State) error {
	if s.ProactiveMaxMessages > 0 && len(st.Messages) > s.ProactiveMaxMessages {
		_ = s.summarizeOldest(ctx, st) // best-effort
	}
	if s.Inner != nil {
		return s.Inner.Prepare(ctx, st)
	}
	return nil
}

func (s *Summarize) Reduce(ctx context.Context, st *run.State) error {
	before := len(st.Messages)
	if err := s.summarizeOldest(ctx, st); err != nil {
		return err
	}
	if s.Inner != nil {
		if err := s.Inner.Reduce(ctx, st); err != nil {
			// Inner reduce may fail if already small; only require overall progress.
			if len(st.Messages) >= before {
				return err
			}
		}
	}
	if len(st.Messages) >= before {
		return fmt.Errorf("summarize reduce: failed to reduce context")
	}
	return nil
}

func (s *Summarize) summarizeOldest(ctx context.Context, st *run.State) error {
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
	summary, err := s.Summarizer.Summarize(ctx, toSummarize)
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}
	if summary.Role == "" {
		summary.Role = message.RoleUser
	}
	st.Messages = append([]message.Message{summary}, remaining...)
	return nil
}

// adjustSplitPointForToolPairs moves the split forward so we do not orphan a ToolUse.
func adjustSplitPointForToolPairs(msgs []message.Message, split int) int {
	if split <= 0 || split >= len(msgs) {
		return split
	}
	// If split lands between toolUse and toolResult, include the result.
	for split < len(msgs) {
		if split > 0 && message.HasToolUse(msgs[split-1]) {
			if isToolResultMessage(msgs[split]) {
				split++
				continue
			}
		}
		// If split starts on a dangling tool-result, include it with prior or advance.
		if isToolResultMessage(msgs[split]) && split+1 <= len(msgs) {
			// Prefer moving split past the result so remaining does not start with orphan result.
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
