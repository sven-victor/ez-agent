package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"

	eza2a "github.com/sven-victor/ez-agent/a2a"
	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
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
		Message:    message.NewAssistantText("echo: " + text),
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

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	ag, err := agent.New(
		agent.WithProvider(echoProvider{}),
		agent.WithSystem("You echo the user text."),
	)
	if err != nil {
		log.Fatal(err)
	}

	host := *addr
	if len(host) > 0 && host[0] == ':' {
		host = "127.0.0.1" + host
	}
	invoke := fmt.Sprintf("http://%s/invoke", host)
	card := eza2a.NewJSONRPCCard("echo", "Deterministic echo agent (no LLM).", invoke, protocol.AgentSkill{
		ID:          "echo",
		Name:        "echo",
		Description: "Echo the prompt back with an echo: prefix.",
		Tags:        []string{"echo"},
		Examples:    []string{"hello"},
	})

	log.Printf("A2A JSON-RPC echo server listening on %s", *addr)
	log.Printf("Agent Card: http://%s/.well-known/agent-card.json", host)
	if err := eza2a.ListenAndServe(*addr, ag, card); err != nil {
		log.Fatal(err)
	}
}
