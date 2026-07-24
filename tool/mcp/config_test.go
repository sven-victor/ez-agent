package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	ezmcp "github.com/sven-victor/ez-agent/tool/mcp"
)

func TestParseConfigHTTPWithTypeAndHeaders(t *testing.T) {
	cfg, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "devops": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer secret"
      }
    }
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := cfg.MCPServers["devops"]
	if !ok {
		t.Fatal("missing devops")
	}
	if srv.URL != "https://example.com/mcp" {
		t.Fatalf("url=%q", srv.URL)
	}
	if srv.Type != "http" {
		t.Fatalf("type=%q", srv.Type)
	}
	if srv.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("headers=%v", srv.Headers)
	}
}

func TestParseConfigStdioAndTransport(t *testing.T) {
	cfg, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "weather": {
      "url": "https://weather.example.com/mcp",
      "transport": "http"
    },
    "assistant": {
      "command": "python",
      "args": ["./assistant.py"],
      "env": {"LOG_LEVEL": "INFO"},
      "cwd": "/tmp"
    },
    "legacy": {
      "url": "https://example.com/sse",
      "transport": "sse"
    }
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 3 {
		t.Fatalf("servers=%d", len(cfg.MCPServers))
	}
	a := cfg.MCPServers["assistant"]
	if a.Command != "python" || len(a.Args) != 1 || a.Env["LOG_LEVEL"] != "INFO" || a.Cwd != "/tmp" {
		t.Fatalf("assistant=%+v", a)
	}
}

func TestParseConfigInferSSEFromURL(t *testing.T) {
	cfg, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "s": {"url": "https://example.com/sse"}
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	// DialServer uses infer; ensure parse accepts omit transport.
	if cfg.MCPServers["s"].URL == "" {
		t.Fatal("missing url")
	}
}

func TestParseConfigRootServersWrapped(t *testing.T) {
	cfg, err := ezmcp.ParseConfig([]byte(`{
  "only": {"url": "https://example.com/mcp"}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCPServers["only"]; !ok {
		t.Fatalf("servers=%v", cfg.MCPServers)
	}
}

func TestParseConfigAuthOAuthRejected(t *testing.T) {
	_, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "s": {"url": "https://example.com/mcp", "auth": "oauth"}
  }
}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseConfigEmptyRejected(t *testing.T) {
	_, err := ezmcp.ParseConfig([]byte(`{"mcpServers": {}}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
  "mcpServers": {
    "devops": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": {"Authorization": "Bearer x"}
    }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ezmcp.LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["devops"].Headers["Authorization"] != "Bearer x" {
		t.Fatalf("%+v", cfg.MCPServers["devops"])
	}
}

func TestParseConfigBearerAuthField(t *testing.T) {
	cfg, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "s": {"url": "https://example.com/mcp", "auth": "tok"}
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["s"].Auth != "tok" {
		t.Fatalf("auth=%q", cfg.MCPServers["s"].Auth)
	}
}

func TestParseConfigProxyHTTPAndSOCKS5(t *testing.T) {
	cfg, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "http_proxy": {
      "type": "http",
      "url": "https://example.com/mcp",
      "proxy": "http://127.0.0.1:7890"
    },
    "socks": {
      "type": "sse",
      "url": "https://example.com/sse",
      "proxy": "socks5://user:pass@127.0.0.1:1080"
    }
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["http_proxy"].Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("http proxy=%q", cfg.MCPServers["http_proxy"].Proxy)
	}
	if cfg.MCPServers["socks"].Proxy != "socks5://user:pass@127.0.0.1:1080" {
		t.Fatalf("socks proxy=%q", cfg.MCPServers["socks"].Proxy)
	}
}

func TestParseConfigProxyRejectedOnStdio(t *testing.T) {
	_, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "local": {
      "command": "npx",
      "args": ["-y", "x"],
      "proxy": "http://127.0.0.1:7890"
    }
  }
}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseConfigProxyBadScheme(t *testing.T) {
	_, err := ezmcp.ParseConfig([]byte(`{
  "mcpServers": {
    "s": {"url": "https://example.com/mcp", "proxy": "ftp://127.0.0.1:21"}
  }
}`))
	if err == nil {
		t.Fatal("expected error")
	}
}
