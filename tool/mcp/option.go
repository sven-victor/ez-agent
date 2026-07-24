package mcp

// Option configures Dial* / DialServer.
type Option func(*dialOpts)

type dialOpts struct {
	headers map[string]string
	auth    string // bearer token (sets Authorization if not already set)
	env     map[string]string
	cwd     string
	prefix  string
	proxy   string // http/https/socks5/socks5h URL for HTTP / SSE
}

// WithHeaders sets static HTTP headers (HTTP / SSE).
func WithHeaders(headers map[string]string) Option {
	return func(o *dialOpts) {
		if len(headers) == 0 {
			return
		}
		if o.headers == nil {
			o.headers = make(map[string]string, len(headers))
		}
		for k, v := range headers {
			o.headers[k] = v
		}
	}
}

// WithBearerAuth sets Authorization: Bearer <token> unless Authorization is already present.
func WithBearerAuth(token string) Option {
	return func(o *dialOpts) { o.auth = token }
}

// WithProxy sets an HTTP or SOCKS5 proxy URL for HTTP / SSE transports.
// Examples: "http://127.0.0.1:7890", "socks5://127.0.0.1:1080".
func WithProxy(proxyURL string) Option {
	return func(o *dialOpts) { o.proxy = proxyURL }
}

// WithEnv sets extra environment variables for a stdio server (KEY=value overlay).
func WithEnv(env map[string]string) Option {
	return func(o *dialOpts) { o.env = env }
}

// WithCwd sets the working directory for a stdio server process.
func WithCwd(cwd string) Option {
	return func(o *dialOpts) { o.cwd = cwd }
}

// WithPrefix prefixes tool names exposed via Registry (e.g. "devops_").
func WithPrefix(prefix string) Option {
	return func(o *dialOpts) { o.prefix = prefix }
}

func applyOptions(opts []Option) dialOpts {
	var o dialOpts
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

func (o dialOpts) httpHeaders() map[string]string {
	if o.auth == "" && len(o.headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(o.headers)+1)
	for k, v := range o.headers {
		out[k] = v
	}
	if o.auth != "" {
		if _, ok := out["Authorization"]; !ok {
			out["Authorization"] = "Bearer " + o.auth
		}
	}
	return out
}

func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
