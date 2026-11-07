package anthropic_test

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
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func TestAnthropicStreamTextAndTools(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
		write("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		write("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n")
		write("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		write("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"echo\",\"input\":{}}}\n\n")
		write("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"t\\\":\"}}\n\n")
		write("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"x\\\"}\"}}\n\n")
		write("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		write("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n")
		write("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	p := anthropic.New(
		anthropic.WithBaseURL(srv.URL),
		anthropic.WithAPIKey("test"),
		anthropic.WithModel("claude-test"),
		anthropic.WithHTTPClient(srv.Client()),
	)
	res, err := p.Stream(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hi")},
		Tools: []tool.Spec{{
			Name:        "echo",
			Description: "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: tool.Choice{Type: "auto"},
		MaxTokens:  256,
	}, func(ev event.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != run.StopToolUse {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if !strings.Contains(message.TextContent(res.Message), "Hi") {
		t.Fatalf("text=%q", message.TextContent(res.Message))
	}
	uses := message.ToolUses(res.Message)
	if len(uses) != 1 || uses[0].Name != "echo" {
		t.Fatalf("uses=%v", uses)
	}
	if string(uses[0].Input) != `{"t":"x"}` {
		t.Fatalf("input=%s", uses[0].Input)
	}
	if gotBody["max_tokens"].(float64) != 256 {
		t.Fatalf("body max_tokens=%v", gotBody["max_tokens"])
	}
}

func TestAnthropicCompleteTextAndTools(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[
				{"type":"text","text":"Hi"},
				{"type":"tool_use","id":"toolu_1","name":"echo","input":{"t":"x"}}
			],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":5,"output_tokens":3}
		}`))
	}))
	defer srv.Close()

	p := anthropic.New(
		anthropic.WithBaseURL(srv.URL),
		anthropic.WithAPIKey("test"),
		anthropic.WithModel("claude-test"),
		anthropic.WithHTTPClient(srv.Client()),
	)
	res, err := p.Complete(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hi")},
		Tools: []tool.Spec{{
			Name:        "echo",
			Description: "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["stream"] != false {
		t.Fatalf("stream=%v want false", gotBody["stream"])
	}
	if res.StopReason != run.StopToolUse {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if !strings.Contains(message.TextContent(res.Message), "Hi") {
		t.Fatalf("text=%q", message.TextContent(res.Message))
	}
	uses := message.ToolUses(res.Message)
	if len(uses) != 1 || uses[0].Name != "echo" {
		t.Fatalf("uses=%v", uses)
	}
	if string(uses[0].Input) != `{"t":"x"}` {
		t.Fatalf("input=%s", uses[0].Input)
	}
}

func TestAnthropicWithHeader(t *testing.T) {
	var gotBeta, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		gotKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	p := anthropic.New(
		anthropic.WithBaseURL(srv.URL),
		anthropic.WithAPIKey("secret"),
		anthropic.WithHeader("anthropic-beta", "max-tokens-3-5-2024-07-15"),
		anthropic.WithHeader("x-api-key", "override"),
		anthropic.WithHTTPClient(srv.Client()),
	)
	if _, err := p.Complete(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hi")},
	}); err != nil {
		t.Fatal(err)
	}
	if gotBeta != "max-tokens-3-5-2024-07-15" {
		t.Fatalf("anthropic-beta=%q", gotBeta)
	}
	if gotKey != "override" {
		t.Fatalf("x-api-key=%q", gotKey)
	}
}

func TestAnthropicUsageCache(t *testing.T) {
	// input_tokens excludes cache. Inclusive prompt is 10+20+30.
	// Provider does not send a total; message_delta only updates output.
	want := run.NewUsage(60, 15, 30, 20)

	t.Run("complete", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"content":[{"type":"text","text":"ok"}],
				"stop_reason":"end_turn",
				"usage":{
					"input_tokens":10,
					"output_tokens":15,
					"cache_creation_input_tokens":20,
					"cache_read_input_tokens":30
				}
			}`))
		}))
		defer srv.Close()
		p := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("test"), anthropic.WithHTTPClient(srv.Client()))
		res, err := p.Complete(context.Background(), model.Request{
			Messages: []message.Message{message.NewUserText("hi")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Usage != want {
			t.Fatalf("usage=%+v want %+v", res.Usage, want)
		}
	})

	t.Run("stream", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("" +
				"event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":1,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":30}}}\n\n" +
				"event: content_block_delta\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"event: message_delta\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":15}}\n\n" +
				"event: message_stop\n" +
				"data: {\"type\":\"message_stop\"}\n\n"))
		}))
		defer srv.Close()
		p := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("test"), anthropic.WithHTTPClient(srv.Client()))
		res, err := p.Stream(context.Background(), model.Request{
			Messages: []message.Message{message.NewUserText("hi")},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Usage != want {
			t.Fatalf("usage=%+v want %+v", res.Usage, want)
		}
	})
}
