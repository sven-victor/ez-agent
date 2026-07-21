package loop

import (
	"context"

	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

// Interrupter can pause the run after the model requests tools (HITL).
// When BeforeTools returns interrupt=true, the loop stops with StopInterrupted
// and leaves Pending on State for Resume.
type Interrupter interface {
	BeforeTools(ctx context.Context, st *run.State, calls []tool.Call) (interrupt bool, err error)
}

// FuncInterrupter adapts a function to Interrupter.
type FuncInterrupter func(ctx context.Context, st *run.State, calls []tool.Call) (bool, error)

func (f FuncInterrupter) BeforeTools(ctx context.Context, st *run.State, calls []tool.Call) (bool, error) {
	return f(ctx, st, calls)
}
