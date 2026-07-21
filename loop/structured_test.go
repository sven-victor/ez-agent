package loop_test

import (
	"context"
	"testing"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/loop"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
)

func TestStructuredOutputValidJSON(t *testing.T) {
	p := &scriptedProvider{
		caps: model.Capabilities{TextIn: true, Tools: true, Streaming: true, JSONSchema: true},
		fn: func(n int, req model.Request) (model.Result, error) {
			if len(req.OutputSchema) == 0 {
				t.Fatal("expected OutputSchema on request")
			}
			return model.Result{
				Message:    message.NewAssistantText(`{"answer":"42"}`),
				StopReason: run.StopEndTurn,
			}, nil
		},
	}
	l := loop.New(loop.Config{
		Provider:     p,
		OutputSchema: []byte(`{"type":"object"}`),
		OutputName:   "answer",
	})
	res, err := l.Run(context.Background(), newState(message.NewUserText("q")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Structured) != `{"answer":"42"}` {
		t.Fatalf("structured=%s", res.Structured)
	}
}

func TestStructuredOutputRetriesThenFails(t *testing.T) {
	p := &scriptedProvider{
		caps: model.Capabilities{TextIn: true, Tools: true, Streaming: true, JSONSchema: true},
		fn: func(n int, req model.Request) (model.Result, error) {
			return model.Result{
				Message:    message.NewAssistantText("not-json"),
				StopReason: run.StopEndTurn,
			}, nil
		},
	}
	l := loop.New(loop.Config{
		Provider:      p,
		OutputSchema:  []byte(`{"type":"object"}`),
		MaxModelRetry: 2,
	})
	_, err := l.Run(context.Background(), newState(message.NewUserText("q")), func(event.Event) error { return nil })
	if err == nil {
		t.Fatal("expected error")
	}
	if p.calls < 3 { // initial + 2 retries... actually modelRetries increments then continue, so 1+2 = 3 attempts after exceeding?
		// On invalid: modelRetries++ ; if > MaxModelRetry fail. Start modelRetries=0.
		// attempt1 fail -> retries=1 continue
		// attempt2 fail -> retries=2 continue
		// attempt3 fail -> retries=3 > 2 fail
		// so calls should be 3
	}
	if p.calls != 3 {
		t.Fatalf("calls=%d want 3", p.calls)
	}
}
