package realtime_test

import (
	"context"
	"testing"

	"github.com/sven-victor/ez-agent/realtime"
)

func TestPipeSession(t *testing.T) {
	ctx := context.Background()
	s := realtime.OpenPipe(8)
	if err := s.SendText(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	ev1 := <-s.Events()
	ev2 := <-s.Events()
	u, ok := ev1.(realtime.TranscriptDelta)
	if !ok || u.Role != "user" || u.Text != "hi" {
		t.Fatalf("ev1: %#v", ev1)
	}
	a, ok := ev2.(realtime.TranscriptDelta)
	if !ok || a.Role != "assistant" {
		t.Fatalf("ev2: %#v", ev2)
	}
	if err := s.SendAudio(ctx, "audio/pcm", []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	<-s.Events()
	audio := <-s.Events()
	if _, ok := audio.(realtime.AudioDelta); !ok {
		t.Fatalf("audio: %#v", audio)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := (<-s.Events()).(realtime.Closed); !ok {
		t.Fatal("expected Closed")
	}
}
