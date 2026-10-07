package a2a_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"

	eza2a "github.com/sven-victor/ez-agent/a2a"
	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

type echoProvider struct{}

func (echoProvider) Name() string { return "echo" }
func (echoProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Tools: true, Streaming: true}
}
func (echoProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	text := "empty"
	if n := len(req.Messages); n > 0 {
		if t := message.TextContent(req.Messages[n-1]); t != "" {
			text = t
		}
	}
	res := model.Result{
		Message:    message.NewAssistantText("echo:" + text),
		StopReason: run.StopEndTurn,
	}
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason})
	}
	return res, nil
}
func (echoProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return echoProvider{}.Stream(ctx, req, nil)
}

type handlerHolder struct{ h http.Handler }

func (h *handlerHolder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.h.ServeHTTP(w, r)
}

func TestJSONRPCRoundTrip(t *testing.T) {
	ag, err := agent.New(agent.WithProvider(echoProvider{}))
	if err != nil {
		t.Fatal(err)
	}

	hold := &handlerHolder{h: http.NotFoundHandler()}
	srv := httptest.NewServer(hold)
	t.Cleanup(srv.Close)

	card := eza2a.NewJSONRPCCard("echo", "echo agent", srv.URL+"/invoke", protocol.AgentSkill{
		ID:          "echo",
		Name:        "echo",
		Description: "Echo the prompt",
		Tags:        []string{"echo"},
	})
	hold.h = eza2a.Handler(ag, card)

	c, err := eza2a.Dial(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	specs := c.Specs()
	if len(specs) != 1 || specs[0].Name != "echo" {
		t.Fatalf("specs=%v", specs)
	}
	h, ok := c.Lookup("echo")
	if !ok {
		t.Fatal("missing echo tool")
	}
	res, err := h(context.Background(), tool.Call{
		ID:    "c1",
		Name:  "echo",
		Input: []byte(`{"prompt":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("not ok: %+v", res)
	}
	text := message.TextContent(message.Message{Parts: res.Content})
	if text != "echo:hello" {
		t.Fatalf("got %q", text)
	}
}

func TestNewJSONRPCCardDefaultSkill(t *testing.T) {
	card := eza2a.NewJSONRPCCard("n", "d", "http://127.0.0.1:9/invoke")
	if len(card.Skills) != 1 || card.Skills[0].ID != "chat" {
		t.Fatalf("skills=%v", card.Skills)
	}
	if card.Name != "n" {
		t.Fatalf("name=%s", card.Name)
	}
	if len(card.SupportedInterfaces) == 0 || card.SupportedInterfaces[0].URL == "" {
		t.Fatal("missing interface url")
	}
}
