package tool_test

import (
	"context"
	"testing"

	"github.com/sven-victor/ez-agent/tool"
)

type staticReg struct {
	specs []tool.Spec
	hands map[string]tool.Handler
}

func (s staticReg) Specs() []tool.Spec { return s.specs }
func (s staticReg) Lookup(name string) (tool.Handler, bool) {
	h, ok := s.hands[name]
	return h, ok
}

func TestMultiLiveRefresh(t *testing.T) {
	local := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "local", InputSchema: []byte(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	})
	dyn := &mutableReg{byName: map[string]tool.Spec{}}
	m := tool.NewMulti(local, dyn)

	if len(m.Specs()) != 1 {
		t.Fatalf("specs=%d", len(m.Specs()))
	}
	dyn.set("mcp_a", tool.Spec{Name: "mcp_a", InputSchema: []byte(`{}`)})
	specs := m.Specs()
	if len(specs) != 2 {
		t.Fatalf("after refresh specs=%d", len(specs))
	}
	if _, ok := m.Lookup("mcp_a"); !ok {
		t.Fatal("lookup mcp_a")
	}
}

type mutableReg struct {
	byName map[string]tool.Spec
}

func (m *mutableReg) set(name string, spec tool.Spec) {
	m.byName[name] = spec
}
func (m *mutableReg) Specs() []tool.Spec {
	out := make([]tool.Spec, 0, len(m.byName))
	for _, s := range m.byName {
		out = append(out, s)
	}
	return out
}
func (m *mutableReg) Lookup(name string) (tool.Handler, bool) {
	if _, ok := m.byName[name]; !ok {
		return nil, false
	}
	return func(ctx context.Context, call tool.Call) (tool.Result, error) {
		return tool.Result{ToolUseID: call.ID, OK: true}, nil
	}, true
}
