package hook_test

import (
	"context"
	"testing"

	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/run"
)

func TestAfterReverseOrder(t *testing.T) {
	r := hook.NewRegistry()
	var order []string
	r.OnAfterRun(func(ctx context.Context, e *hook.AfterRun) error {
		order = append(order, "a")
		return nil
	})
	r.OnAfterRun(func(ctx context.Context, e *hook.AfterRun) error {
		order = append(order, "b")
		return nil
	})
	r.OnBeforeRun(func(ctx context.Context, e *hook.BeforeRun) error {
		order = append(order, "before-a")
		return nil
	})
	r.OnBeforeRun(func(ctx context.Context, e *hook.BeforeRun) error {
		order = append(order, "before-b")
		return nil
	})

	st := &run.State{}
	if err := r.FireBeforeRun(context.Background(), &hook.BeforeRun{State: st}); err != nil {
		t.Fatal(err)
	}
	if err := r.FireAfterRun(context.Background(), &hook.AfterRun{State: st}); err != nil {
		t.Fatal(err)
	}
	want := []string{"before-a", "before-b", "b", "a"}
	if len(order) != len(want) {
		t.Fatalf("got %v want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("got %v want %v", order, want)
		}
	}
}
