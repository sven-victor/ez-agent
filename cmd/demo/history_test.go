package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sven-victor/ez-agent/message"
)

func TestLoadSaveInputHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	items := []string{"你好", "second", "third"}
	if err := saveInputHistory(path, items); err != nil {
		t.Fatal(err)
	}
	got := loadInputHistory(path)
	if len(got) != 3 || got[0] != "你好" || got[2] != "third" {
		t.Fatalf("got=%v", got)
	}
}

func TestSessionToChatSkipsToolResults(t *testing.T) {
	msgs := []message.Message{
		message.NewUserText("hello"),
		{
			Role: message.RoleAssistant,
			Parts: []message.Part{
				message.Text{Text: "hi **there**"},
				message.ToolUse{ID: "1", Name: "now", Input: []byte(`{}`)},
			},
		},
		message.NewToolResultMessage(message.ToolResult{
			ToolUseID: "1",
			OK:        true,
			Parts:     []message.Part{message.Text{Text: "12:00"}},
		}),
		message.NewUserText("thanks"),
	}
	chat, tools := sessionToChat(msgs)
	if len(chat) != 3 {
		t.Fatalf("chat=%d %+v", len(chat), chat)
	}
	if chat[0].kind != chatUser || chat[0].text != "hello" {
		t.Fatalf("chat[0]=%+v", chat[0])
	}
	if chat[1].kind != chatAssistant || chat[1].text != "hi **there**" {
		t.Fatalf("chat[1]=%+v", chat[1])
	}
	if chat[2].kind != chatUser || chat[2].text != "thanks" {
		t.Fatalf("chat[2]=%+v", chat[2])
	}
	if len(tools) != 2 {
		t.Fatalf("tools=%v", tools)
	}
}

func TestLoadSessionMessagesMissing(t *testing.T) {
	wd := t.TempDir()
	if msgs := loadSessionMessages(wd, "nope"); msgs != nil {
		t.Fatalf("got %d msgs", len(msgs))
	}
	_ = os.MkdirAll(filepath.Join(wd, ".sessions"), 0o755)
}
