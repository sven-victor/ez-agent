package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/proxy"
)

// parseProxyURL validates and normalizes a proxy URL.
// Supported schemes: http, https, socks5, socks5h.
func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty proxy URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxy URL missing host")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	case "":
		return nil, fmt.Errorf("proxy URL missing scheme (want http|https|socks5|socks5h)")
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (want http|https|socks5|socks5h)", u.Scheme)
	}
}

// httpClientForProxy builds an *http.Client that dials through the given proxy URL.
func httpClientForProxy(raw string) (*http.Client, error) {
	u, err := parseProxyURL(raw)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		d, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks proxy: %w", err)
		}
		transport.Proxy = nil
		if cd, ok := d.(proxy.ContextDialer); ok {
			transport.DialContext = cd.DialContext
		} else {
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return d.Dial(network, addr)
			}
		}
	}
	return &http.Client{Transport: transport}, nil
}
