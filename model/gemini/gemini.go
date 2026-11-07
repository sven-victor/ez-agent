package gemini

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
	"strings"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com"

// Provider is a Google Gemini generateContent (stream) adapter.
type Provider struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	caps       model.Capabilities
	headers    map[string]string // extra HTTP headers (applied after built-ins)
}

type Option func(*Provider)

func WithAPIKey(key string) Option { return func(p *Provider) { p.apiKey = key } }

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
func WithModel(name string) Option         { return func(p *Provider) { p.model = name } }
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.httpClient = c } }
func WithCaps(c model.Capabilities) Option { return func(p *Provider) { p.caps = c } }

func New(opts ...Option) *Provider {
	p := &Provider{
		baseURL:    defaultBaseURL,
		model:      "gemini-2.0-flash",
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

func (p *Provider) Name() string             { return "gemini" }
func (p *Provider) Caps() model.Capabilities { return p.caps }

func (p *Provider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	rawReq, resp, err := p.doGenerate(ctx, req, true)
	if err != nil {
		return model.Result{RawRequest: rawReq}, err
	}
	defer resp.Body.Close()
	res, err := decodeStream(resp.Body, yield)
	res.RawRequest = rawReq
	return res, err
}

// Complete runs a non-streaming generateContent request.
func (p *Provider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	rawReq, resp, err := p.doGenerate(ctx, req, false)
	if err != nil {
		return model.Result{RawRequest: rawReq}, err
	}
	defer resp.Body.Close()
	res, err := decodeComplete(resp.Body)
	res.RawRequest = rawReq
	return res, err
}

func (p *Provider) doGenerate(ctx context.Context, req model.Request, stream bool) ([]byte, *http.Response, error) {
	if err := model.Validate(p, req); err != nil {
		return nil, nil, err
	}
	body, err := encodeRequest(req)
	if err != nil {
		return nil, nil, err
	}
	var url string
	if stream {
		url = fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", p.baseURL, p.model, p.apiKey)
	} else {
		url = fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.baseURL, p.model, p.apiKey)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return body, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
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
		s := strings.ToLower(string(b))
		if strings.Contains(s, "token") && strings.Contains(s, "limit") || strings.Contains(s, "context") {
			return body, nil, fmt.Errorf("%w: %s", model.ErrOverflow, string(b))
		}
		if resp.StatusCode == 429 {
			return body, nil, fmt.Errorf("%w: %s", model.ErrThrottled, string(b))
		}
		return body, nil, fmt.Errorf("gemini: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return body, resp, nil
}

type genRequest struct {
	Contents          []content        `json:"contents"`
	SystemInstruction *content         `json:"systemInstruction,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolConfig        map[string]any   `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]any   `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []any  `json:"parts"`
}

func encodeRequest(req model.Request) ([]byte, error) {
	var contents []content
	var lastToolNames map[string]string
	for _, m := range req.Messages {
		role := "user"
		if m.Role == message.RoleAssistant {
			role = "model"
			lastToolNames = map[string]string{}
			for _, p := range m.Parts {
				if tu, ok := p.(message.ToolUse); ok {
					lastToolNames[tu.ID] = tu.Name
				}
			}
		}
		parts, err := encodeParts(m, lastToolNames)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			continue
		}
		contents = append(contents, content{Role: role, Parts: parts})
	}
	gr := genRequest{Contents: contents}
	if req.System.Text != "" || len(req.System.Parts) > 0 {
		sysParts := []any{}
		if req.System.Text != "" {
			sysParts = append(sysParts, map[string]any{"text": req.System.Text})
		}
		for _, p := range req.System.Parts {
			encoded, err := encodeOnePart(p, nil)
			if err != nil {
				return nil, err
			}
			sysParts = append(sysParts, encoded...)
		}
		if len(sysParts) > 0 {
			gr.SystemInstruction = &content{Parts: sysParts}
		}
	}
	if len(req.Tools) > 0 {
		decls := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			var schema any
			_ = json.Unmarshal(t.InputSchema, &schema)
			decls = append(decls, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  schema,
			})
		}
		gr.Tools = []map[string]any{{"functionDeclarations": decls}}
	}
	if tc := encodeToolChoice(req.ToolChoice); tc != nil {
		gr.ToolConfig = map[string]any{"functionCallingConfig": tc}
	}
	gen := map[string]any{}
	if req.MaxTokens > 0 {
		gen["maxOutputTokens"] = req.MaxTokens
	}
	if len(req.OutputSchema) > 0 {
		var schema any
		if err := json.Unmarshal(req.OutputSchema, &schema); err != nil {
			return nil, err
		}
		gen["responseMimeType"] = "application/json"
		gen["responseSchema"] = schema
	}
	if len(gen) > 0 {
		gr.GenerationConfig = gen
	}
	return json.Marshal(gr)
}

func encodeToolChoice(c tool.Choice) map[string]any {
	switch strings.ToLower(c.Type) {
	case "none":
		return map[string]any{"mode": "NONE"}
	case "required", "any":
		return map[string]any{"mode": "ANY"}
	case "named":
		return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{c.Name}}
	case "auto", "":
		return nil
	default:
		return nil
	}
}

func encodeParts(m message.Message, toolNames map[string]string) ([]any, error) {
	var parts []any
	for _, p := range m.Parts {
		encoded, err := encodeOnePart(p, toolNames)
		if err != nil {
			return nil, err
		}
		parts = append(parts, encoded...)
	}
	return parts, nil
}

func encodeOnePart(p message.Part, toolNames map[string]string) ([]any, error) {
	switch v := p.(type) {
	case message.Text:
		if v.Text == "" {
			return nil, nil
		}
		return []any{map[string]any{"text": v.Text}}, nil
	case message.Image:
		blob, err := inlineData(v.Blob)
		if err != nil {
			return nil, err
		}
		return []any{map[string]any{"inlineData": blob}}, nil
	case message.Audio:
		blob, err := inlineData(v.Blob)
		if err != nil {
			return nil, err
		}
		return []any{map[string]any{"inlineData": blob}}, nil
	case message.Video:
		blob, err := inlineData(v.Blob)
		if err != nil {
			return nil, err
		}
		return []any{map[string]any{"inlineData": blob}}, nil
	case message.Document:
		blob, err := inlineData(v.Blob)
		if err != nil {
			return nil, err
		}
		return []any{map[string]any{"inlineData": blob}}, nil
	case message.ToolUse:
		var args any
		_ = json.Unmarshal(v.Input, &args)
		return []any{map[string]any{
			"functionCall": map[string]any{"name": v.Name, "args": args},
		}}, nil
	case message.ToolResult:
		name := "tool"
		if toolNames != nil {
			if n, ok := toolNames[v.ToolUseID]; ok {
				name = n
			}
		}
		var resp any = message.TextContent(message.Message{Parts: v.Parts})
		return []any{map[string]any{
			"functionResponse": map[string]any{
				"name":     name,
				"response": map[string]any{"content": resp, "ok": v.OK},
			},
		}}, nil
	case message.Reasoning:
		if v.Text == "" {
			return nil, nil
		}
		// Gemini has no dedicated thinking wire in generateContent; keep as text for multi-turn.
		return []any{map[string]any{"text": "[reasoning]\n" + v.Text}}, nil
	case message.Opaque:
		if v.Vendor != "" && v.Vendor != "gemini" {
			return nil, fmt.Errorf("gemini: opaque vendor %q not supported", v.Vendor)
		}
		var part any
		if err := json.Unmarshal(v.Data, &part); err != nil {
			return nil, fmt.Errorf("gemini: opaque data: %w", err)
		}
		return []any{part}, nil
	default:
		return nil, fmt.Errorf("gemini: unsupported part %T", p)
	}
}

func inlineData(b message.Blob) (map[string]any, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	raw := b.Bytes
	mime := b.MIME
	if len(raw) == 0 && b.Path != "" {
		var err error
		raw, err = os.ReadFile(b.Path)
		if err != nil {
			return nil, err
		}
	}
	if len(raw) == 0 && b.URL != "" {
		return nil, fmt.Errorf("gemini: URL blobs must be loaded to Bytes/Path by caller for inlineData")
	}
	return map[string]any{
		"mimeType": mime,
		"data":     base64.StdEncoding.EncodeToString(raw),
	}, nil
}

func decodeStream(r io.Reader, yield func(event.Event) error) (model.Result, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	acc := &genAcc{}
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
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk genResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if err := acc.apply(chunk, yield); err != nil {
			return model.Result{RawResponse: rawResp.Bytes()}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return model.Result{RawResponse: rawResp.Bytes()}, err
	}
	res := acc.result()
	res.RawResponse = rawResp.Bytes()
	if yield != nil {
		_ = yield(event.ModelFinished{Message: res.Message, StopReason: res.StopReason, Usage: res.Usage})
	}
	return res, nil
}

func decodeComplete(r io.Reader) (model.Result, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return model.Result{}, err
	}
	var resp genResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return model.Result{RawResponse: raw}, err
	}
	acc := &genAcc{}
	if err := acc.apply(resp, nil); err != nil {
		return model.Result{RawResponse: raw}, err
	}
	res := acc.result()
	res.RawResponse = raw
	return res, nil
}

type genResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text       string `json:"text"`
				InlineData *struct {
					MIMEType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
				FunctionCall *struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
}

// geminiUsageMetadata is one generateContent usageMetadata object.
// promptTokenCount already includes cached content. thoughtsTokenCount is
// part of the completion. totalTokenCount is ignored.
type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
}

func (m *geminiUsageMetadata) normalized() run.Usage {
	if m == nil {
		return run.Usage{}
	}
	return run.NewUsage(
		m.PromptTokenCount,
		m.CandidatesTokenCount+m.ThoughtsTokenCount,
		m.CachedContentTokenCount,
		0,
	)
}

type genAcc struct {
	text       strings.Builder
	audioBytes []byte
	audioMIME  string
	tools      []message.ToolUse
	finish     string
	usage      run.Usage
}

func (a *genAcc) apply(chunk genResponse, yield func(event.Event) error) error {
	if chunk.UsageMetadata != nil {
		a.usage = chunk.UsageMetadata.normalized()
	}
	if len(chunk.Candidates) == 0 {
		return nil
	}
	c := chunk.Candidates[0]
	if c.FinishReason != "" {
		a.finish = c.FinishReason
	}
	for _, part := range c.Content.Parts {
		if part.Text != "" {
			a.text.WriteString(part.Text)
			if yield != nil {
				if err := yield(event.ModelDelta{TextDelta: part.Text}); err != nil {
					return err
				}
			}
		}
		if part.InlineData != nil && part.InlineData.Data != "" {
			mime := part.InlineData.MIMEType
			if strings.HasPrefix(strings.ToLower(mime), "audio/") {
				raw, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err == nil && len(raw) > 0 {
					a.audioBytes = append(a.audioBytes, raw...)
					if a.audioMIME == "" {
						a.audioMIME = mime
					}
					if yield != nil {
						if err := yield(event.ModelDelta{AudioChunk: raw, MIME: mime}); err != nil {
							return err
						}
					}
				}
			}
		}
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			a.tools = append(a.tools, message.ToolUse{
				ID:    fmt.Sprintf("call_%d", len(a.tools)+1),
				Name:  part.FunctionCall.Name,
				Input: args,
			})
		}
	}
	return nil
}

func (a *genAcc) result() model.Result {
	msg := message.Message{Role: message.RoleAssistant}
	if a.text.Len() > 0 {
		msg.Parts = append(msg.Parts, message.Text{Text: a.text.String()})
	}
	if len(a.audioBytes) > 0 {
		mime := a.audioMIME
		if mime == "" {
			mime = "audio/wav"
		}
		msg.Parts = append(msg.Parts, message.AudioOut{Blob: message.Blob{MIME: mime, Bytes: a.audioBytes}})
	}
	for _, tu := range a.tools {
		msg.Parts = append(msg.Parts, tu)
	}
	stop := run.StopEndTurn
	switch strings.ToUpper(a.finish) {
	case "MAX_TOKENS":
		stop = run.StopMaxTokens
	case "STOP", "":
		if len(a.tools) > 0 {
			stop = run.StopToolUse
		}
	default:
		if len(a.tools) > 0 {
			stop = run.StopToolUse
		}
	}
	return model.Result{Message: msg, StopReason: stop, Usage: a.usage}
}

var _ model.Provider = (*Provider)(nil)
