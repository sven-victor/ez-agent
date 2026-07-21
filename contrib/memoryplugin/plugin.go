// Package memoryplugin provides a LongTerm memory Plugin example (P2).
//
// It registers remember / search_memory tools and optionally injects
// retrieved facts into the model window on BeforeModel. Nothing here is
// wired into loop by default — install via agent.WithPlugins.
package memoryplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/plugin"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

const injectMarker = "[longterm_memory]"

// Plugin exposes LongTerm via tools and optional BeforeModel injection.
type Plugin struct {
	Store  memory.LongTerm
	Inject bool // when true, search last user text and prepend a memory note
	TopK   int
}

func (p *Plugin) Name() string { return "longterm_memory" }

func (p *Plugin) Install(h *plugin.Host) error {
	if p.Store == nil {
		return fmt.Errorf("memoryplugin: Store is required")
	}
	k := p.TopK
	if k <= 0 {
		k = 5
	}
	store := p.Store

	h.RegisterTool(tool.Registration{
		Spec: tool.Spec{
			Name:        "remember",
			Description: "Store a fact in long-term memory for later retrieval.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string"},"id":{"type":"string"}},"required":["content"]}`),
		},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var in struct {
				Content string `json:"content"`
				ID      string `json:"id"`
			}
			_ = json.Unmarshal(call.Input, &in)
			if strings.TrimSpace(in.Content) == "" {
				return tool.Result{ToolUseID: call.ID, OK: false, Content: []message.Part{message.Text{Text: "content required"}}}, nil
			}
			if err := store.Add(ctx, memory.Item{ID: in.ID, Content: in.Content}); err != nil {
				return tool.Result{ToolUseID: call.ID, OK: false, Content: []message.Part{message.Text{Text: err.Error()}}}, nil
			}
			return tool.Result{ToolUseID: call.ID, OK: true, Content: []message.Part{message.Text{Text: "ok"}}}, nil
		},
	})

	h.RegisterTool(tool.Registration{
		Spec: tool.Spec{
			Name:        "search_memory",
			Description: "Search long-term memory for relevant facts.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"k":{"type":"integer"}},"required":["query"]}`),
		},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var in struct {
				Query string `json:"query"`
				K     int    `json:"k"`
			}
			_ = json.Unmarshal(call.Input, &in)
			n := in.K
			if n <= 0 {
				n = k
			}
			items, err := store.Search(ctx, in.Query, n)
			if err != nil {
				return tool.Result{ToolUseID: call.ID, OK: false, Content: []message.Part{message.Text{Text: err.Error()}}}, nil
			}
			var b strings.Builder
			if len(items) == 0 {
				b.WriteString("(no matches)")
			} else {
				for i, it := range items {
					if i > 0 {
						b.WriteByte('\n')
					}
					fmt.Fprintf(&b, "- [%s] %s", it.ID, it.Content)
				}
			}
			return tool.Result{ToolUseID: call.ID, OK: true, Content: []message.Part{message.Text{Text: b.String()}}}, nil
		},
	})

	if p.Inject {
		h.OnBeforeModel(func(ctx context.Context, e *hook.BeforeModel) error {
			stripInjected(e.State)
			q := lastUserText(e.State.Messages)
			if q == "" {
				return nil
			}
			items, err := store.Search(ctx, q, k)
			if err != nil || len(items) == 0 {
				return err
			}
			var b strings.Builder
			b.WriteString(injectMarker)
			b.WriteString("\nRelevant long-term memory:\n")
			for _, it := range items {
				fmt.Fprintf(&b, "- %s\n", it.Content)
			}
			// Inject into model window only (not Transcript).
			e.State.Messages = append([]message.Message{{
				Role:  message.RoleUser,
				Parts: []message.Part{message.Text{Text: b.String()}},
			}}, e.State.Messages...)
			return nil
		})
		h.OnAfterModel(func(_ context.Context, e *hook.AfterModel) error {
			stripInjected(e.State)
			return nil
		})
	}
	return nil
}

func stripInjected(st *run.State) {
	if st == nil {
		return
	}
	out := st.Messages[:0]
	for _, m := range st.Messages {
		if m.Role == message.RoleUser && strings.HasPrefix(message.TextContent(m), injectMarker) {
			continue
		}
		out = append(out, m)
	}
	st.Messages = out
}

func lastUserText(msgs []message.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != message.RoleUser {
			continue
		}
		t := message.TextContent(msgs[i])
		if t != "" && !strings.HasPrefix(t, injectMarker) {
			return t
		}
	}
	return ""
}
