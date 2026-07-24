package mcp

import (
	"net/http"
	"testing"
)

func TestHTTPClientForProxyHTTP(t *testing.T) {
	c, err := httpClientForProxy("http://127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("expected transport with Proxy, got %#v", c.Transport)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	u, err := tr.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "127.0.0.1:7890" {
		t.Fatalf("proxy url=%v", u)
	}
}

func TestHTTPClientForProxySOCKS5(t *testing.T) {
	c, err := httpClientForProxy("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatalf("expected socks DialContext, got %#v", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("socks client should not set HTTP Proxy")
	}
}

func TestParseProxyURLSchemes(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:7890",
		"https://proxy.example:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:1080",
	} {
		if _, err := parseProxyURL(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "://nohost", "ftp://x", "127.0.0.1:7890"} {
		if _, err := parseProxyURL(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}
