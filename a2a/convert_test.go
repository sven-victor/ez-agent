package a2a

import (
	"testing"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

func TestTextFromParts(t *testing.T) {
	got := textFromParts(protocol.ContentParts{
		protocol.NewTextPart("hello"),
		protocol.NewTextPart(" world"),
	})
	if got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestTextFromNonTextParts(t *testing.T) {
	got := textFromParts(protocol.ContentParts{
		protocol.NewFileURLPart("https://example.com/a.png", "image/png"),
	})
	if got != "[file url: https://example.com/a.png]" {
		t.Fatalf("got %q", got)
	}
}

func TestLastAssistantText(t *testing.T) {
	res := &run.Result{Messages: []message.Message{
		message.NewUserText("q"),
		message.NewAssistantText("a1"),
		message.NewUserText("tool"),
		message.NewAssistantText("final"),
	}}
	if got := lastAssistantText(res); got != "final" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeToolName(t *testing.T) {
	if got := sanitizeToolName("Hello World!"); got != "Hello_World" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeToolName("9abc"); got != "a_9abc" {
		t.Fatalf("got %q", got)
	}
}
