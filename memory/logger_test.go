package memory_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

type capturingLogger struct {
	msgs []string
}

func (c *capturingLogger) Debug(msg string, _ ...any) { c.msgs = append(c.msgs, "D:"+msg) }
func (c *capturingLogger) Info(msg string, _ ...any)  { c.msgs = append(c.msgs, "I:"+msg) }
func (c *capturingLogger) Warn(msg string, _ ...any)  { c.msgs = append(c.msgs, "W:"+msg) }
func (c *capturingLogger) Error(msg string, _ ...any) { c.msgs = append(c.msgs, "E:"+msg) }

func TestSetLoggerPropagates(t *testing.T) {
	log := &capturingLogger{}
	robust := &memory.RobustSummarizer{}
	seg := &memory.SegmentedSummarizer{}
	win := &memory.Window{MaxMessages: 5}
	pol := &memory.Summarize{
		Summarizer: robust,
		Segmented:  seg,
		Inner:      win,
		Fallback:   &memory.Window{MaxMessages: 3},
	}
	memory.SetLogger(pol, log)
	if pol.Logger != log {
		t.Fatal("Summarize.Logger not set")
	}
	if robust.Logger != log || seg.Logger != log {
		t.Fatal("summarizer Logger not set")
	}
	if win.Logger != log {
		t.Fatal("Inner Window.Logger not set")
	}
	if pol.Fallback.(*memory.Window).Logger != log {
		t.Fatal("Fallback Window.Logger not set")
	}

	// Does not overwrite existing logger.
	other := logger.Nop()
	pol2 := &memory.Summarize{Logger: other, Summarizer: &memory.ProviderSummarizer{}}
	memory.SetLogger(pol2, log)
	if pol2.Logger != other {
		t.Fatal("existing Logger overwritten")
	}
}

func TestSummarizePrepareLogs(t *testing.T) {
	log := &capturingLogger{}
	st := &run.State{}
	for i := 0; i < 6; i++ {
		st.AppendMessage(message.NewUserText("m"))
	}
	pol := &memory.Summarize{
		Summarizer:           &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		KeepRecent:           2,
		ProactiveMaxMessages: 3,
		Logger:               log,
	}
	if err := pol.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(log.msgs, "|")
	for _, want := range []string{"proactive summarize", "summarizing oldest messages"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, log.msgs)
		}
	}
}

func TestWindowTrimLogs(t *testing.T) {
	log := &capturingLogger{}
	st := &run.State{}
	for i := 0; i < 5; i++ {
		st.AppendMessage(message.NewUserText("m"))
	}
	w := &memory.Window{MaxMessages: 2, Logger: log}
	if err := w.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != 2 {
		t.Fatalf("got %d messages", len(st.Messages))
	}
	joined := strings.Join(log.msgs, "|")
	if !strings.Contains(joined, "window trimmed") {
		t.Fatalf("missing trim log: %v", log.msgs)
	}
}
