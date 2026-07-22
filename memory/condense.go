package memory

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sven-victor/ez-agent/message"
)

const defaultCondenseNotice = "\n...[truncated for context budget]"

// CondenseOversizedParts returns a copy of msgs where oversized Text / ToolResult
// text parts are truncated to maxBytes (UTF-8 safe). maxBytes <= 0 leaves msgs unchanged
// (still copied).
func CondenseOversizedParts(msgs []message.Message, maxBytes int) []message.Message {
	out := make([]message.Message, len(msgs))
	for i, m := range msgs {
		out[i] = message.Message{
			ID:    m.ID,
			Role:  m.Role,
			Parts: condenseParts(m.Parts, maxBytes),
		}
	}
	return out
}

func condenseParts(parts []message.Part, maxBytes int) []message.Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]message.Part, len(parts))
	for i, p := range parts {
		out[i] = condensePart(p, maxBytes)
	}
	return out
}

func condensePart(p message.Part, maxBytes int) message.Part {
	switch v := p.(type) {
	case message.Text:
		if maxBytes > 0 && len(v.Text) > maxBytes {
			v.Text = truncateUTF8ByBytes(v.Text, maxBytes) + defaultCondenseNotice
		}
		return v
	case message.ToolResult:
		v.Parts = condenseParts(v.Parts, maxBytes)
		return v
	case message.Reasoning:
		if maxBytes > 0 && len(v.Text) > maxBytes {
			v.Text = truncateUTF8ByBytes(v.Text, maxBytes) + defaultCondenseNotice
		}
		return v
	default:
		return p
	}
}

// truncateUTF8ByBytes shortens s to at most maxBytes without splitting a rune.
func truncateUTF8ByBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		return s[:maxBytes]
	}
	return s[:cut]
}

// lastUserText returns the latest non-empty user text, optionally truncated.
func lastUserText(msgs []message.Message, maxBytes int) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != message.RoleUser {
			continue
		}
		t := strings.TrimSpace(flattenMessageText(msgs[i]))
		if t == "" {
			continue
		}
		if maxBytes > 0 && len(t) > maxBytes {
			return truncateUTF8ByBytes(t, maxBytes) + "\n...[truncated for summary context]"
		}
		return t
	}
	return ""
}

// flattenMessageText extracts a plain-text view of a message for summarization.
func flattenMessageText(m message.Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		switch v := p.(type) {
		case message.Text:
			b.WriteString(v.Text)
		case message.Reasoning:
			if v.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString("[reasoning] ")
				b.WriteString(v.Text)
			}
		case message.ToolUse:
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "[tool_use %s id=%s] %s", v.Name, v.ID, string(v.Input))
		case message.ToolResult:
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			status := "ok"
			if !v.OK {
				status = "error"
			}
			fmt.Fprintf(&b, "[tool_result id=%s %s] ", v.ToolUseID, status)
			for _, sp := range v.Parts {
				if t, ok := sp.(message.Text); ok {
					b.WriteString(t.Text)
				}
			}
		}
	}
	return b.String()
}

func flattenMessagesText(msgs []message.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n---\n")
		}
		fmt.Fprintf(&b, "[%s] %s", m.Role, flattenMessageText(m))
	}
	return b.String()
}

func summaryMessage(text string) message.Message {
	return message.Message{
		ID:    message.NewID(),
		Role:  message.RoleUser,
		Parts: []message.Part{message.Text{Text: text}},
	}
}
