package gemini_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/model/gemini"
	"github.com/sven-victor/ez-agent/run"
)

func TestGeminiStreamText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n"))
	}))
	defer srv.Close()

	p := gemini.New(
		gemini.WithBaseURL(srv.URL),
		gemini.WithAPIKey("k"),
		gemini.WithHTTPClient(srv.Client()),
	)
	res, err := p.Stream(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hey")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.TextContent(res.Message) != "hi" {
		t.Fatalf("text=%q", message.TextContent(res.Message))
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
}

func TestGeminiCompleteText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":generateContent") || strings.Contains(r.URL.Path, "streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer srv.Close()

	p := gemini.New(
		gemini.WithBaseURL(srv.URL),
		gemini.WithAPIKey("k"),
		gemini.WithHTTPClient(srv.Client()),
	)
	res, err := p.Complete(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hey")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.TextContent(res.Message) != "hi" {
		t.Fatalf("text=%q", message.TextContent(res.Message))
	}
	if res.StopReason != run.StopEndTurn {
		t.Fatalf("stop=%s", res.StopReason)
	}
	if res.Usage.TotalTokens != 2 {
		t.Fatalf("usage=%v", res.Usage)
	}
}

func TestGeminiWithHeader(t *testing.T) {
	var gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-Custom")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer srv.Close()

	p := gemini.New(
		gemini.WithBaseURL(srv.URL),
		gemini.WithAPIKey("k"),
		gemini.WithHeader("X-Custom", "v1"),
		gemini.WithHTTPClient(srv.Client()),
	)
	if _, err := p.Complete(context.Background(), model.Request{
		Messages: []message.Message{message.NewUserText("hey")},
	}); err != nil {
		t.Fatal(err)
	}
	if gotCustom != "v1" {
		t.Fatalf("X-Custom=%q", gotCustom)
	}
}

func TestGeminiUsageCache(t *testing.T) {
	// promptTokenCount already includes cached content. thoughts are output.
	// totalTokenCount is a different vendor total and must not be stored.
	const body = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":5,"cachedContentTokenCount":40,"totalTokenCount":999}}`
	want := run.NewUsage(100, 15, 40, 0)

	t.Run("complete", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		p := gemini.New(gemini.WithBaseURL(srv.URL), gemini.WithAPIKey("k"), gemini.WithHTTPClient(srv.Client()))
		res, err := p.Complete(context.Background(), model.Request{
			Messages: []message.Message{message.NewUserText("hey")},
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
			_, _ = w.Write([]byte("data: " + body + "\n\n"))
		}))
		defer srv.Close()
		p := gemini.New(gemini.WithBaseURL(srv.URL), gemini.WithAPIKey("k"), gemini.WithHTTPClient(srv.Client()))
		res, err := p.Stream(context.Background(), model.Request{
			Messages: []message.Message{message.NewUserText("hey")},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Usage != want {
			t.Fatalf("usage=%+v want %+v", res.Usage, want)
		}
	})
}
