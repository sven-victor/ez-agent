package tool_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/tool"
)

func TestSequentialOrder(t *testing.T) {
	var order []int
	var mu atomic.Int32
	reg := tool.NewRegistry(
		tool.Registration{
			Spec: tool.Spec{Name: "a", InputSchema: []byte(`{}`)},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				order = append(order, 1)
				mu.Add(1)
				time.Sleep(5 * time.Millisecond)
				return tool.Result{ToolUseID: call.ID, OK: true}, nil
			},
		},
		tool.Registration{
			Spec: tool.Spec{Name: "b", InputSchema: []byte(`{}`)},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				order = append(order, 2)
				return tool.Result{ToolUseID: call.ID, OK: true}, nil
			},
		},
	)
	ex := &tool.Sequential{Registry: reg}
	res, err := ex.Execute(context.Background(), []tool.Call{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].OK || !res[1].OK {
		t.Fatalf("%#v", res)
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("order=%v", order)
	}
}
