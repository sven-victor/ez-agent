package memory_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

type failSummarizer struct{}

func (failSummarizer) Summarize(ctx context.Context, msgs []message.Message) (message.Message, run.Usage, error) {
	return message.Message{}, run.Usage{}, errors.New("summarizer boom")
}

func TestSummarizeFiresSummaryHooks(t *testing.T) {
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)

	h := hook.NewRegistry()
	var order []string
	var sawReactive bool
	var beforeCount, afterCount int
	var hookUsage run.Usage
	h.OnBeforeSummary(func(ctx context.Context, e *hook.BeforeSummary) error {
		order = append(order, "before")
		sawReactive = e.Reactive
		if len(e.Input) == 0 {
			t.Fatal("BeforeSummary.Input empty")
		}
		return nil
	})
	h.OnAfterSummary(func(ctx context.Context, e *hook.AfterSummary) error {
		order = append(order, "after")
		beforeCount = e.BeforeCount
		afterCount = e.AfterCount
		hookUsage = e.Usage
		if !strings.Contains(message.TextContent(e.Summary), "summary") {
			t.Fatalf("summary=%q", message.TextContent(e.Summary))
		}
		if e.State.Usage.TotalTokens != e.Usage.TotalTokens {
			t.Fatalf("State.Usage=%v Usage=%v", e.State.Usage, e.Usage)
		}
		return nil
	})

	pol := &memory.Summarize{
		Summarizer: &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		Ratio:      0.3,
		KeepRecent: 10,
		Hooks:      h,
	}
	if err := pol.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !sawReactive {
		t.Fatal("expected Reactive=true on Reduce")
	}
	if len(order) != 2 || order[0] != "before" || order[1] != "after" {
		t.Fatalf("order=%v", order)
	}
	if beforeCount != 20 || afterCount >= beforeCount {
		t.Fatalf("before=%d after=%d", beforeCount, afterCount)
	}
	if hookUsage.TotalTokens != 15 || st.Usage.TotalTokens != 15 {
		t.Fatalf("usage hook=%v state=%v", hookUsage, st.Usage)
	}
}

func TestSummarizeBeforeSummarySkip(t *testing.T) {
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)
	before := len(st.Messages)

	h := hook.NewRegistry()
	var afterErr error
	var afterCalls int
	h.OnBeforeSummary(func(ctx context.Context, e *hook.BeforeSummary) error {
		e.Skip = true
		return nil
	})
	h.OnAfterSummary(func(ctx context.Context, e *hook.AfterSummary) error {
		afterCalls++
		afterErr = e.Err
		repl := message.NewUserText("should-not-apply")
		e.ReplaceSummary = &repl
		return nil
	})
	pol := &memory.Summarize{
		Summarizer:           &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		KeepRecent:           4,
		ProactiveMaxMessages: 5,
		Hooks:                h,
	}
	// Prepare ignores summarize errors (including skip).
	if err := pol.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != before {
		t.Fatalf("skip should leave messages untouched: before=%d after=%d", before, len(st.Messages))
	}
	if afterCalls != 1 {
		t.Fatalf("AfterSummary calls=%d", afterCalls)
	}
	if afterErr == nil {
		t.Fatal("expected AfterSummary.Err on skip")
	}
	if message.TextContent(st.Messages[0]) == "should-not-apply" {
		t.Fatal("ReplaceSummary must be ignored when AfterSummary.Err is set")
	}
}

func TestSummarizeAfterSummaryOnFailure(t *testing.T) {
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)
	before := len(st.Messages)

	h := hook.NewRegistry()
	var afterErr error
	h.OnAfterSummary(func(ctx context.Context, e *hook.AfterSummary) error {
		afterErr = e.Err
		return nil
	})
	pol := &memory.Summarize{
		Summarizer: failSummarizer{},
		Ratio:      0.3,
		KeepRecent: 10,
		Hooks:      h,
	}
	// Reduce falls back to dropping oldest messages when summarizer fails.
	if err := pol.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if afterErr == nil {
		t.Fatal("expected AfterSummary.Err even when Reduce falls back")
	}
	if len(st.Messages) >= before {
		t.Fatalf("fallback should shrink messages: before=%d after=%d", before, len(st.Messages))
	}
}

func TestSummarizeAfterSummaryReplace(t *testing.T) {
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)

	h := hook.NewRegistry()
	h.OnAfterSummary(func(ctx context.Context, e *hook.AfterSummary) error {
		repl := message.NewUserText("hook-replaced-summary")
		e.ReplaceSummary = &repl
		return nil
	})
	pol := &memory.Summarize{
		Summarizer: &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		Ratio:      0.3,
		KeepRecent: 10,
		Hooks:      h,
	}
	if err := pol.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if message.TextContent(st.Messages[0]) != "hook-replaced-summary" {
		t.Fatalf("got %q", message.TextContent(st.Messages[0]))
	}
}

func TestSetHooksPropagates(t *testing.T) {
	h := hook.NewRegistry()
	inner := &memory.Summarize{Summarizer: &memory.ProviderSummarizer{}}
	pol := &memory.Summarize{
		Summarizer: &memory.ProviderSummarizer{},
		Inner:      inner,
		Fallback:   &memory.Summarize{Summarizer: &memory.ProviderSummarizer{}},
	}
	memory.SetHooks(pol, h)
	if pol.Hooks != h || inner.Hooks != h {
		t.Fatal("Hooks not set on Summarize tree")
	}
	if pol.Fallback.(*memory.Summarize).Hooks != h {
		t.Fatal("Hooks not set on Fallback")
	}
	other := hook.NewRegistry()
	pol2 := &memory.Summarize{Hooks: other, Summarizer: &memory.ProviderSummarizer{}}
	memory.SetHooks(pol2, h)
	if pol2.Hooks != other {
		t.Fatal("existing Hooks overwritten")
	}
}
