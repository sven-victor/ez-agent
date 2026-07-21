package main

import (
	"context"
	"fmt"
	"strings"

	ezmcp "github.com/sven-victor/ez-agent/tool/mcp"
)

// mcpConfig holds optional MCP server endpoints from CLI flags.
type mcpConfig struct {
	Stdio []string // each: "command arg1 arg2..."
	HTTP  []string
	SSE   []string
}

func (c mcpConfig) empty() bool {
	return len(c.Stdio) == 0 && len(c.HTTP) == 0 && len(c.SSE) == 0
}

type mcpBundle struct {
	clients []*ezmcp.Client
}

func (b *mcpBundle) Close() {
	for _, c := range b.clients {
		_ = c.Close()
	}
}

func connectMCP(ctx context.Context, cfg mcpConfig) (*mcpBundle, error) {
	b := &mcpBundle{}
	add := func(c *ezmcp.Client, err error, label string) error {
		if err != nil {
			b.Close()
			return fmt.Errorf("%s: %w", label, err)
		}
		b.clients = append(b.clients, c)
		return nil
	}

	for i, raw := range cfg.Stdio {
		cmd, args, err := splitCommand(raw)
		if err != nil {
			b.Close()
			return nil, fmt.Errorf("mcp-stdio[%d]: %w", i, err)
		}
		c, err := ezmcp.DialStdio(ctx, cmd, args...)
		if err := add(c, err, fmt.Sprintf("mcp-stdio %q", raw)); err != nil {
			return nil, err
		}
	}
	for _, url := range cfg.HTTP {
		c, err := ezmcp.DialHTTP(ctx, url)
		if err := add(c, err, fmt.Sprintf("mcp-http %q", url)); err != nil {
			return nil, err
		}
	}
	for _, url := range cfg.SSE {
		c, err := ezmcp.DialSSE(ctx, url)
		if err := add(c, err, fmt.Sprintf("mcp-sse %q", url)); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func splitCommand(raw string) (string, []string, error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("empty command")
	}
	return fields[0], fields[1:], nil
}
