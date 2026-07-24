package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
)

func isToolResultOnly(m message.Message) bool {
	if len(m.Parts) == 0 {
		return false
	}
	for _, p := range m.Parts {
		if _, ok := p.(message.ToolResult); !ok {
			return false
		}
	}
	return true
}

// sessionToChat converts persisted model-window messages into TUI chat/tool panes.
// Tool-result user messages go to the tools pane; plain user/assistant text to chat.
func sessionToChat(msgs []message.Message) (chat []chatEntry, tools []string) {
	for _, msg := range msgs {
		switch msg.Role {
		case message.RoleUser:
			if isToolResultOnly(msg) {
				for _, p := range msg.Parts {
					tr, ok := p.(message.ToolResult)
					if !ok {
						continue
					}
					status := "ok"
					if !tr.OK {
						status = "err"
					}
					body := truncate(partsText(tr.Parts), 200)
					tools = append(tools, fmt.Sprintf("← [%s] %s", status, body))
				}
				continue
			}
			text := strings.TrimSpace(message.TextContent(msg))
			if text != "" {
				chat = append(chat, chatEntry{kind: chatUser, text: text})
			}
		case message.RoleAssistant:
			text := strings.TrimSpace(message.TextContent(msg))
			if text != "" {
				chat = append(chat, chatEntry{kind: chatAssistant, text: text})
			}
			for _, tu := range message.ToolUses(msg) {
				tools = append(tools, fmt.Sprintf("→ %s(%s)", tu.Name, compactJSON(tu.Input)))
			}
		}
	}
	return chat, tools
}

func loadSessionMessages(wd, sessionID string) []message.Message {
	if sessionID == "" || wd == "" {
		return nil
	}
	store := &memory.FileStore{Dir: filepath.Join(wd, ".sessions")}
	s, err := store.Load(nil, sessionID)
	if err != nil || s == nil {
		return nil
	}
	return s.Messages
}
