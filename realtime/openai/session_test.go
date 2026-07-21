package openai_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	rtopenai "github.com/sven-victor/ez-agent/realtime/openai"
	"github.com/sven-victor/ez-agent/realtime"
)

func TestDialMockServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"session.created"}`))
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var msg map[string]any
		_ = json.Unmarshal(data, &msg)
		if msg["type"] != "conversation.item.create" {
			t.Errorf("first client msg type=%v", msg["type"])
		}
		audio := base64.StdEncoding.EncodeToString([]byte("pcm"))
		_ = c.Write(r.Context(), websocket.MessageText, []byte(
			`{"type":"response.audio_transcript.delta","delta":"hi"}`,
		))
		_ = c.Write(r.Context(), websocket.MessageText, []byte(
			`{"type":"response.audio.delta","delta":"`+audio+`"}`,
		))
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := rtopenai.Dial(ctx,
		rtopenai.WithAPIKey("k"),
		rtopenai.WithBaseURL(wsURL),
		rtopenai.WithModel("test"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if err := s.SendText(ctx, "hello"); err != nil {
		t.Fatal(err)
	}

	gotText, gotAudio := false, false
	deadline := time.After(3 * time.Second)
	for !gotText || !gotAudio {
		select {
		case <-deadline:
			t.Fatalf("timeout text=%v audio=%v", gotText, gotAudio)
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed early")
			}
			switch e := ev.(type) {
			case realtime.TranscriptDelta:
				if e.Text == "hi" {
					gotText = true
				}
			case realtime.AudioDelta:
				if string(e.Data) == "pcm" {
					gotAudio = true
				}
			case realtime.Closed:
				if !gotText {
					t.Fatal("closed before transcript")
				}
				return
			}
		}
	}
}
