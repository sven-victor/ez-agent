package openai_test

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
	"github.com/sven-victor/ez-agent/model/openai"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func TestOpenAIStreamTextAndTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"t\\\"\"}}]},\"finish_reason\":null}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\":\\\"x\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := openai.New(
		openai.WithBaseURL(srv.URL),
		openai.WithAPIKey("test"),
		openai.WithModel("gpt-test"),
		openai.WithHTTPClient(srv.Client()),
	)

	var deltas []string
	res, err := p.Stream(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hi")},
		Tools: []tool.Spec{{
			Name:        "echo",
			Description: "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}, func(ev event.Event) error {
		if d, ok := ev.(event.ModelDelta); ok {
			deltas = append(deltas, d.TextDelta)
		}
		return nil
	})
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
	if res.Usage.TotalTokens != 3 {
		t.Fatalf("usage=%v", res.Usage)
	}
	if len(deltas) == 0 {
		t.Fatal("expected deltas")
	}
	if len(res.RawRequest) == 0 || !strings.Contains(string(res.RawRequest), `"stream":true`) {
		t.Fatalf("RawRequest=%s", res.RawRequest)
	}
	rawResp := string(res.RawResponse)
	if !strings.Contains(rawResp, `"content":"Hi"`) || !strings.Contains(rawResp, "[DONE]") {
		t.Fatalf("RawResponse=%s", rawResp)
	}
	if strings.Contains(rawResp, "data:") {
		t.Fatal("RawResponse should store data payloads without data: prefix")
	}
}

func TestOpenAICompleteTextAndTools(t *testing.T) {
	var streamFlag any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		streamFlag = body["stream"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"content":"Hi",
					"tool_calls":[{
						"id":"call_1",
						"type":"function",
						"function":{"name":"echo","arguments":"{\"t\":\"x\"}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
		}`))
	}))
	defer srv.Close()

	p := openai.New(
		openai.WithBaseURL(srv.URL),
		openai.WithAPIKey("test"),
		openai.WithModel("gpt-test"),
		openai.WithHTTPClient(srv.Client()),
	)
	res, err := p.Complete(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hi")},
		Tools: []tool.Spec{{
			Name:        "echo",
			Description: "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamFlag != false {
		t.Fatalf("stream=%v want false", streamFlag)
	}
	if res.StopReason != run.StopToolUse {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if message.TextContent(res.Message) != "Hi" {
		t.Fatalf("text=%q", message.TextContent(res.Message))
	}
	uses := message.ToolUses(res.Message)
	if len(uses) != 1 || uses[0].Name != "echo" || string(uses[0].Input) != `{"t":"x"}` {
		t.Fatalf("uses=%v", uses)
	}
	if res.Usage.TotalTokens != 3 {
		t.Fatalf("usage=%v", res.Usage)
	}
	if len(res.RawRequest) == 0 || strings.Contains(string(res.RawRequest), `"stream":true`) {
		t.Fatalf("RawRequest=%s", res.RawRequest)
	}
	if !strings.Contains(string(res.RawResponse), `"content":"Hi"`) {
		t.Fatalf("RawResponse=%s", res.RawResponse)
	}
}

func TestOpenAIOrganizationAndHeaders(t *testing.T) {
	var gotOrg, gotCustom, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrg = r.Header.Get("OpenAI-Organization")
		gotCustom = r.Header.Get("X-Custom")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	p := openai.New(
		openai.WithBaseURL(srv.URL),
		openai.WithAPIKey("secret"),
		openai.WithOrganization("org-abc"),
		openai.WithHeader("X-Custom", "v1"),
		openai.WithHeader("Authorization", "Bearer override"), // custom wins over WithAPIKey
		openai.WithHTTPClient(srv.Client()),
	)
	if _, err := p.Complete(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hi")},
	}); err != nil {
		t.Fatal(err)
	}
	if gotOrg != "org-abc" {
		t.Fatalf("OpenAI-Organization=%q", gotOrg)
	}
	if gotCustom != "v1" {
		t.Fatalf("X-Custom=%q", gotCustom)
	}
	if gotAuth != "Bearer override" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
}

func TestOpenAIUsageCache(t *testing.T) {
	const body = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":999,"prompt_tokens_details":{"cached_tokens":80}}}`
	want := run.NewUsage(100, 20, 80, 0)

	t.Run("complete", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		p := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("test"), openai.WithHTTPClient(srv.Client()))
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
			_, _ = w.Write([]byte("data: " + body + "\n\ndata: [DONE]\n\n"))
		}))
		defer srv.Close()
		p := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("test"), openai.WithHTTPClient(srv.Client()))
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
