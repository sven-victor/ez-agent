package memoryplugin_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sven-victor/ez-agent/contrib/memoryplugin"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/plugin"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func TestMemoryPluginTools(t *testing.T) {
	ctx := context.Background()
	store := memory.NewMemLongTerm()
	h := plugin.NewHost(nil, nil)
	p := &memoryplugin.Plugin{Store: store}
	if err := p.Install(h); err != nil {
		t.Fatal(err)
	}
	remember, ok := h.Tools.Lookup("remember")
	if !ok {
		t.Fatal("remember missing")
	}
	res, err := remember(ctx, tool.Call{ID: "1", Input: json.RawMessage(`{"content":"prefers dark mode"}`)})
	if err != nil || !res.OK {
		t.Fatalf("remember: %v %#v", err, res)
	}
	search, ok := h.Tools.Lookup("search_memory")
	if !ok {
		t.Fatal("search_memory missing")
	}
	res, err = search(ctx, tool.Call{ID: "2", Input: json.RawMessage(`{"query":"dark"}`)})
	if err != nil || !res.OK {
		t.Fatalf("search: %v %#v", err, res)
	}
	if message.TextContent(message.Message{Parts: res.Content}) == "(no matches)" {
		t.Fatal("expected match")
	}
}

func TestMemoryPluginInject(t *testing.T) {
	ctx := context.Background()
	store := memory.NewMemLongTerm()
	_ = store.Add(ctx, memory.Item{Content: "user timezone is UTC+8"})
	h := plugin.NewHost(nil, nil)
	p := &memoryplugin.Plugin{Store: store, Inject: true}
	if err := p.Install(h); err != nil {
		t.Fatal(err)
	}
	st := &run.State{}
	st.InitHistory([]message.Message{message.NewUserText("what is my timezone?")})
	if err := h.Hooks.FireBeforeModel(ctx, &hook.BeforeModel{State: st}); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) < 2 {
		t.Fatalf("expected inject, got %d", len(st.Messages))
	}
	if message.TextContent(st.Messages[0]) == "" || len(st.Transcript) != 1 {
		t.Fatalf("inject should touch Messages only: msgs=%d transcript=%d", len(st.Messages), len(st.Transcript))
	}
	if err := h.Hooks.FireAfterModel(ctx, &hook.AfterModel{State: st}); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != 1 {
		t.Fatalf("after strip want 1 message, got %d", len(st.Messages))
	}
}
