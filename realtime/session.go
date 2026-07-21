// Package realtime provides full-duplex voice/text sessions outside agent.Run (P2).
//
// The turn loop remains request/response. Realtime sessions stream audio/text
// independently; vendor WebSocket adapters can implement Session.
package realtime

import (
	"context"
	"fmt"
	"sync"
)

// Session is a full-duplex realtime channel (microphone in, model audio/text out).
type Session interface {
	SendText(ctx context.Context, text string) error
	SendAudio(ctx context.Context, mime string, data []byte) error
	Events() <-chan Event
	Close(ctx context.Context) error
}

// Event is a typed realtime stream event.
type Event interface {
	realtimeEvent()
}

// TranscriptDelta is incremental text (user or assistant).
type TranscriptDelta struct {
	Role string // "user" | "assistant"
	Text string
}

func (TranscriptDelta) realtimeEvent() {}

// AudioDelta is a model audio chunk for playback.
type AudioDelta struct {
	MIME string
	Data []byte
}

func (AudioDelta) realtimeEvent() {}

// ErrorEvent carries a non-fatal or fatal session error.
type ErrorEvent struct {
	Err error
}

func (ErrorEvent) realtimeEvent() {}

// Closed is emitted when the session ends.
type Closed struct{}

func (Closed) realtimeEvent() {}

// Pipe is an in-process Session for tests and local demos.
// SendText echoes an assistant transcript; SendAudio acks with a tiny silence frame.
type Pipe struct {
	mu     sync.Mutex
	ch     chan Event
	closed bool
}

// OpenPipe creates a buffered Pipe session.
func OpenPipe(buffer int) *Pipe {
	if buffer <= 0 {
		buffer = 32
	}
	return &Pipe{ch: make(chan Event, buffer)}
}

func (p *Pipe) Events() <-chan Event { return p.ch }

func (p *Pipe) SendText(ctx context.Context, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("realtime: session closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.ch <- TranscriptDelta{Role: "user", Text: text}:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.ch <- TranscriptDelta{Role: "assistant", Text: "echo: " + text}:
		return nil
	}
}

func (p *Pipe) SendAudio(ctx context.Context, mime string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("realtime: session closed")
	}
	if mime == "" {
		mime = "audio/pcm"
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.ch <- TranscriptDelta{Role: "user", Text: fmt.Sprintf("[audio %s %d bytes]", mime, len(data))}:
	}
	// Tiny ack chunk (empty payload with MIME) — apps play AudioDelta; SDK has no speaker.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.ch <- AudioDelta{MIME: mime, Data: nil}:
		return nil
	}
}

func (p *Pipe) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	select {
	case <-ctx.Done():
		close(p.ch)
		return ctx.Err()
	case p.ch <- Closed{}:
		close(p.ch)
		return nil
	}
}

var _ Session = (*Pipe)(nil)
