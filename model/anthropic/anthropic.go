package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

const (
	defaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2023-06-01"
	defaultMaxTok  = 4096
)

// Provider is an Anthropic Messages API adapter.
type Provider struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	caps       model.Capabilities
	headers    map[string]string // extra HTTP headers (applied after built-ins)
	log        logger.Logger
}

// Option configures the provider.
type Option func(*Provider)

func WithAPIKey(key string) Option {
	return func(p *Provider) { p.apiKey = key }
}

// WithHeader sets a custom HTTP header on every request.
// Applied after built-in headers, so it may override them.
// Multiple calls accumulate; later values for the same key win.
func WithHeader(key, value string) Option {
	return func(p *Provider) {
		if key == "" {
			return
		}
		if p.headers == nil {
			p.headers = map[string]string{}
		}
		p.headers[key] = value
	}
}

func WithBaseURL(url string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(url, "/") }
}

func WithModel(name string) Option {
	return func(p *Provider) { p.model = name }
}

func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.httpClient = c }
}

func WithCaps(c model.Capabilities) Option {
	return func(p *Provider) { p.caps = c }
}

// WithLogger sets the diagnostic logger. Nil discards logs.
func WithLogger(l logger.Logger) Option {
	return func(p *Provider) { p.log = l }
}

// New creates an Anthropic Messages provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		baseURL:    defaultBaseURL,
		model:      "claude-sonnet-4-20250514",
		httpClient: http.DefaultClient,
		log:        logger.FromSlog(slog.Default()),
		caps: model.Capabilities{
			TextIn:      true,
			ImageIn:     true,
			DocumentIn:  true,
			Tools:       true,
			Streaming:   true,
			SystemParts: true,
			JSONSchema:  true,
			Opaque:      true, // round-trip unknown content blocks
			// AudioIn/VideoIn: Messages API has no native audio/video content blocks.
		},
	}
	for _, o := range opts {
		o(p)
	}
	if p.log == nil {
		p.log = logger.Nop()
	}
	return p
}

func (p *Provider) Name() string             { return "anthropic" }
func (p *Provider) Caps() model.Capabilities { return p.caps }

// Stream runs a Messages API request with stream=true.
func (p *Provider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	rawReq, resp, err := p.doMessages(ctx, req, true)
	if err != nil {
		return model.Result{RawRequest: rawReq}, err
	}
	defer resp.Body.Close()
	res, err := decodeStream(resp.Body, yield)
	res.RawRequest = rawReq
	return res, err
}

// Complete runs a Messages API request with stream=false.
func (p *Provider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	rawReq, resp, err := p.doMessages(ctx, req, false)
	if err != nil {
		return model.Result{RawRequest: rawReq}, err
	}
	defer resp.Body.Close()
	res, err := decodeComplete(resp.Body)
	res.RawRequest = rawReq
	return res, err
}

func (p *Provider) doMessages(ctx context.Context, req model.Request, stream bool) ([]byte, *http.Response, error) {
	if err := model.Validate(p, req); err != nil {
		return nil, nil, err
	}
	body, err := encodeRequest(p.model, req, stream, p.log)
	if err != nil {
		return nil, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return body, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if p.apiKey != "" {
		httpReq.Header.Set("x-api-key", p.apiKey)
	}
	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return body, nil, err
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if isOverflow(resp.StatusCode, b) {
			return body, nil, fmt.Errorf("%w: %s", model.ErrOverflow, string(b))
		}
		if isThrottled(resp.StatusCode, b) {
			return body, nil, fmt.Errorf("%w: %s", model.ErrThrottled, string(b))
		}
		return body, nil, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return body, resp, nil
}

func isOverflow(code int, body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "too long") ||
		strings.Contains(s, "context") && strings.Contains(s, "limit") ||
		strings.Contains(s, "prompt is too long") ||
		strings.Contains(s, "max_tokens") && strings.Contains(s, "exceed")
}

func isThrottled(code int, body []byte) bool {
	if code == 429 {
		return true
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "rate_limit") || strings.Contains(s, "overloaded")
}

type apiRequest struct {
	Model        string         `json:"model"`
	MaxTokens    int            `json:"max_tokens"`
	Messages     []wireMessage  `json:"messages"`
	System       any            `json:"system,omitempty"`
	Tools        []wireTool     `json:"tools,omitempty"`
	ToolChoice   any            `json:"tool_choice,omitempty"`
	OutputConfig map[string]any `json:"output_config,omitempty"`
	Stream       bool           `json:"stream"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type wireTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

const maxCacheBreakpoints = 4

// cacheCursor walks system, tools, then messages. A breakpoint marks the latest block.
// Anthropic accepts at most 4 cache_control breakpoints. Extra markers are dropped.
type cacheCursor struct {
	n        int
	dropped  int
	block    map[string]any
	tools    *[]wireTool
	toolIdx  int
	hasBlock bool
	hasTool  bool
}

func (c *cacheCursor) noteBlock(b map[string]any) {
	if b == nil {
		return
	}
	c.block = b
	c.hasBlock = true
	c.hasTool = false
}

func (c *cacheCursor) noteTool(tools *[]wireTool, idx int) {
	c.tools = tools
	c.toolIdx = idx
	c.hasTool = true
	c.hasBlock = false
}

func (c *cacheCursor) breakpoint() error {
	if c.n >= maxCacheBreakpoints {
		c.dropped++
		return nil
	}
	switch {
	case c.hasBlock:
		c.block["cache_control"] = map[string]any{"type": "ephemeral"}
	case c.hasTool:
		(*c.tools)[c.toolIdx].CacheControl = &cacheControl{Type: "ephemeral"}
	default:
		return fmt.Errorf("anthropic: cache breakpoint has no preceding block")
	}
	c.n++
	return nil
}

func (c *cacheCursor) warnTruncation(log logger.Logger) {
	if c.dropped == 0 {
		return
	}
	logger.OrNop(log).Warn("anthropic cache breakpoints exceed limit; kept the first ones",
		"kept", c.n,
		"dropped", c.dropped,
		"limit", maxCacheBreakpoints,
	)
}

func encodeRequest(modelName string, req model.Request, stream bool, log logger.Logger) ([]byte, error) {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = defaultMaxTok
	}
	cur := &cacheCursor{}
	ar := apiRequest{
		Model:     modelName,
		MaxTokens: maxTok,
		Stream:    stream,
	}
	if sys, err := encodeSystem(req.System, cur); err != nil {
		return nil, err
	} else if sys != nil {
		ar.System = sys
	}
	for _, t := range req.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		ar.Tools = append(ar.Tools, wireTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
		cur.noteTool(&ar.Tools, len(ar.Tools)-1)
		if t.CacheBreakpoint {
			if err := cur.breakpoint(); err != nil {
				return nil, err
			}
		}
	}
	msgs, err := encodeMessages(req.Messages, cur)
	if err != nil {
		return nil, err
	}
	ar.Messages = msgs
	if tc := encodeToolChoice(req.ToolChoice); tc != nil {
		ar.ToolChoice = tc
	}
	if len(req.OutputSchema) > 0 {
		var schema any
		if err := json.Unmarshal(req.OutputSchema, &schema); err != nil {
			return nil, fmt.Errorf("anthropic: invalid OutputSchema: %w", err)
		}
		ar.OutputConfig = map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"schema": schema,
			},
		}
	}
	cur.warnTruncation(log)
	return json.Marshal(ar)
}

func encodeToolChoice(c tool.Choice) any {
	switch strings.ToLower(c.Type) {
	case "", "auto":
		if c.Name == "" {
			return nil
		}
		return map[string]any{"type": "tool", "name": c.Name}
	case "none":
		return map[string]any{"type": "none"}
	case "required", "any":
		return map[string]any{"type": "any"}
	case "named", "tool":
		return map[string]any{"type": "tool", "name": c.Name}
	default:
		return nil
	}
}

func encodeSystem(sys model.System, cur *cacheCursor) (any, error) {
	if sys.Text == "" && len(sys.Parts) == 0 {
		return nil, nil
	}
	if len(sys.Parts) == 0 {
		return sys.Text, nil
	}
	blocks := make([]map[string]any, 0, len(sys.Parts)+1)
	if sys.Text != "" {
		b := map[string]any{"type": "text", "text": sys.Text}
		blocks = append(blocks, b)
		cur.noteBlock(b)
	}
	for _, p := range sys.Parts {
		if _, ok := p.(message.CacheBreakpoint); ok {
			if err := cur.breakpoint(); err != nil {
				return nil, err
			}
			continue
		}
		b, err := encodeContentPart(p)
		if err != nil {
			return nil, err
		}
		if b != nil {
			blocks = append(blocks, b)
			cur.noteBlock(b)
		}
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	return blocks, nil
}

func encodeMessages(msgs []message.Message, cur *cacheCursor) ([]wireMessage, error) {
	var out []wireMessage
	for _, m := range msgs {
		switch m.Role {
		case message.RoleAssistant:
			blocks, err := encodeAssistantBlocks(m, cur)
			if err != nil {
				return nil, err
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, wireMessage{Role: "assistant", Content: blocks})
		case message.RoleUser:
			blocks, err := encodeUserBlocks(m, cur)
			if err != nil {
				return nil, err
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, wireMessage{Role: "user", Content: blocks})
		default:
			return nil, fmt.Errorf("anthropic: unsupported role %q", m.Role)
		}
	}
	return out, nil
}

func encodeAssistantBlocks(m message.Message, cur *cacheCursor) ([]map[string]any, error) {
	var blocks []map[string]any
	sawBreakpoint := false
	for _, p := range m.Parts {
		switch v := p.(type) {
		case message.CacheBreakpoint:
			sawBreakpoint = true
			if err := cur.breakpoint(); err != nil {
				return nil, err
			}
		case message.Text:
			if v.Text != "" {
				b := map[string]any{"type": "text", "text": v.Text}
				blocks = append(blocks, b)
				cur.noteBlock(b)
			}
		case message.ToolUse:
			var input any = map[string]any{}
			if len(v.Input) > 0 {
				if err := json.Unmarshal(v.Input, &input); err != nil {
					input = json.RawMessage(v.Input)
				}
			}
			b := map[string]any{
				"type":  "tool_use",
				"id":    v.ID,
				"name":  v.Name,
				"input": input,
			}
			blocks = append(blocks, b)
			cur.noteBlock(b)
		case message.Reasoning:
			block := map[string]any{"type": "thinking", "thinking": v.Text}
			if v.Signature != "" {
				block["signature"] = v.Signature
			}
			blocks = append(blocks, block)
			cur.noteBlock(block)
		case message.Opaque:
			if v.Vendor != "" && v.Vendor != "anthropic" {
				return nil, fmt.Errorf("anthropic: opaque vendor %q not supported", v.Vendor)
			}
			var block map[string]any
			if err := json.Unmarshal(v.Data, &block); err != nil {
				return nil, fmt.Errorf("anthropic: opaque data: %w", err)
			}
			blocks = append(blocks, block)
			cur.noteBlock(block)
		default:
			return nil, fmt.Errorf("anthropic: unsupported assistant part %T", p)
		}
	}
	if len(blocks) == 0 && !sawBreakpoint {
		b := map[string]any{"type": "text", "text": ""}
		blocks = append(blocks, b)
		cur.noteBlock(b)
	}
	return blocks, nil
}

func encodeUserBlocks(m message.Message, cur *cacheCursor) ([]map[string]any, error) {
	var blocks []map[string]any
	for _, p := range m.Parts {
		switch v := p.(type) {
		case message.CacheBreakpoint:
			if err := cur.breakpoint(); err != nil {
				return nil, err
			}
		case message.ToolResult:
			content := message.TextContent(message.Message{Parts: v.Parts})
			if content == "" && !v.OK {
				content = "tool failed"
			}
			// Prefer structured content array when media present.
			var contentVal any = content
			hasMedia := false
			for _, np := range v.Parts {
				if _, ok := np.(message.Image); ok {
					hasMedia = true
					break
				}
			}
			if hasMedia {
				var nested []map[string]any
				for _, np := range v.Parts {
					b, err := encodeContentPart(np)
					if err != nil {
						return nil, err
					}
					if b != nil {
						nested = append(nested, b)
					}
				}
				contentVal = nested
			}
			b := map[string]any{
				"type":        "tool_result",
				"tool_use_id": v.ToolUseID,
				"content":     contentVal,
				"is_error":    !v.OK,
			}
			blocks = append(blocks, b)
			cur.noteBlock(b)
		default:
			b, err := encodeContentPart(p)
			if err != nil {
				return nil, err
			}
			if b != nil {
				blocks = append(blocks, b)
				cur.noteBlock(b)
			}
		}
	}
	return blocks, nil
}

func encodeContentPart(p message.Part) (map[string]any, error) {
	switch v := p.(type) {
	case message.Text:
		if v.Text == "" {
			return nil, nil
		}
		return map[string]any{"type": "text", "text": v.Text}, nil
	case message.Image:
		src, err := encodeImageSource(v.Blob)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "image", "source": src}, nil
	case message.Document:
		src, err := encodeImageSource(v.Blob) // same base64/url shape; type document for PDF
		if err != nil {
			return nil, err
		}
		if strings.Contains(strings.ToLower(v.Blob.MIME), "pdf") {
			return map[string]any{"type": "document", "source": src}, nil
		}
		return map[string]any{"type": "image", "source": src}, nil
	case message.CacheBreakpoint:
		return nil, fmt.Errorf("anthropic: cache breakpoint must sit beside a content block, not inside tool result content")
	case message.Audio, message.Video:
		return nil, fmt.Errorf("anthropic: %T not supported (no native audio/video content blocks)", p)
	case message.Opaque:
		if v.Vendor != "" && v.Vendor != "anthropic" {
			return nil, fmt.Errorf("anthropic: opaque vendor %q not supported", v.Vendor)
		}
		var block map[string]any
		if err := json.Unmarshal(v.Data, &block); err != nil {
			return nil, fmt.Errorf("anthropic: opaque data: %w", err)
		}
		return block, nil
	default:
		return nil, fmt.Errorf("anthropic: unsupported content part %T", p)
	}
}

func encodeImageSource(b message.Blob) (map[string]any, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if b.URL != "" && (strings.HasPrefix(b.URL, "http://") || strings.HasPrefix(b.URL, "https://")) {
		return map[string]any{"type": "url", "url": b.URL}, nil
	}
	raw, mime, err := loadBytes(b)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":       "base64",
		"media_type": mime,
		"data":       base64.StdEncoding.EncodeToString(raw),
	}, nil
}

func loadBytes(b message.Blob) ([]byte, string, error) {
	mime := b.MIME
	if mime == "" {
		mime = "image/png"
	}
	if len(b.Bytes) > 0 {
		return b.Bytes, mime, nil
	}
	if b.Path != "" {
		raw, err := os.ReadFile(b.Path)
		if err != nil {
			return nil, "", err
		}
		return raw, mime, nil
	}
	if b.URL != "" && strings.HasPrefix(b.URL, "data:") {
		// data:image/png;base64,...
		parts := strings.SplitN(b.URL, ",", 2)
		if len(parts) != 2 {
			return nil, "", fmt.Errorf("anthropic: bad data url")
		}
		raw, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, "", err
		}
		return raw, mime, nil
	}
	return nil, "", fmt.Errorf("anthropic: blob has no Bytes or Path")
}

type streamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	ContentBlock *struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage *anthropicUsage `json:"usage"`
}

// anthropicUsage is one Messages API usage object.
// input_tokens excludes cache read and cache write; those are added into InputTokens.
type anthropicUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

// applyAnthropicUsage merges one usage event into dst.
// Missing fields keep the previous value so a message_delta that only
// reports output_tokens does not clear cache counts from message_start.
func applyAnthropicUsage(dst *run.Usage, u *anthropicUsage) {
	if u == nil {
		return
	}
	base := dst.InputTokens - dst.CacheReadTokens - dst.CacheWriteTokens
	if base < 0 {
		base = 0
	}
	if u.InputTokens != nil {
		base = *u.InputTokens
	}
	if u.CacheReadInputTokens != nil {
		dst.CacheReadTokens = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		dst.CacheWriteTokens = *u.CacheCreationInputTokens
	}
	if u.InputTokens != nil || u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil {
		dst.InputTokens = base + dst.CacheReadTokens + dst.CacheWriteTokens
	}
	if u.OutputTokens != nil {
		dst.OutputTokens = *u.OutputTokens
	}
	dst.TotalTokens = dst.InputTokens + dst.OutputTokens
}

type blockAcc struct {
	typ       string
	id        string
	name      string
	text      strings.Builder
	json      strings.Builder
	thinking  strings.Builder
	signature string
	raw       json.RawMessage // unknown block passthrough
}

func decodeStream(r io.Reader, yield func(event.Event) error) (model.Result, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	blocks := map[int]*blockAcc{}
	var stopReason string
	var usage run.Usage
	var eventName string
	var rawResp bytes.Buffer

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if rawResp.Len() > 0 {
			rawResp.WriteByte('\n')
		}
		rawResp.WriteString(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Type == "" {
			ev.Type = eventName
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				applyAnthropicUsage(&usage, ev.Message.Usage)
			}
		case "content_block_start":
			acc := &blockAcc{}
			if ev.ContentBlock != nil {
				acc.typ = ev.ContentBlock.Type
				acc.id = ev.ContentBlock.ID
				acc.name = ev.ContentBlock.Name
				if ev.ContentBlock.Text != "" {
					acc.text.WriteString(ev.ContentBlock.Text)
				}
				if ev.ContentBlock.Thinking != "" {
					acc.thinking.WriteString(ev.ContentBlock.Thinking)
				}
				acc.signature = ev.ContentBlock.Signature
				switch acc.typ {
				case "text", "tool_use", "thinking", "redacted_thinking", "":
				default:
					raw, _ := json.Marshal(ev.ContentBlock)
					acc.raw = raw
				}
			}
			blocks[ev.Index] = acc
		case "content_block_delta":
			acc := blocks[ev.Index]
			if acc == nil {
				acc = &blockAcc{}
				blocks[ev.Index] = acc
			}
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				acc.text.WriteString(ev.Delta.Text)
				if yield != nil && ev.Delta.Text != "" {
					if err := yield(event.ModelDelta{TextDelta: ev.Delta.Text}); err != nil {
						return model.Result{}, err
					}
				}
			case "thinking_delta":
				acc.thinking.WriteString(ev.Delta.Thinking)
			case "input_json_delta":
				acc.json.WriteString(ev.Delta.PartialJSON)
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
			applyAnthropicUsage(&usage, ev.Usage)
		}
	}
	if err := scanner.Err(); err != nil {
		return model.Result{}, err
	}

	msg := message.Message{Role: message.RoleAssistant}
	maxIdx := -1
	for i := range blocks {
		if i > maxIdx {
			maxIdx = i
		}
	}
	hasTools := false
	for i := 0; i <= maxIdx; i++ {
		acc, ok := blocks[i]
		if !ok {
			continue
		}
		switch acc.typ {
		case "text", "":
			if acc.text.Len() > 0 {
				msg.Parts = append(msg.Parts, message.Text{Text: acc.text.String()})
			}
		case "thinking", "redacted_thinking":
			msg.Parts = append(msg.Parts, message.Reasoning{
				Text:      acc.thinking.String(),
				Signature: acc.signature,
			})
		case "tool_use":
			hasTools = true
			input := []byte(acc.json.String())
			if len(input) == 0 {
				input = []byte("{}")
			}
			msg.Parts = append(msg.Parts, message.ToolUse{
				ID:    acc.id,
				Name:  acc.name,
				Input: input,
			})
		default:
			if len(acc.raw) > 0 {
				msg.Parts = append(msg.Parts, message.Opaque{Vendor: "anthropic", Data: acc.raw})
			}
		}
	}

	stop := mapStopReason(stopReason, hasTools)
	res := model.Result{Message: msg, StopReason: stop, Usage: usage, RawResponse: rawResp.Bytes()}
	if yield != nil {
		if err := yield(event.ModelFinished{Message: msg, StopReason: stop, Usage: usage}); err != nil {
			return model.Result{}, err
		}
	}
	return res, nil
}

type completeResponse struct {
	Content []struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Input     json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      *anthropicUsage `json:"usage"`
}

func decodeComplete(r io.Reader) (model.Result, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return model.Result{}, err
	}
	var resp completeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return model.Result{RawResponse: raw}, err
	}
	msg := message.Message{Role: message.RoleAssistant}
	hasTools := false
	for _, block := range resp.Content {
		switch block.Type {
		case "text", "":
			if block.Text != "" {
				msg.Parts = append(msg.Parts, message.Text{Text: block.Text})
			}
		case "thinking", "redacted_thinking":
			msg.Parts = append(msg.Parts, message.Reasoning{
				Text:      block.Thinking,
				Signature: block.Signature,
			})
		case "tool_use":
			hasTools = true
			input := block.Input
			if len(input) == 0 {
				input = []byte("{}")
			}
			msg.Parts = append(msg.Parts, message.ToolUse{
				ID:    block.ID,
				Name:  block.Name,
				Input: input,
			})
		default:
			blockRaw, _ := json.Marshal(block)
			msg.Parts = append(msg.Parts, message.Opaque{Vendor: "anthropic", Data: blockRaw})
		}
	}
	var usage run.Usage
	applyAnthropicUsage(&usage, resp.Usage)
	return model.Result{
		Message:     msg,
		StopReason:  mapStopReason(resp.StopReason, hasTools),
		Usage:       usage,
		RawResponse: raw,
	}, nil
}

func mapStopReason(reason string, hasTools bool) run.StopReason {
	switch reason {
	case "tool_use":
		return run.StopToolUse
	case "max_tokens":
		return run.StopMaxTokens
	case "end_turn", "stop_sequence", "":
		if hasTools {
			return run.StopToolUse
		}
		return run.StopEndTurn
	default:
		if hasTools {
			return run.StopToolUse
		}
		return run.StopEndTurn
	}
}

var _ model.Provider = (*Provider)(nil)
