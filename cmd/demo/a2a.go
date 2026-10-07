package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"

	eza2a "github.com/sven-victor/ez-agent/a2a"
	"github.com/sven-victor/ez-agent/agent"
)

type a2aConfig struct {
	URLs  []string
	Serve string
}

type a2aBundle struct {
	clients []*eza2a.Client
	server  *http.Server
}

func (b *a2aBundle) Close() {
	if b == nil {
		return
	}
	if b.server != nil {
		_ = b.server.Close()
	}
	for _, c := range b.clients {
		_ = c.Close()
	}
}

func connectA2A(ctx context.Context, cfg a2aConfig) (*a2aBundle, error) {
	b := &a2aBundle{}
	for _, url := range cfg.URLs {
		c, err := eza2a.Dial(ctx, url)
		if err != nil {
			b.Close()
			return nil, fmt.Errorf("a2a-url %q: %w", url, err)
		}
		b.clients = append(b.clients, c)
	}
	return b, nil
}

func serveA2A(ctx context.Context, addr string, a *agent.Agent) (*http.Server, error) {
	host, err := advertiseHost(addr)
	if err != nil {
		return nil, err
	}
	invoke := "http://" + host + "/invoke"
	card := eza2a.NewJSONRPCCard("ez-agent-demo", "ez-agent demo agent over A2A JSON-RPC.", invoke, protocol.AgentSkill{
		ID:          "chat",
		Name:        "chat",
		Description: "Send a natural-language prompt to the demo agent.",
		Tags:        []string{"chat"},
	})
	srv := &http.Server{Addr: addr, Handler: eza2a.Handler(a, card)}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("a2a-serve %q: %w", addr, err)
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	fmt.Printf("a2a-serve: %s  card http://%s/.well-known/agent-card.json\n", addr, host)
	return srv, nil
}

func advertiseHost(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("empty listen address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}
