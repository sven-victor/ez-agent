package message

// NewUserText builds a user message with a single text part.
func NewUserText(s string) Message {
	return Message{Role: RoleUser, Parts: []Part{Text{Text: s}}}
}

// NewUserParts builds a user message with the given parts.
func NewUserParts(parts ...Part) Message {
	return Message{Role: RoleUser, Parts: parts}
}

// NewAssistantText builds an assistant message with a single text part.
func NewAssistantText(s string) Message {
	return Message{Role: RoleAssistant, Parts: []Part{Text{Text: s}}}
}

// ToolUses returns all ToolUse parts from a message.
func ToolUses(m Message) []ToolUse {
	var out []ToolUse
	for _, p := range m.Parts {
		if tu, ok := p.(ToolUse); ok {
			out = append(out, tu)
		}
	}
	return out
}

// HasToolUse reports whether the message contains any ToolUse part.
func HasToolUse(m Message) bool {
	return len(ToolUses(m)) > 0
}

// WithoutCacheBreakpoints copies parts with CacheBreakpoint markers removed.
// Nested ToolResult parts are copied the same way.
func WithoutCacheBreakpoints(parts []Part) []Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]Part, 0, len(parts))
	for _, p := range parts {
		if _, ok := p.(CacheBreakpoint); ok {
			continue
		}
		if tr, ok := p.(ToolResult); ok {
			tr.Parts = WithoutCacheBreakpoints(tr.Parts)
			out = append(out, tr)
			continue
		}
		out = append(out, p)
	}
	return out
}

// TextContent concatenates all Text parts.
func TextContent(m Message) string {
	var s string
	for _, p := range m.Parts {
		if t, ok := p.(Text); ok {
			s += t.Text
		}
	}
	return s
}

// NewToolResultMessage builds a user message containing tool results.
func NewToolResultMessage(results ...ToolResult) Message {
	parts := make([]Part, len(results))
	for i, r := range results {
		parts[i] = r
	}
	return Message{Role: RoleUser, Parts: parts}
}
