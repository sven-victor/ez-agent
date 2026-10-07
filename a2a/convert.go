package a2a

import (
	"encoding/json"
	"fmt"
	"strings"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

func textFromProtocolMessage(m *protocol.Message) string {
	if m == nil {
		return ""
	}
	return textFromParts(m.Parts)
}

func textFromParts(parts protocol.ContentParts) string {
	var b strings.Builder
	for _, p := range parts {
		if p == nil {
			continue
		}
		if t := p.Text(); t != "" {
			b.WriteString(t)
			continue
		}
		switch {
		case p.Raw() != nil:
			b.WriteString("[binary part")
			if p.MediaType != "" {
				b.WriteString(" " + p.MediaType)
			}
			b.WriteString("]")
		case p.URL() != "":
			b.WriteString("[file url: " + string(p.URL()) + "]")
		case p.Data() != nil:
			raw, err := json.Marshal(p.Data())
			if err != nil {
				b.WriteString("[data]")
			} else {
				b.WriteString(string(raw))
			}
		default:
			if p.MediaType != "" {
				b.WriteString("[part " + p.MediaType + "]")
			}
		}
	}
	return b.String()
}

func lastAssistantText(res *run.Result) string {
	if res == nil {
		return ""
	}
	for i := len(res.Messages) - 1; i >= 0; i-- {
		m := res.Messages[i]
		if m.Role != message.RoleAssistant {
			continue
		}
		if t := message.TextContent(m); t != "" {
			return t
		}
	}
	return ""
}

func textFromSendResult(res protocol.SendMessageResult) string {
	if res == nil {
		return ""
	}
	switch v := res.(type) {
	case *protocol.Message:
		return textFromProtocolMessage(v)
	case *protocol.Task:
		return textFromTask(v)
	default:
		return fmt.Sprintf("%v", res)
	}
}

func textFromTask(t *protocol.Task) string {
	if t == nil {
		return ""
	}
	if t.Status.Message != nil {
		if s := textFromProtocolMessage(t.Status.Message); s != "" {
			return s
		}
	}
	for i := len(t.Artifacts) - 1; i >= 0; i-- {
		if t.Artifacts[i] == nil {
			continue
		}
		if s := textFromParts(t.Artifacts[i].Parts); s != "" {
			return s
		}
	}
	for i := len(t.History) - 1; i >= 0; i-- {
		if s := textFromProtocolMessage(t.History[i]); s != "" {
			return s
		}
	}
	if t.Status.State != "" {
		return string(t.Status.State)
	}
	return ""
}
