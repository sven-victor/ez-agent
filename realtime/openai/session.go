// Package openai implements OpenAI Realtime API as a realtime.Session.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/sven-victor/ez-agent/realtime"
)

const defaultWSURL = "wss://api.openai.com/v1/realtime"

// Session is a full-duplex OpenAI Realtime WebSocket session.
type Session struct {
	conn   *websocket.Conn
	events chan realtime.Event
	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
}

// Option configures Dial.
type Option func(*dialConfig)

type dialConfig struct {
	apiKey  string
	model   string
	baseURL string
	header  http.Header
}

// WithAPIKey sets the Bearer token.
func WithAPIKey(key string) Option {
	return func(c *dialConfig) { c.apiKey = key }
}

// WithModel sets the realtime model id.
func WithModel(model string) Option {
	return func(c *dialConfig) { c.model = model }
}

// WithBaseURL overrides the WebSocket base (e.g. wss://api.openai.com/v1/realtime).
func WithBaseURL(u string) Option {
	return func(c *dialConfig) { c.baseURL = strings.TrimRight(u, "/") }
}

// Dial opens a Realtime session and starts receiving server events.
func Dial(ctx context.Context, opts ...Option) (*Session, error) {
	cfg := dialConfig{
		model:   "gpt-4o-realtime-preview",
		baseURL: defaultWSURL,
		header:  make(http.Header),
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.apiKey == "" {
		return nil, fmt.Errorf("openai realtime: api key required")
	}
	u, err := url.Parse(cfg.baseURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("model", cfg.model)
	u.RawQuery = q.Encode()

	cfg.header.Set("Authorization", "Bearer "+cfg.apiKey)
	cfg.header.Set("OpenAI-Beta", "realtime=v1")

	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPHeader: cfg.header,
	})
	if err != nil {
		return nil, fmt.Errorf("openai realtime dial: %w", err)
	}
	conn.SetReadLimit(8 << 20)

	runCtx, cancel := context.WithCancel(context.Background())
	s := &Session{
		conn:   conn,
		events: make(chan realtime.Event, 64),
		cancel: cancel,
	}
	go s.readLoop(runCtx)
	return s, nil
}

func (s *Session) Events() <-chan realtime.Event { return s.events }

// SendText creates a user message and requests a response.
func (s *Session) SendText(ctx context.Context, text string) error {
	item := map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": text},
			},
		},
	}
	if err := s.writeJSON(ctx, item); err != nil {
		return err
	}
	return s.writeJSON(ctx, map[string]any{"type": "response.create"})
}

// SendAudio appends base64 PCM/audio to the input buffer and commits it.
func (s *Session) SendAudio(ctx context.Context, mime string, data []byte) error {
	_ = mime // OpenAI Realtime expects session-configured audio format (default pcm16).
	payload := map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(data),
	}
	if err := s.writeJSON(ctx, payload); err != nil {
		return err
	}
	if err := s.writeJSON(ctx, map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		return err
	}
	return s.writeJSON(ctx, map[string]any{"type": "response.create"})
}

// Close ends the session.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	return s.conn.Close(websocket.StatusNormalClosure, "bye")
}

func (s *Session) writeJSON(ctx context.Context, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("openai realtime: session closed")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, b)
}

func (s *Session) readLoop(ctx context.Context) {
	defer close(s.events)
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				s.emit(realtime.ErrorEvent{Err: err})
			}
			s.emit(realtime.Closed{})
			return
		}
		s.handleServerEvent(data)
	}
}

func (s *Session) emit(ev realtime.Event) {
	select {
	case s.events <- ev:
	default:
		select {
		case s.events <- ev:
		default:
		}
	}
}

func (s *Session) handleServerEvent(data []byte) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		s.emit(realtime.ErrorEvent{Err: err})
		return
	}
	var typ string
	_ = json.Unmarshal(raw["type"], &typ)

	switch typ {
	case "response.audio_transcript.delta", "response.text.delta":
		var delta string
		_ = json.Unmarshal(raw["delta"], &delta)
		if delta != "" {
			s.emit(realtime.TranscriptDelta{Role: "assistant", Text: delta})
		}
	case "conversation.item.input_audio_transcription.delta":
		var delta string
		_ = json.Unmarshal(raw["delta"], &delta)
		if delta != "" {
			s.emit(realtime.TranscriptDelta{Role: "user", Text: delta})
		}
	case "response.audio.delta":
		var b64 string
		_ = json.Unmarshal(raw["delta"], &b64)
		if b64 != "" {
			rawAudio, err := base64.StdEncoding.DecodeString(b64)
			if err == nil && len(rawAudio) > 0 {
				s.emit(realtime.AudioDelta{MIME: "audio/pcm", Data: rawAudio})
			}
		}
	case "error":
		var env struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := "realtime error"
		if err := json.Unmarshal(data, &env); err == nil && env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		s.emit(realtime.ErrorEvent{Err: fmt.Errorf("openai realtime: %s", msg)})
	default:
		// session.*, response.*, rate_limits.*, etc. — ignore
	}
}

var _ realtime.Session = (*Session)(nil)
