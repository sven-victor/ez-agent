package tool

import (
	"context"
)

// Sequential executes tool calls one after another in call order.
type Sequential struct {
	Registry Registry
	Hooks    CallHooks
}

// Execute runs calls sequentially and returns results in the same order.
func (e *Sequential) Execute(ctx context.Context, calls []Call) ([]Result, error) {
	results := make([]Result, len(calls))
	for i, call := range calls {
		select {
		case <-ctx.Done():
			for j := i; j < len(calls); j++ {
				results[j] = errorResult(calls[j], ctx.Err())
			}
			return results, nil
		default:
		}
		results[i] = runOne(ctx, e.Registry, e.Hooks, call)
	}
	return results, nil
}
