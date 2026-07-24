package main

import (
	"strings"
	"testing"
)

func TestRenderAssistantMarkdownBasic(t *testing.T) {
	m := &tuiModel{}
	out := m.renderAssistantMarkdown("**bold** and `code`", 80)
	if out == "" {
		t.Fatal("empty render")
	}
	// Glamour emits ANSI; raw markers should not dominate the output as plain text alone.
	if !strings.Contains(out, "bold") || !strings.Contains(out, "code") {
		t.Fatalf("unexpected render: %q", out)
	}
}

func TestRenderChatContentUsesMarkdown(t *testing.T) {
	m := &tuiModel{
		chat: []chatEntry{
			{kind: chatUser, text: "hi"},
			{kind: chatAssistant, text: "# Title\n\n- item"},
		},
		width: 100,
	}
	m.chatVP.Width = 80
	out := m.renderChatContent()
	if !strings.Contains(out, "you") {
		t.Fatalf("missing user label: %q", out)
	}
	if !strings.Contains(out, "assistant") {
		t.Fatalf("missing assistant label: %q", out)
	}
	if !strings.Contains(out, "Title") {
		t.Fatalf("missing markdown title: %q", out)
	}
}
