package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/tool"
)

// Client wraps an MCP session and exposes tools as a tool.Registry.
type Client struct {
	inner  *mcpclient.Client
	mu     sync.RWMutex
	specs  []tool.Spec
	byName map[string]tool.Spec
	prefix string
	closed bool
}

// DialStdio connects to an MCP server over stdio (command + args).
func DialStdio(ctx context.Context, command string, args ...string) (*Client, error) {
	return DialStdioOpts(ctx, command, args)
}

// DialStdioOpts connects over stdio with optional env / cwd / prefix.
func DialStdioOpts(ctx context.Context, command string, args []string, opts ...Option) (*Client, error) {
	o := applyOptions(opts)
	env := envSlice(o.env)

	var inner *mcpclient.Client
	var err error
	if o.cwd != "" {
		cwd := o.cwd
		inner, err = mcpclient.NewStdioMCPClientWithOptions(command, env, args, transport.WithCommandFunc(
			func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, command, args...)
				cmd.Env = append(os.Environ(), env...)
				cmd.Dir = cwd
				return cmd, nil
			},
		))
	} else {
		inner, err = mcpclient.NewStdioMCPClient(command, env, args...)
	}
	if err != nil {
		return nil, err
	}
	return wrapAndInit(ctx, inner, o.prefix)
}

// DialHTTP connects to an MCP server over streamable HTTP.
func DialHTTP(ctx context.Context, url string, opts ...Option) (*Client, error) {
	o := applyOptions(opts)
	var httpOpts []transport.StreamableHTTPCOption
	if h := o.httpHeaders(); len(h) > 0 {
		httpOpts = append(httpOpts, transport.WithHTTPHeaders(h))
	}
	inner, err := mcpclient.NewStreamableHttpClient(url, httpOpts...)
	if err != nil {
		return nil, err
	}
	if err := inner.Start(ctx); err != nil {
		_ = inner.Close()
		return nil, err
	}
	return wrapAndInit(ctx, inner, o.prefix)
}

// DialSSE connects to an MCP server over SSE transport.
func DialSSE(ctx context.Context, url string, opts ...Option) (*Client, error) {
	o := applyOptions(opts)
	var sseOpts []transport.ClientOption
	if h := o.httpHeaders(); len(h) > 0 {
		sseOpts = append(sseOpts, mcpclient.WithHeaders(h))
	}
	inner, err := mcpclient.NewSSEMCPClient(url, sseOpts...)
	if err != nil {
		return nil, err
	}
	if err := inner.Start(ctx); err != nil {
		_ = inner.Close()
		return nil, err
	}
	return wrapAndInit(ctx, inner, o.prefix)
}

// DialServer connects using a named ServerConfig from an MCP JSON config.
// Tool names are prefixed with "<name>_".
func DialServer(ctx context.Context, name string, srv ServerConfig) (*Client, error) {
	kind, err := srv.kind()
	if err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	opts := srv.dialOptions(name)
	switch kind {
	case transportStdio:
		return DialStdioOpts(ctx, srv.Command, srv.Args, opts...)
	case transportHTTP:
		return DialHTTP(ctx, srv.URL, opts...)
	case transportSSE:
		return DialSSE(ctx, srv.URL, opts...)
	default:
		return nil, fmt.Errorf("mcp server %q: unsupported transport %q", name, kind)
	}
}

// DialConfig connects to every server in cfg. On any failure, already-opened clients are closed.
// Servers are dialed in sorted name order.
func DialConfig(ctx context.Context, cfg Config) ([]*Client, error) {
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	clients := make([]*Client, 0, len(names))
	for _, name := range names {
		c, err := DialServer(ctx, name, cfg.MCPServers[name])
		if err != nil {
			for _, opened := range clients {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		clients = append(clients, c)
	}
	return clients, nil
}

// DialInProcess wraps an already-constructed mcp-go client (e.g. in-process for tests).
func DialInProcess(ctx context.Context, inner *mcpclient.Client, prefix string) (*Client, error) {
	if err := inner.Start(ctx); err != nil {
		return nil, err
	}
	return wrapAndInit(ctx, inner, prefix)
}

func wrapAndInit(ctx context.Context, inner *mcpclient.Client, prefix string) (*Client, error) {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "ez-agent", Version: "0.1.0"}
	if _, err := inner.Initialize(ctx, initReq); err != nil {
		_ = inner.Close()
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	c := &Client{
		inner:  inner,
		prefix: prefix,
		byName: make(map[string]tool.Spec),
	}
	if err := c.refreshTools(ctx); err != nil {
		_ = inner.Close()
		return nil, err
	}
	return c, nil
}

// Close shuts down the MCP session.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.inner.Close()
}

// Registry returns this client as a tool.Registry.
func (c *Client) Registry() tool.Registry { return c }

// RefreshTools re-lists tools from the MCP server.
// When the client is attached via agent.WithToolRegistry, the next model call
// sees the updated Specs without reconstructing the Agent.
func (c *Client) RefreshTools(ctx context.Context) error {
	return c.refreshTools(ctx)
}

// Specs implements tool.Registry.
func (c *Client) Specs() []tool.Spec {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]tool.Spec, len(c.specs))
	copy(out, c.specs)
	return out
}

// Lookup implements tool.Registry.
func (c *Client) Lookup(name string) (tool.Handler, bool) {
	c.mu.RLock()
	_, ok := c.byName[name]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	mcpName := name
	if c.prefix != "" && len(name) > len(c.prefix) && name[:len(c.prefix)] == c.prefix {
		mcpName = name[len(c.prefix):]
	}
	return func(ctx context.Context, call tool.Call) (tool.Result, error) {
		return c.callTool(ctx, call, mcpName)
	}, true
}

func (c *Client) refreshTools(ctx context.Context) error {
	res, err := c.inner.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("mcp list tools: %w", err)
	}
	specs := make([]tool.Spec, 0, len(res.Tools))
	byName := make(map[string]tool.Spec, len(res.Tools))
	for _, t := range res.Tools {
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			return err
		}
		name := t.Name
		if c.prefix != "" {
			name = c.prefix + name
		}
		spec := tool.Spec{
			Name:        name,
			Description: t.Description,
			InputSchema: schema,
		}
		specs = append(specs, spec)
		byName[name] = spec
	}
	c.mu.Lock()
	c.specs = specs
	c.byName = byName
	c.mu.Unlock()
	return nil
}

func (c *Client) callTool(ctx context.Context, call tool.Call, mcpName string) (tool.Result, error) {
	var args any
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &args); err != nil {
			return tool.Result{
				ToolUseID: call.ID,
				OK:        false,
				Content:   []message.Part{message.Text{Text: "invalid tool input: " + err.Error()}},
			}, nil
		}
	} else {
		args = map[string]any{}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = mcpName
	req.Params.Arguments = args
	res, err := c.inner.CallTool(ctx, req)
	if err != nil {
		return tool.Result{
			ToolUseID: call.ID,
			OK:        false,
			Content:   []message.Part{message.Text{Text: err.Error()}},
			Err:       err,
		}, nil
	}
	parts := contentToParts(res.Content)
	if len(parts) == 0 && res.IsError {
		parts = []message.Part{message.Text{Text: "tool error"}}
	}
	return tool.Result{
		ToolUseID: call.ID,
		OK:        !res.IsError,
		Content:   parts,
	}, nil
}

func contentToParts(contents []mcp.Content) []message.Part {
	var parts []message.Part
	for _, c := range contents {
		switch v := c.(type) {
		case mcp.TextContent:
			parts = append(parts, message.Text{Text: v.Text})
		case *mcp.TextContent:
			parts = append(parts, message.Text{Text: v.Text})
		case mcp.ImageContent:
			raw, err := base64.StdEncoding.DecodeString(v.Data)
			if err != nil {
				parts = append(parts, message.Text{Text: "[invalid image data]"})
				continue
			}
			parts = append(parts, message.Image{Blob: message.Blob{MIME: v.MIMEType, Bytes: raw}})
		case *mcp.ImageContent:
			raw, err := base64.StdEncoding.DecodeString(v.Data)
			if err != nil {
				parts = append(parts, message.Text{Text: "[invalid image data]"})
				continue
			}
			parts = append(parts, message.Image{Blob: message.Blob{MIME: v.MIMEType, Bytes: raw}})
		default:
			parts = append(parts, message.Text{Text: fmt.Sprintf("[%T]", c)})
		}
	}
	return parts
}
