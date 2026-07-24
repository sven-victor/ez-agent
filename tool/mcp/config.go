package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Config is the canonical MCP client configuration (mcpServers map).
// Compatible with FastMCP / Cursor / Claude Desktop style JSON:
//
//	{
//	  "mcpServers": {
//	    "devops": {
//	      "type": "http",
//	      "url": "https://example.com/mcp",
//	      "headers": {"Authorization": "Bearer ..."},
//	      "proxy": "socks5://127.0.0.1:1080"
//	    },
//	    "local": {
//	      "command": "npx",
//	      "args": ["-y", "@modelcontextprotocol/server-everything"],
//	      "env": {"FOO": "bar"}
//	    }
//	  }
//	}
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ServerConfig describes one MCP server entry.
// Remote servers use url (+ optional headers / auth / proxy / transport|type).
// Stdio servers use command (+ optional args / env / cwd).
type ServerConfig struct {
	// Stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`

	// Remote
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Auth is a bearer token string, or the literal "oauth" (unsupported here).
	Auth string `json:"auth,omitempty"`
	// Proxy is an optional proxy URL for HTTP / SSE transports.
	// Supported schemes: http, https, socks5, socks5h
	// (e.g. "http://127.0.0.1:7890", "socks5://user:pass@127.0.0.1:1080").
	Proxy string `json:"proxy,omitempty"`

	// Transport is "stdio" | "http" | "streamable-http" | "sse".
	// Type is an alias used by some clients (e.g. Cursor).
	Transport string `json:"transport,omitempty"`
	Type      string `json:"type,omitempty"`

	// Optional metadata (ignored when dialing).
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Timeout     *int   `json:"timeout,omitempty"`
}

// LoadConfigFile reads and parses an MCP JSON config file.
func LoadConfigFile(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(raw)
}

// ParseConfig unmarshals MCP JSON config bytes.
// If mcpServers is missing but root values look like server configs, they are wrapped.
func ParseConfig(raw []byte) (Config, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return Config{}, fmt.Errorf("empty MCP config")
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse MCP config: %w", err)
	}
	if len(cfg.MCPServers) == 0 {
		// FastMCP-style: servers at root → treat root as mcpServers.
		var root map[string]ServerConfig
		if err := json.Unmarshal(raw, &root); err != nil {
			return Config{}, fmt.Errorf("no MCP servers defined in config")
		}
		delete(root, "$schema")
		servers := make(map[string]ServerConfig)
		for name, srv := range root {
			if strings.TrimSpace(srv.Command) == "" && strings.TrimSpace(srv.URL) == "" {
				continue
			}
			servers[name] = srv
		}
		if len(servers) == 0 {
			return Config{}, fmt.Errorf("no MCP servers defined in config")
		}
		cfg.MCPServers = servers
	}
	for name, srv := range cfg.MCPServers {
		if err := srv.validate(name); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func (s ServerConfig) validate(name string) error {
	kind, err := s.kind()
	if err != nil {
		return fmt.Errorf("mcp server %q: %w", name, err)
	}
	switch kind {
	case transportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("mcp server %q: stdio requires command", name)
		}
		if strings.TrimSpace(s.Proxy) != "" {
			return fmt.Errorf("mcp server %q: proxy is only supported for http/sse transports", name)
		}
	case transportHTTP, transportSSE:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("mcp server %q: %s requires url", name, kind)
		}
	}
	if s.Auth == "oauth" {
		return fmt.Errorf("mcp server %q: auth \"oauth\" is not supported", name)
	}
	if p := strings.TrimSpace(s.Proxy); p != "" {
		if _, err := parseProxyURL(p); err != nil {
			return fmt.Errorf("mcp server %q: %w", name, err)
		}
	}
	return nil
}

type transportKind string

const (
	transportStdio transportKind = "stdio"
	transportHTTP  transportKind = "http"
	transportSSE   transportKind = "sse"
)

func (s ServerConfig) kind() (transportKind, error) {
	t := strings.ToLower(strings.TrimSpace(s.Transport))
	if t == "" {
		t = strings.ToLower(strings.TrimSpace(s.Type))
	}
	switch t {
	case "stdio":
		return transportStdio, nil
	case "http", "streamable-http", "streamable_http":
		return transportHTTP, nil
	case "sse":
		return transportSSE, nil
	case "":
		if strings.TrimSpace(s.Command) != "" {
			return transportStdio, nil
		}
		if strings.TrimSpace(s.URL) == "" {
			return "", fmt.Errorf("need command (stdio) or url (http/sse)")
		}
		return inferTransportFromURL(s.URL), nil
	default:
		return "", fmt.Errorf("unknown transport %q", t)
	}
}

func inferTransportFromURL(raw string) transportKind {
	u, err := url.Parse(raw)
	if err != nil {
		return transportHTTP
	}
	// FastMCP: path contains a "/sse" segment → SSE, else streamable HTTP.
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "sse" {
			return transportSSE
		}
	}
	return transportHTTP
}

func (s ServerConfig) dialOptions(name string) []Option {
	opts := []Option{WithPrefix(name + "_")}
	if len(s.Headers) > 0 {
		opts = append(opts, WithHeaders(s.Headers))
	}
	if s.Auth != "" && s.Auth != "oauth" {
		opts = append(opts, WithBearerAuth(s.Auth))
	}
	if p := strings.TrimSpace(s.Proxy); p != "" {
		opts = append(opts, WithProxy(p))
	}
	if len(s.Env) > 0 {
		opts = append(opts, WithEnv(s.Env))
	}
	if s.Cwd != "" {
		opts = append(opts, WithCwd(s.Cwd))
	}
	return opts
}
