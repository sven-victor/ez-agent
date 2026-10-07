package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/tool"
)

var defaultSkillInputSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"prompt": {
			"type": "string",
			"description": "The task or question to send to the remote agent"
		}
	},
	"required": ["prompt"]
}`)

// Client wraps an A2A client and exposes remote skills as a tool.Registry.
type Client struct {
	inner  *a2aclient.Client
	mu     sync.RWMutex
	card   *protocol.AgentCard
	specs  []tool.Spec
	byName map[string]string // tool name → skill ID (empty = agent-level chat)
}

// Dial resolves the Agent Card at cardURL and opens an A2A client.
func Dial(ctx context.Context, cardURL string) (*Client, error) {
	card, err := agentcard.DefaultResolver.Resolve(ctx, cardURL)
	if err != nil {
		return nil, fmt.Errorf("a2a resolve card: %w", err)
	}
	inner, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		return nil, fmt.Errorf("a2a client: %w", err)
	}
	c := &Client{inner: inner, card: card, byName: map[string]string{}}
	c.refreshFromCard(card)
	return c, nil
}

// Card returns the resolved Agent Card.
func (c *Client) Card() *protocol.AgentCard {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.card
}

// Close is a no-op for the JSON-RPC transport (kept for API symmetry with MCP).
func (c *Client) Close() error { return nil }

// Registry returns this client as a tool.Registry.
func (c *Client) Registry() tool.Registry { return c }

// RefreshSkills re-resolves the Agent Card from cardURL and updates Specs.
func (c *Client) RefreshSkills(ctx context.Context, cardURL string) error {
	card, err := agentcard.DefaultResolver.Resolve(ctx, cardURL)
	if err != nil {
		return fmt.Errorf("a2a refresh card: %w", err)
	}
	c.mu.Lock()
	c.card = card
	c.refreshFromCard(card)
	c.mu.Unlock()
	return nil
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
	toolName := name
	return func(ctx context.Context, call tool.Call) (tool.Result, error) {
		return c.callSkill(ctx, call, toolName)
	}, true
}

func (c *Client) refreshFromCard(card *protocol.AgentCard) {
	c.specs = nil
	c.byName = map[string]string{}
	if card == nil {
		return
	}
	if len(card.Skills) == 0 {
		name := sanitizeToolName(card.Name)
		if name == "" {
			name = "a2a_agent"
		}
		desc := card.Description
		if desc == "" {
			desc = "Send a prompt to the remote A2A agent."
		}
		c.addSpec(name, desc, "")
		return
	}
	for _, sk := range card.Skills {
		name := sanitizeToolName(sk.ID)
		if name == "" {
			name = sanitizeToolName(sk.Name)
		}
		if name == "" {
			continue
		}
		desc := sk.Description
		if desc == "" {
			desc = sk.Name
		}
		c.addSpec(name, desc, sk.ID)
	}
}

func (c *Client) addSpec(name, desc, skillID string) {
	if _, exists := c.byName[name]; exists {
		return
	}
	spec := tool.Spec{
		Name:        name,
		Description: desc,
		InputSchema: append(json.RawMessage(nil), defaultSkillInputSchema...),
	}
	c.specs = append(c.specs, spec)
	c.byName[name] = skillID
}

func (c *Client) callSkill(ctx context.Context, call tool.Call, toolName string) (tool.Result, error) {
	prompt, err := promptFromToolInput(call.Input)
	if err != nil {
		return toolFail(call.ID, err.Error()), nil
	}
	if prompt == "" {
		return toolFail(call.ID, "prompt is required"), nil
	}
	c.mu.RLock()
	skillID := c.byName[toolName]
	c.mu.RUnlock()
	if skillID != "" && skillID != toolName {
		prompt = "[" + skillID + "] " + prompt
	}

	msg := protocol.NewMessage(protocol.MessageRoleUser, protocol.NewTextPart(prompt))
	resp, err := c.inner.SendMessage(ctx, &protocol.SendMessageRequest{Message: msg})
	if err != nil {
		return toolFail(call.ID, err.Error()), nil
	}
	text := textFromSendResult(resp)
	if text == "" {
		text = "(empty remote agent response)"
	}
	return tool.Result{
		ToolUseID: call.ID,
		OK:        true,
		Content:   []message.Part{message.Text{Text: text}},
	}, nil
}

func promptFromToolInput(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("invalid tool input: %w", err)
	}
	v, ok := obj["prompt"]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("prompt must be a string")
	}
	return s, nil
}

func sanitizeToolName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out != "" && unicode.IsDigit(rune(out[0])) {
		out = "a_" + out
	}
	return out
}

func toolFail(id, text string) tool.Result {
	return tool.Result{
		ToolUseID: id,
		OK:        false,
		Content:   []message.Part{message.Text{Text: text}},
	}
}
