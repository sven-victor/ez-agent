package anthropic_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/internal/livetest"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func TestLiveStreamText(t *testing.T) {
	e := livetest.RequireAnthropic(t)
	p := livetest.NewAnthropic(e)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var deltas int
	res, err := p.Stream(ctx, model.Request{
		System:    model.System{Text: "You are a test assistant. Answer with a single short sentence."},
		Messages:  []message.Message{message.NewUserText("Reply with the word: pong")},
		MaxTokens: 128,
	}, func(ev event.Event) error {
		if _, ok := ev.(event.ModelDelta); ok {
			deltas++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop reason: %s", res.StopReason)
	}
	text := message.TextContent(res.Message)
	if text == "" {
		t.Fatal("empty model response")
	}
	if deltas == 0 {
		t.Fatal("expected streaming deltas")
	}
	if !strings.Contains(strings.ToLower(text), "pong") {
		t.Logf("response did not contain 'pong' (model behavior): %q", text)
	}
	t.Logf("response: %q usage=%+v", text, res.Usage)
}

func TestLiveStreamTools(t *testing.T) {
	e := livetest.RequireAnthropic(t)
	p := livetest.NewAnthropic(e)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := p.Stream(ctx, model.Request{
		System: model.System{Text: "You must call the get_secret tool. Do not invent the answer."},
		Messages: []message.Message{
			message.NewUserText("What is the secret code? Call get_secret."),
		},
		Tools: []tool.Spec{{
			Name:        "get_secret",
			Description: "Returns the secret code.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
		ToolChoice: tool.Choice{Type: "any"},
		MaxTokens:  256,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopToolUse {
		t.Fatalf("expected tool_use stop, got %s text=%q", res.StopReason, message.TextContent(res.Message))
	}
	uses := message.ToolUses(res.Message)
	if len(uses) == 0 {
		t.Fatal("expected at least one tool use")
	}
	found := false
	for _, u := range uses {
		if u.Name == "get_secret" {
			found = true
			if u.ID == "" {
				t.Fatal("tool use missing id")
			}
		}
	}
	if !found {
		t.Fatalf("expected get_secret, got %#v", uses)
	}
	t.Logf("tool uses=%d usage=%+v", len(uses), res.Usage)
}

func TestLiveMultiTurnWithToolResult(t *testing.T) {
	e := livetest.RequireAnthropic(t)
	p := livetest.NewAnthropic(e)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tools := []tool.Spec{{
		Name:        "lookup",
		Description: "Look up a value by key.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
	}}

	res1, err := p.Stream(ctx, model.Request{
		System:     model.System{Text: "Always use the lookup tool when asked for a value."},
		Messages:   []message.Message{message.NewUserText("Look up key=alpha with the lookup tool.")},
		Tools:      tools,
		ToolChoice: tool.Choice{Type: "tool", Name: "lookup"},
		MaxTokens:  256,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	uses := message.ToolUses(res1.Message)
	if len(uses) == 0 {
		t.Fatalf("expected tool use, stop=%s text=%q", res1.StopReason, message.TextContent(res1.Message))
	}

	history := []message.Message{
		message.NewUserText("Look up key=alpha with the lookup tool."),
		res1.Message,
		{
			Role: message.RoleUser,
			Parts: []message.Part{
				message.ToolResult{
					ToolUseID: uses[0].ID,
					OK:        true,
					Parts:     []message.Part{message.Text{Text: "VALUE=nebula"}},
				},
			},
		},
	}
	res2, err := p.Stream(ctx, model.Request{
		System:    model.System{Text: "Answer using the tool result. Mention the value clearly."},
		Messages:  history,
		Tools:     tools,
		MaxTokens: 256,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := message.TextContent(res2.Message)
	if text == "" {
		t.Fatal("empty second-turn response")
	}
	if !strings.Contains(strings.ToLower(text), "nebula") {
		t.Logf("second turn did not mention nebula (model wording): %q", text)
	}
	t.Logf("turn2: %q stop=%s", text, res2.StopReason)
}

func TestLiveMaxTokensStop(t *testing.T) {
	e := livetest.RequireAnthropic(t)
	p := livetest.NewAnthropic(e)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := p.Stream(ctx, model.Request{
		System:    model.System{Text: "Write a very long essay with many paragraphs."},
		Messages:  []message.Message{message.NewUserText("Write at least 2000 words about rivers.")},
		MaxTokens: 16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := message.TextContent(res.Message)
	if text == "" && res.StopReason != run.StopMaxTokens {
		t.Fatalf("expected some text or max_tokens stop, got stop=%s", res.StopReason)
	}
	t.Logf("stop=%s len=%d usage=%+v", res.StopReason, len(text), res.Usage)
}

func TestLiveCancel(t *testing.T) {
	e := livetest.RequireAnthropic(t)
	p := livetest.NewAnthropic(e)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Stream(ctx, model.Request{
		Messages:  []message.Message{message.NewUserText("Say hello")},
		MaxTokens: 64,
	}, nil)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestLiveSystemAndUsage(t *testing.T) {
	e := livetest.RequireAnthropic(t)
	p := livetest.NewAnthropic(e)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := p.Stream(ctx, model.Request{
		System:    model.System{Text: "Always start your reply with the exact token: READY."},
		Messages:  []message.Message{message.NewUserText("Confirm you are ready.")},
		MaxTokens: 64,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := message.TextContent(res.Message)
	if text == "" {
		t.Fatal("empty response")
	}
	if res.Usage.InputTokens == 0 && res.Usage.OutputTokens == 0 {
		t.Logf("usage not reported by gateway: %+v", res.Usage)
	}
	if !strings.Contains(text, "READY") {
		t.Logf("system instruction may not have been followed: %q", text)
	}
	t.Logf("response=%q usage=%+v", text, res.Usage)
}
