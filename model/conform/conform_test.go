package conform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/model/anthropic"
	"github.com/sven-victor/ez-agent/model/gemini"
	"github.com/sven-victor/ez-agent/model/openai"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

// Shared fixtures: plain text, image+text, tools — each provider must produce
// assistant Message with expected role structure and stop reasons.

func TestConformPlainText(t *testing.T) {
	for _, tc := range []struct {
		name      string
		newP      func(url string, client *http.Client) model.Provider
		pathMatch func(path string) bool
		sse       string
	}{
		{
			name:      "openai",
			pathMatch: func(path string) bool { return path == "/chat/completions" },
			newP: func(url string, c *http.Client) model.Provider {
				return openai.New(openai.WithBaseURL(url), openai.WithAPIKey("k"), openai.WithHTTPClient(c))
			},
			sse: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:      "anthropic",
			pathMatch: func(path string) bool { return path == "/v1/messages" },
			newP: func(url string, c *http.Client) model.Provider {
				return anthropic.New(anthropic.WithBaseURL(url), anthropic.WithAPIKey("k"), anthropic.WithHTTPClient(c))
			},
			sse: "" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		},
		{
			name:      "gemini",
			pathMatch: func(path string) bool { return strings.Contains(path, ":streamGenerateContent") },
			newP: func(url string, c *http.Client) model.Provider {
				return gemini.New(gemini.WithBaseURL(url), gemini.WithAPIKey("k"), gemini.WithHTTPClient(c))
			},
			sse: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !tc.pathMatch(r.URL.Path) {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tc.sse))
			}))
			defer srv.Close()
			p := tc.newP(srv.URL, srv.Client())
			res, err := p.Stream(context.Background(), model.Request{
				Messages:  []message.Message{message.NewUserText("hi")},
				MaxTokens: 64,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertAssistantText(t, res, "hello", run.StopEndTurn)
		})
	}
}

func TestConformTools(t *testing.T) {
	tools := []tool.Spec{{
		Name:        "echo",
		Description: "echo",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}}

	t.Run("openai", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
					"data: [DONE]\n\n",
			))
		}))
		defer srv.Close()
		p := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("k"), openai.WithHTTPClient(srv.Client()))
		res, err := p.Stream(context.Background(), model.Request{
			Messages: []message.Message{message.NewUserText("hi")},
			Tools:    tools,
		}, func(event.Event) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		assertToolUse(t, res, "echo")
	})

	t.Run("anthropic", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"echo\",\"input\":{}}}\n\n" +
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"x\\\"}\"}}\n\n" +
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n",
			))
		}))
		defer srv.Close()
		p := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("k"), anthropic.WithHTTPClient(srv.Client()))
		res, err := p.Stream(context.Background(), model.Request{
			Messages:  []message.Message{message.NewUserText("hi")},
			Tools:     tools,
			MaxTokens: 128,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertToolUse(t, res, "echo")
	})
}

func TestConformImageAcceptedByCaps(t *testing.T) {
	for _, p := range []model.Provider{
		openai.New(openai.WithCaps(model.Capabilities{TextIn: true, ImageIn: true, Streaming: true})),
		anthropic.New(anthropic.WithCaps(model.Capabilities{TextIn: true, ImageIn: true, Streaming: true, Tools: true})),
	} {
		t.Run(p.Name(), func(t *testing.T) {
			err := model.Validate(p, model.Request{
				Messages: []message.Message{
					message.NewUserParts(
						message.Text{Text: "see"},
						message.Image{Blob: message.Blob{MIME: "image/png", Bytes: []byte("x")}},
					),
				},
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertAssistantText(t *testing.T, res model.Result, want string, stop run.StopReason) {
	t.Helper()
	if res.Message.Role != message.RoleAssistant {
		t.Fatalf("role=%s", res.Message.Role)
	}
	if message.TextContent(res.Message) != want {
		t.Fatalf("text=%q want %q", message.TextContent(res.Message), want)
	}
	if res.StopReason != stop {
		t.Fatalf("stop=%s want %s", res.StopReason, stop)
	}
}

func assertToolUse(t *testing.T, res model.Result, name string) {
	t.Helper()
	if res.StopReason != run.StopToolUse {
		t.Fatalf("stop=%s", res.StopReason)
	}
	uses := message.ToolUses(res.Message)
	if len(uses) != 1 || uses[0].Name != name {
		t.Fatalf("uses=%v", uses)
	}
}

func TestConformGeminiTools(t *testing.T) {
	tools := []tool.Spec{{
		Name:        "echo",
		Description: "echo",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"echo","args":{"q":"x"}}}]},"finishReason":"STOP"}]}` + "\n\n",
		))
	}))
	defer srv.Close()
	p := gemini.New(gemini.WithBaseURL(srv.URL), gemini.WithAPIKey("k"), gemini.WithHTTPClient(srv.Client()))
	res, err := p.Stream(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hi")},
		Tools:    tools,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertToolUse(t, res, "echo")
}

func TestConformImageEncodeRoundTrip(t *testing.T) {
	img := message.NewUserParts(
		message.Text{Text: "see"},
		message.Image{Blob: message.Blob{MIME: "image/png", Bytes: []byte{0x89, 0x50, 0x4e, 0x47}}},
	)
	t.Run("openai", func(t *testing.T) {
		var sawImage bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			msgs, _ := body["messages"].([]any)
			for _, m := range msgs {
				mm, _ := m.(map[string]any)
				content, _ := mm["content"].([]any)
				for _, c := range content {
					cm, _ := c.(map[string]any)
					if cm["type"] == "image_url" {
						sawImage = true
					}
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		}))
		defer srv.Close()
		p := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("k"), openai.WithHTTPClient(srv.Client()))
		_, err := p.Stream(context.Background(), model.Request{Messages: []message.Message{img}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !sawImage {
			t.Fatal("expected image_url in request")
		}
	})
	t.Run("anthropic", func(t *testing.T) {
		var sawImage bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			msgs, _ := body["messages"].([]any)
			for _, m := range msgs {
				mm, _ := m.(map[string]any)
				content, _ := mm["content"].([]any)
				for _, c := range content {
					cm, _ := c.(map[string]any)
					if cm["type"] == "image" {
						sawImage = true
					}
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
			))
		}))
		defer srv.Close()
		p := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("k"), anthropic.WithHTTPClient(srv.Client()))
		_, err := p.Stream(context.Background(), model.Request{Messages: []message.Message{img}, MaxTokens: 32}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !sawImage {
			t.Fatal("expected image content block")
		}
	})
}

func TestConformAnthropicJSONSchema(t *testing.T) {
	var sawFormat bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if oc, ok := body["output_config"].(map[string]any); ok {
			if f, ok := oc["format"].(map[string]any); ok && f["type"] == "json_schema" {
				sawFormat = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"{\\\"a\\\":1}\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		))
	}))
	defer srv.Close()
	p := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("k"), anthropic.WithHTTPClient(srv.Client()))
	if !p.Caps().JSONSchema {
		t.Fatal("expected JSONSchema cap")
	}
	_, err := p.Stream(context.Background(), model.Request{
		Messages:     []message.Message{message.NewUserText("hi")},
		OutputSchema: []byte(`{"type":"object","properties":{"a":{"type":"number"}},"required":["a"],"additionalProperties":false}`),
		MaxTokens:    64,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sawFormat {
		t.Fatal("expected output_config.format")
	}
}

func TestConformAnthropicReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"sig\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan\"}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		))
	}))
	defer srv.Close()
	p := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("k"), anthropic.WithHTTPClient(srv.Client()))
	res, err := p.Stream(context.Background(), model.Request{
		Messages:  []message.Message{message.NewUserText("hi")},
		MaxTokens: 64,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var gotReasoning bool
	for _, part := range res.Message.Parts {
		if r, ok := part.(message.Reasoning); ok {
			gotReasoning = true
			if r.Text != "plan" || r.Signature != "sig" {
				t.Fatalf("reasoning=%+v", r)
			}
		}
	}
	if !gotReasoning {
		t.Fatal("expected Reasoning part")
	}
	if message.TextContent(res.Message) != "hi" {
		t.Fatalf("text=%q", message.TextContent(res.Message))
	}
}

func TestConformSystemParts(t *testing.T) {
	for _, p := range []model.Provider{
		openai.New(),
		gemini.New(),
		anthropic.New(),
	} {
		t.Run(p.Name(), func(t *testing.T) {
			if !p.Caps().SystemParts {
				t.Fatal("expected SystemParts")
			}
			err := model.Validate(p, model.Request{
				System: model.System{
					Text:  "sys",
					Parts: []message.Part{message.Text{Text: "extra"}},
				},
				Messages: []message.Message{message.NewUserText("hi")},
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
