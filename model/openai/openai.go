package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

const defaultBaseURL = "https://api.openai.com/v1"

// Provider is an OpenAI-compatible Chat Completions adapter.
type Provider struct {
	apiKey         string
	organizationID string
	baseURL        string
	model          string
	httpClient     *http.Client
	caps           model.Capabilities
	headers        map[string]string // extra HTTP headers (applied after built-ins)
}

// Option configures the provider.
type Option func(*Provider)

// WithAPIKey sets the API key.
func WithAPIKey(key string) Option {
	return func(p *Provider) { p.apiKey = key }
}

// WithOrganization sets the OpenAI-Organization request header.
func WithOrganization(org string) Option {
	return func(p *Provider) { p.organizationID = org }
}

// WithHeader sets a custom HTTP header on every request.
// Applied after built-in headers (Authorization, Content-Type, …), so it may override them.
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

// WithBaseURL sets the API base URL (e.g. https://api.openai.com/v1).
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(url, "/") }
}

// WithModel sets the model id.
func WithModel(name string) Option {
	return func(p *Provider) { p.model = name }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.httpClient = c }
}

// WithCaps overrides provider capabilities (e.g. disable AudioIn for gateways).
func WithCaps(c model.Capabilities) Option {
	return func(p *Provider) { p.caps = c }
}

// New creates an OpenAI-compatible provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		baseURL:    defaultBaseURL,
		model:      "gpt-4o-mini",
		httpClient: http.DefaultClient,
		caps: model.Capabilities{
			TextIn:      true,
			ImageIn:     true,
			AudioIn:     true,
			VideoIn:     true,
			DocumentIn:  true,
			AudioOut:    true,
			Tools:       true,
			Streaming:   true,
			JSONSchema:  true,
			SystemParts: true,
			Opaque:      true,
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Provider) Name() string             { return "openai" }
func (p *Provider) Caps() model.Capabilities { return p.caps }

// Stream runs a Chat Completions request with stream=true.
func (p *Provider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	rawReq, resp, err := p.doChat(ctx, req, true)
	if err != nil {
		return model.Result{RawRequest: rawReq}, err
	}
	defer resp.Body.Close()
	res, err := decodeStream(resp.Body, yield)
	res.RawRequest = rawReq
	return res, err
}

// Complete runs a Chat Completions request with stream=false.
func (p *Provider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	rawReq, resp, err := p.doChat(ctx, req, false)
	if err != nil {
		return model.Result{RawRequest: rawReq}, err
	}
	defer resp.Body.Close()
	res, err := decodeComplete(resp.Body)
	res.RawRequest = rawReq
	return res, err
}

func (p *Provider) doChat(ctx context.Context, req model.Request, stream bool) ([]byte, *http.Response, error) {
	if err := model.Validate(p, req); err != nil {
		return nil, nil, err
	}
	body, err := encodeRequest(p.model, req, stream)
	if err != nil {
		return nil, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return body, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if p.organizationID != "" {
		httpReq.Header.Set("OpenAI-Organization", p.organizationID)
	}
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
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
		if isOverflowStatus(resp.StatusCode, b) {
			return body, nil, fmt.Errorf("%w: %s", model.ErrOverflow, string(b))
		}
		if isThrottledStatus(resp.StatusCode, b) {
			return body, nil, fmt.Errorf("%w: %s", model.ErrThrottled, string(b))
		}
		return body, nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return body, resp, nil
}

func isOverflowStatus(code int, body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "context_length") ||
		strings.Contains(s, "context length") ||
		strings.Contains(s, "maximum context") ||
		strings.Contains(s, "token limit")
}

func isThrottledStatus(code int, body []byte) bool {
	if code == 429 {
		return true
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "rate limit") ||
		strings.Contains(s, "too many requests") ||
		strings.Contains(s, "overloaded")
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []wireMessage  `json:"messages"`
	Tools          []wireTool     `json:"tools,omitempty"`
	ToolChoice     any            `json:"tool_choice,omitempty"`
	Stream         bool           `json:"stream"`
	StreamOpts     *streamOpts    `json:"stream_options,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role             string         `json:"role"`
	Content          any            `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	Name             string         `json:"name,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireToolSpec `json:"function"`
}

type wireToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func encodeRequest(modelName string, req model.Request, stream bool) ([]byte, error) {
	msgs, err := encodeMessages(req)
	if err != nil {
		return nil, err
	}
	cr := chatRequest{
		Model:    modelName,
		Messages: msgs,
		Stream:   stream,
	}
	if stream {
		cr.StreamOpts = &streamOpts{IncludeUsage: true}
	}
	if req.MaxTokens > 0 {
		cr.MaxTokens = req.MaxTokens
	}
	if tc := encodeToolChoice(req.ToolChoice); tc != nil {
		cr.ToolChoice = tc
	}
	for _, t := range req.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		cr.Tools = append(cr.Tools, wireTool{
			Type: "function",
			Function: wireToolSpec{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}
	if len(req.OutputSchema) > 0 {
		name := req.OutputName
		if name == "" {
			name = "response"
		}
		var schema any
		if err := json.Unmarshal(req.OutputSchema, &schema); err != nil {
			return nil, fmt.Errorf("openai: invalid OutputSchema: %w", err)
		}
		cr.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"schema": schema,
				"strict": true,
			},
		}
	}
	return json.Marshal(cr)
}

func encodeToolChoice(c tool.Choice) any {
	switch strings.ToLower(c.Type) {
	case "", "auto":
		if c.Name != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": c.Name},
			}
		}
		return nil
	case "none":
		return "none"
	case "required":
		return "required"
	case "named":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": c.Name},
		}
	default:
		return nil
	}
}

func encodeMessages(req model.Request) ([]wireMessage, error) {
	var out []wireMessage
	sysParts := message.WithoutCacheBreakpoints(req.System.Parts)
	if req.System.Text != "" || len(sysParts) > 0 {
		content, err := encodeUserContent(append([]message.Part{message.Text{Text: req.System.Text}}, sysParts...))
		if err != nil {
			return nil, err
		}
		// System is text-only on OpenAI chat completions in the common case.
		if req.System.Text != "" && len(sysParts) == 0 {
			out = append(out, wireMessage{Role: "system", Content: req.System.Text})
		} else {
			out = append(out, wireMessage{Role: "system", Content: content})
		}
	}

	for _, m := range req.Messages {
		parts := message.WithoutCacheBreakpoints(m.Parts)
		switch m.Role {
		case message.RoleAssistant:
			if len(parts) == 0 && len(m.Parts) > 0 {
				continue
			}
			m.Parts = parts
			wm, err := encodeAssistant(m)
			if err != nil {
				return nil, err
			}
			out = append(out, wm)
		case message.RoleUser:
			// Tool results become role=tool messages.
			var toolResults []message.ToolResult
			var other []message.Part
			for _, p := range parts {
				if tr, ok := p.(message.ToolResult); ok {
					toolResults = append(toolResults, tr)
				} else {
					other = append(other, p)
				}
			}
			for _, tr := range toolResults {
				content, err := encodeToolResultContent(tr)
				if err != nil {
					return nil, err
				}
				out = append(out, wireMessage{
					Role:       "tool",
					ToolCallID: tr.ToolUseID,
					Content:    content,
				})
			}
			if len(other) > 0 {
				content, err := encodeUserContent(other)
				if err != nil {
					return nil, err
				}
				out = append(out, wireMessage{Role: "user", Content: content})
			}
		default:
			return nil, fmt.Errorf("openai: unsupported role %q", m.Role)
		}
	}
	return out, nil
}

func encodeAssistant(m message.Message) (wireMessage, error) {
	wm := wireMessage{Role: "assistant"}
	var text strings.Builder
	for _, p := range m.Parts {
		switch v := p.(type) {
		case message.Text:
			text.WriteString(v.Text)
		case message.ToolUse:
			args := string(v.Input)
			if args == "" {
				args = "{}"
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   v.ID,
				Type: "function",
				Function: wireFunctionCall{
					Name:      v.Name,
					Arguments: args,
				},
			})
		case message.Reasoning:
			wm.ReasoningContent = v.Text
		case message.Opaque:
			if v.Vendor != "" && v.Vendor != "openai" {
				return wireMessage{}, fmt.Errorf("openai: opaque vendor %q not supported", v.Vendor)
			}
			// Opaque for OpenAI: embed as assistant text marker when content is a string/object note.
			var note string
			if err := json.Unmarshal(v.Data, &note); err == nil {
				text.WriteString(note)
			} else {
				text.WriteString(string(v.Data))
			}
		default:
			return wireMessage{}, fmt.Errorf("openai: unsupported assistant part %T", p)
		}
	}
	if text.Len() > 0 {
		wm.Content = text.String()
	}
	return wm, nil
}

func encodeUserContent(parts []message.Part) (any, error) {
	if len(parts) == 1 {
		if t, ok := parts[0].(message.Text); ok {
			return t.Text, nil
		}
	}
	var blocks []map[string]any
	for _, p := range parts {
		switch v := p.(type) {
		case message.Text:
			if v.Text == "" {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type": "text",
				"text": v.Text,
			})
		case message.Image:
			url, err := mediaDataURL(v.Blob)
			if err != nil {
				return nil, err
			}
			img := map[string]any{"url": url}
			if v.Detail != "" {
				img["detail"] = v.Detail
			}
			blocks = append(blocks, map[string]any{
				"type":      "image_url",
				"image_url": img,
			})
		case message.Audio:
			data, format, err := audioPayload(v.Blob)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, map[string]any{
				"type": "input_audio",
				"input_audio": map[string]any{
					"data":   data,
					"format": format,
				},
			})
		case message.Video:
			url, err := mediaDataURL(v.Blob)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, map[string]any{
				"type":      "video_url",
				"video_url": map[string]any{"url": url},
			})
		case message.Document:
			url, err := mediaDataURL(v.Blob)
			if err != nil {
				return nil, err
			}
			// OpenAI-compatible gateways: send as image_url-style data URL or text note.
			// Prefer file-like text descriptor + data URL in a text block for broad compatibility.
			name := v.Blob.Filename
			if name == "" {
				name = "document"
			}
			blocks = append(blocks, map[string]any{
				"type": "text",
				"text": fmt.Sprintf("[document %s mime=%s]", name, v.Blob.MIME),
			})
			blocks = append(blocks, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		default:
			return nil, fmt.Errorf("openai: unsupported user part %T", p)
		}
	}
	if len(blocks) == 0 {
		return "", nil
	}
	return blocks, nil
}

func encodeToolResultContent(tr message.ToolResult) (any, error) {
	hasMedia := false
	for _, p := range tr.Parts {
		if _, ok := p.(message.Image); ok {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		text := message.TextContent(message.Message{Parts: tr.Parts})
		if text == "" && !tr.OK {
			text = "tool failed"
		}
		return text, nil
	}
	return encodeUserContent(tr.Parts)
}

func mediaDataURL(b message.Blob) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	if b.URL != "" {
		return b.URL, nil
	}
	raw, mime, err := loadBlobBytes(b)
	if err != nil {
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

func loadBlobBytes(b message.Blob) ([]byte, string, error) {
	mime := b.MIME
	if mime == "" {
		mime = "application/octet-stream"
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
	return nil, "", fmt.Errorf("openai: blob has no Bytes or Path")
}

func audioPayload(b message.Blob) (dataB64 string, format string, err error) {
	raw, mime, err := loadBlobBytes(b)
	if err != nil {
		return "", "", err
	}
	format = audioFormat(mime, b.Filename, b.Path)
	return base64.StdEncoding.EncodeToString(raw), format, nil
}

func audioFormat(mime, filename, path string) string {
	switch strings.ToLower(mime) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(path))
	}
	switch ext {
	case ".wav":
		return "wav"
	case ".mp3":
		return "mp3"
	}
	return "wav"
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Audio            *struct {
				Data string `json:"data"`
			} `json:"audio"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

// openAIUsage is one Chat Completions usage object.
// prompt_tokens already includes cached tokens. The provider total_tokens
// field is ignored; run.Usage.TotalTokens is prompt plus completion.
type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u *openAIUsage) normalized() run.Usage {
	if u == nil {
		return run.Usage{}
	}
	cacheRead := 0
	if u.PromptTokensDetails != nil {
		cacheRead = u.PromptTokensDetails.CachedTokens
	}
	return run.NewUsage(u.PromptTokens, u.CompletionTokens, cacheRead, 0)
}

type toolCallAcc struct {
	id   string
	name string
	args strings.Builder
}

func decodeStream(r io.Reader, yield func(event.Event) error) (model.Result, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var text strings.Builder
	var reasoning strings.Builder
	var audioBytes []byte
	toolAcc := map[int]*toolCallAcc{}
	var finishReason string
	var usage run.Usage
	var rawResp bytes.Buffer

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if rawResp.Len() > 0 {
			rawResp.WriteByte('\n')
		}
		rawResp.WriteString(data)
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.normalized()
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finishReason = *ch.FinishReason
		}
		if ch.Delta.Content != "" {
			text.WriteString(ch.Delta.Content)
			if yield != nil {
				if err := yield(event.ModelDelta{TextDelta: ch.Delta.Content}); err != nil {
					return model.Result{}, err
				}
			}
		}
		if ch.Delta.ReasoningContent != "" {
			reasoning.WriteString(ch.Delta.ReasoningContent)
		}
		if ch.Delta.Audio != nil && ch.Delta.Audio.Data != "" {
			raw, err := base64.StdEncoding.DecodeString(ch.Delta.Audio.Data)
			if err == nil && len(raw) > 0 {
				audioBytes = append(audioBytes, raw...)
				if yield != nil {
					if err := yield(event.ModelDelta{AudioChunk: raw, MIME: "audio/wav"}); err != nil {
						return model.Result{}, err
					}
				}
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			acc, ok := toolAcc[tc.Index]
			if !ok {
				acc = &toolCallAcc{}
				toolAcc[tc.Index] = acc
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			acc.args.WriteString(tc.Function.Arguments)
		}
	}
	if err := scanner.Err(); err != nil {
		return model.Result{}, err
	}

	msg := message.Message{Role: message.RoleAssistant}
	if reasoning.Len() > 0 {
		msg.Parts = append(msg.Parts, message.Reasoning{Text: reasoning.String()})
	}
	if text.Len() > 0 {
		msg.Parts = append(msg.Parts, message.Text{Text: text.String()})
	}
	if len(audioBytes) > 0 {
		msg.Parts = append(msg.Parts, message.AudioOut{Blob: message.Blob{MIME: "audio/wav", Bytes: audioBytes}})
	}
	// stable index order
	maxIdx := -1
	for i := range toolAcc {
		if i > maxIdx {
			maxIdx = i
		}
	}
	for i := 0; i <= maxIdx; i++ {
		acc, ok := toolAcc[i]
		if !ok {
			continue
		}
		msg.Parts = append(msg.Parts, message.ToolUse{
			ID:    acc.id,
			Name:  acc.name,
			Input: []byte(acc.args.String()),
		})
	}

	stop := mapFinishReason(finishReason, len(toolAcc) > 0)
	res := model.Result{
		Message:     msg,
		StopReason:  stop,
		Usage:       usage,
		RawResponse: rawResp.Bytes(),
	}
	if yield != nil {
		if err := yield(event.ModelFinished{
			Message:    msg,
			StopReason: stop,
			Usage:      usage,
		}); err != nil {
			return model.Result{}, err
		}
	}
	return res, nil
}

type completeResponse struct {
	Choices []struct {
		Message struct {
			Content          any    `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Audio            *struct {
				Data string `json:"data"`
			} `json:"audio"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
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
	if len(resp.Choices) == 0 {
		return model.Result{RawResponse: raw}, fmt.Errorf("openai: empty choices in complete response")
	}
	ch := resp.Choices[0]
	msg := message.Message{Role: message.RoleAssistant}
	if ch.Message.ReasoningContent != "" {
		msg.Parts = append(msg.Parts, message.Reasoning{Text: ch.Message.ReasoningContent})
	}
	if text := completeContentText(ch.Message.Content); text != "" {
		msg.Parts = append(msg.Parts, message.Text{Text: text})
	}
	if ch.Message.Audio != nil && ch.Message.Audio.Data != "" {
		audioRaw, err := base64.StdEncoding.DecodeString(ch.Message.Audio.Data)
		if err == nil && len(audioRaw) > 0 {
			msg.Parts = append(msg.Parts, message.AudioOut{Blob: message.Blob{MIME: "audio/wav", Bytes: audioRaw}})
		}
	}
	for _, tc := range ch.Message.ToolCalls {
		msg.Parts = append(msg.Parts, message.ToolUse{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: []byte(tc.Function.Arguments),
		})
	}
	usage := resp.Usage.normalized()
	return model.Result{
		Message:     msg,
		StopReason:  mapFinishReason(ch.FinishReason, len(ch.Message.ToolCalls) > 0),
		Usage:       usage,
		RawResponse: raw,
	}, nil
}

func completeContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
		return b.String()
	default:
		return ""
	}
}

func mapFinishReason(reason string, hasTools bool) run.StopReason {
	switch reason {
	case "tool_calls", "function_call":
		return run.StopToolUse
	case "length":
		return run.StopMaxTokens
	case "stop", "":
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
