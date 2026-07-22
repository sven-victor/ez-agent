package message_test

import (
	"testing"

	"github.com/sven-victor/ez-agent/message"
)

func TestNewIDUnique(t *testing.T) {
	a, b := message.NewID(), message.NewID()
	if a == "" || b == "" || a == b {
		t.Fatalf("ids=%q %q", a, b)
	}
}

func TestEnsureID(t *testing.T) {
	m := message.NewUserText("hi")
	if !message.EnsureID(&m) || m.ID == "" {
		t.Fatal("expected assign")
	}
	id := m.ID
	if message.EnsureID(&m) || m.ID != id {
		t.Fatalf("should keep id %q got %q", id, m.ID)
	}
}

func TestEnsureIDs(t *testing.T) {
	msgs := []message.Message{
		message.NewUserText("a"),
		{ID: "keep-me", Role: message.RoleAssistant, Parts: []message.Part{message.Text{Text: "b"}}},
	}
	if !message.EnsureIDs(msgs) {
		t.Fatal("expected change")
	}
	if msgs[0].ID == "" || msgs[1].ID != "keep-me" {
		t.Fatalf("%#v", msgs)
	}
	if message.EnsureIDs(msgs) {
		t.Fatal("second pass should be no-op")
	}
}
