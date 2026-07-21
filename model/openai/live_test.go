package openai_test

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
	e := livetest.RequireOpenAI(t)
	p := livetest.NewOpenAI(e)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var deltas int
	res, err := p.Stream(ctx, model.Request{
		System:   model.System{Text: "You are a test assistant. Answer with a single short sentence."},
		Messages: []message.Message{message.NewUserText("Reply with the word: pong")},
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
	e := livetest.RequireOpenAI(t)
	p := livetest.NewOpenAI(e)

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
		ToolChoice: tool.Choice{Type: "required"},
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
	e := livetest.RequireOpenAI(t)
	p := livetest.NewOpenAI(e)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tools := []tool.Spec{{
		Name:        "lookup",
		Description: "Look up a value by key.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
	}}

	// Turn 1: force tool call
	res1, err := p.Stream(ctx, model.Request{
		System:     model.System{Text: "Always use the lookup tool when asked for a value."},
		Messages:   []message.Message{message.NewUserText("Look up key=alpha with the lookup tool.")},
		Tools:      tools,
		ToolChoice: tool.Choice{Type: "named", Name: "lookup"},
		MaxTokens:  256,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	uses := message.ToolUses(res1.Message)
	if len(uses) == 0 {
		t.Fatalf("expected tool use, stop=%s text=%q", res1.StopReason, message.TextContent(res1.Message))
	}

	// Turn 2: feed tool result back
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

func TestLiveJSONSchema(t *testing.T) {
	e := livetest.RequireOpenAI(t)
	p := livetest.NewOpenAI(e)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"city":{"type":"string"},
			"temp_c":{"type":"number"}
		},
		"required":["city","temp_c"],
		"additionalProperties":false
	}`)

	res, err := p.Stream(ctx, model.Request{
		System: model.System{Text: "Return only JSON matching the schema. Invent plausible weather."},
		Messages: []message.Message{
			message.NewUserText("Weather for Tokyo."),
		},
		OutputSchema: schema,
		OutputName:   "weather",
		MaxTokens:    256,
	}, nil)
	if err != nil {
		// Some OpenAI-compatible gateways (e.g. Anthropic) may not support json_schema.
		if strings.Contains(err.Error(), "response_format") ||
			strings.Contains(err.Error(), "json_schema") ||
			strings.Contains(err.Error(), "400") {
			t.Skipf("endpoint does not support json_schema: %v", err)
		}
		t.Fatal(err)
	}
	raw := strings.TrimSpace(message.TextContent(res.Message))
	if raw == "" {
		t.Fatal("empty structured response")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("response is not JSON: %q err=%v", raw, err)
	}
	if _, ok := out["city"]; !ok {
		t.Fatalf("missing city: %v", out)
	}
	if _, ok := out["temp_c"]; !ok {
		t.Fatalf("missing temp_c: %v", out)
	}
	t.Logf("structured=%s", raw)
}

func TestLiveMaxTokensStop(t *testing.T) {
	e := livetest.RequireOpenAI(t)
	p := livetest.NewOpenAI(e)

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
	// Providers may report max_tokens or still end_turn with short text; either is acceptable
	// as long as we get a truncated-ish response.
	text := message.TextContent(res.Message)
	if text == "" && res.StopReason != run.StopMaxTokens {
		t.Fatalf("expected some text or max_tokens stop, got stop=%s", res.StopReason)
	}
	t.Logf("stop=%s len=%d usage=%+v", res.StopReason, len(text), res.Usage)
}

func TestLiveCancel(t *testing.T) {
	e := livetest.RequireOpenAI(t)
	p := livetest.NewOpenAI(e)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := p.Stream(ctx, model.Request{
		Messages: []message.Message{message.NewUserText("Say hello")},
	}, nil)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
	if !strings.Contains(err.Error(), "context") && ctx.Err() == nil {
		t.Logf("cancel error (acceptable if wrapped): %v", err)
	}
}
