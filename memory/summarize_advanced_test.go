package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
)

func TestEstimateTokensMonotonic(t *testing.T) {
	a := []message.Message{message.NewUserText("hi")}
	b := append(append([]message.Message{}, a...), message.NewUserText("hello world"))
	if memory.EstimateTokens(b) <= memory.EstimateTokens(a) {
		t.Fatalf("expected larger estimate")
	}
}

func TestCondenseOversizedParts(t *testing.T) {
	msgs := []message.Message{{
		Role: message.RoleUser,
		Parts: []message.Part{
			message.Text{Text: strings.Repeat("a", 100)},
			message.ToolResult{
				ToolUseID: "t1", OK: true,
				Parts: []message.Part{message.Text{Text: strings.Repeat("b", 100)}},
			},
		},
	}}
	out := memory.CondenseOversizedParts(msgs, 20)
	text := message.TextContent(out[0])
	if len(text) >= 100 {
		t.Fatalf("expected truncation, got len=%d", len(text))
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("expected notice: %q", text)
	}
	// original unchanged
	if len(message.TextContent(msgs[0])) < 100 {
		t.Fatal("original should be unchanged")
	}
}

type failSum struct{}

func (failSum) Summarize(context.Context, []message.Message) (message.Message, run.Usage, error) {
	return message.Message{}, run.Usage{}, errors.New("boom")
}

func TestSummarizeReduceFallsBackWhenSummarizerFails(t *testing.T) {
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)
	before := len(st.Messages)
	pol := &memory.Summarize{
		Summarizer: failSum{},
		KeepRecent: 5,
	}
	if err := pol.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) >= before {
		t.Fatalf("expected fallback shrink: %d -> %d", before, len(st.Messages))
	}
}

func TestSummarizeProactiveMaxTokens(t *testing.T) {
	st := &run.State{}
	st.InitHistory([]message.Message{
		message.NewUserText(strings.Repeat("x", 4000)),
		message.NewUserText(strings.Repeat("y", 4000)),
		message.NewUserText("recent"),
	})
	before := len(st.Messages)
	pol := &memory.Summarize{
		Summarizer:         &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		KeepRecent:         1,
		ProactiveMaxTokens: 100, // force proactive
		Ratio:              0.8,
	}
	if err := pol.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) >= before {
		t.Fatalf("expected proactive shrink by tokens: %d -> %d", before, len(st.Messages))
	}
}

type scriptedComplete struct {
	calls atomic.Int32
	fn    func(n int, req model.Request) (model.Result, error)
	caps  model.Capabilities
}

func (p *scriptedComplete) Name() string { return "scripted" }
func (p *scriptedComplete) Caps() model.Capabilities {
	if p.caps.TextIn || p.caps.Tools {
		return p.caps
	}
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (p *scriptedComplete) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	return p.Complete(ctx, req)
}
func (p *scriptedComplete) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	n := int(p.calls.Add(1))
	return p.fn(n, req)
}

func TestRobustSummarizerMapReduceOnOverflow(t *testing.T) {
	p := &scriptedComplete{fn: func(n int, req model.Request) (model.Result, error) {
		// First one-shot (or early calls) overflow; later chunk calls succeed.
		est := memory.EstimateTokens(req.Messages)
		if est > 50 {
			return model.Result{}, model.ErrOverflow
		}
		return model.Result{
			Message:    message.NewAssistantText("part-ok"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	r := &memory.RobustSummarizer{
		Provider:       p,
		MaxInputTokens: 10, // force map-reduce path via one-shot budget
		ChunkBytes:     40,
		MaxChunks:      5,
	}
	msgs := []message.Message{
		message.NewUserText(strings.Repeat("aaaa ", 30)),
		message.NewUserText(strings.Repeat("bbbb ", 30)),
		message.NewUserText(strings.Repeat("cccc ", 30)),
	}
	out, _, err := r.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	text := message.TextContent(out)
	if !strings.Contains(text, "Conversation summary") {
		t.Fatalf("got %q", text)
	}
	if p.calls.Load() < 2 {
		t.Fatalf("expected multiple completes, got %d", p.calls.Load())
	}
}

func TestRobustSummarizerTruncateFallback(t *testing.T) {
	p := &scriptedComplete{fn: func(n int, req model.Request) (model.Result, error) {
		return model.Result{}, errors.New("permanent failure")
	}}
	r := &memory.RobustSummarizer{
		Provider:       p,
		MaxInputTokens: 100000,
		ChunkBytes:     32,
		MaxChunks:      3,
	}
	out, _, err := r.Summarize(context.Background(), []message.Message{
		message.NewUserText(strings.Repeat("z", 200)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.TextContent(out), "truncated") {
		t.Fatalf("got %q", message.TextContent(out))
	}
}

func TestRobustSummarizerRetryThrottled(t *testing.T) {
	p := &scriptedComplete{fn: func(n int, req model.Request) (model.Result, error) {
		if n == 1 {
			return model.Result{}, model.ErrThrottled
		}
		return model.Result{
			Message:    message.NewAssistantText("ok-after-retry"),
			StopReason: run.StopEndTurn,
		}, nil
	}}
	r := &memory.RobustSummarizer{
		Provider: p,
		Retry:    model.ExponentialBackoff{MaxAttempts: 3, Initial: 1},
	}
	out, _, err := r.Summarize(context.Background(), []message.Message{message.NewUserText("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.TextContent(out), "ok-after-retry") {
		t.Fatalf("got %q", message.TextContent(out))
	}
}

func TestSegmentedSummarizer(t *testing.T) {
	p := &scriptedComplete{
		caps: model.Capabilities{TextIn: true, Tools: true, Streaming: true},
		fn: func(n int, req model.Request) (model.Result, error) {
			switch n {
			case 1:
				return model.Result{
					Message: message.Message{
						Role: message.RoleAssistant,
						Parts: []message.Part{
							message.ToolUse{
								ID:    "c1",
								Name:  "get_messages",
								Input: json.RawMessage(`{"message_ids":[{"id":"m00"},{"id":"m01"}]}`),
							},
						},
					},
					StopReason: run.StopToolUse,
				}, nil
			case 2:
				return model.Result{
					Message: message.Message{
						Role: message.RoleAssistant,
						Parts: []message.Part{
							message.ToolUse{
								ID:    "c2",
								Name:  "save_summary",
								Input: json.RawMessage(`{"summary":"condensed history"}`),
							},
						},
					},
					StopReason: run.StopToolUse,
				}, nil
			default:
				return model.Result{
					Message:    message.NewAssistantText("done"),
					StopReason: run.StopEndTurn,
				}, nil
			}
		},
	}
	s := &memory.SegmentedSummarizer{Provider: p, MaxTurns: 5}
	out, _, err := s.Summarize(context.Background(), []message.Message{
		message.NewUserText("hello"),
		message.NewAssistantText("world"),
		message.NewUserText("latest intent"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.TextContent(out), "condensed history") {
		t.Fatalf("got %q", message.TextContent(out))
	}
}

func TestSummarizeUsesSegmentedOnPrimaryFailure(t *testing.T) {
	seg := &scriptedComplete{
		caps: model.Capabilities{TextIn: true, Tools: true, Streaming: true},
		fn: func(n int, req model.Request) (model.Result, error) {
			if n == 1 {
				return model.Result{
					Message: message.Message{
						Role: message.RoleAssistant,
						Parts: []message.Part{message.ToolUse{
							ID: "c1", Name: "save_summary",
							Input: json.RawMessage(`{"summary":"from-segmented"}`),
						}},
					},
					StopReason: run.StopToolUse,
				}, nil
			}
			return model.Result{Message: message.NewAssistantText("x"), StopReason: run.StopEndTurn}, nil
		},
	}
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)
	before := len(st.Messages)
	pol := &memory.Summarize{
		Summarizer: failSum{},
		Segmented:  &memory.SegmentedSummarizer{Provider: seg, MaxTurns: 3},
		KeepRecent: 5,
		Ratio:      0.4,
	}
	if err := pol.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) >= before {
		t.Fatalf("expected shrink")
	}
	if !strings.Contains(message.TextContent(st.Messages[0]), "from-segmented") {
		t.Fatalf("got %q", message.TextContent(st.Messages[0]))
	}
}
