package main

import (
	"context"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/run"
)

// statusSnapMsg is a live status bar snapshot pushed from agent hooks.
type statusSnapMsg struct {
	msgs   int
	tokens int
	turn   int
	usage  run.Usage
}

func snapFromState(st *run.State) statusSnapMsg {
	if st == nil {
		return statusSnapMsg{}
	}
	return statusSnapMsg{
		msgs:   len(st.Messages),
		tokens: memory.EstimateTokens(st.Messages),
		turn:   st.Turn,
		usage:  st.Usage,
	}
}

func emitStatus(ch chan<- statusSnapMsg, st *run.State) {
	if ch == nil || st == nil {
		return
	}
	snap := snapFromState(st)
	select {
	case ch <- snap:
	default:
		// drop rather than block the loop
	}
}

// attachStatusHooks pushes model-window stats into ch at key loop points so the
// TUI status bar updates mid-run (not only after RunFinished).
func attachStatusHooks(a *agent.Agent, ch chan<- statusSnapMsg) {
	if a == nil || ch == nil {
		return
	}
	h := a.Hooks()
	h.OnBeforeRun(func(_ context.Context, e *hook.BeforeRun) error {
		emitStatus(ch, e.State)
		return nil
	})
	h.OnBeforeModel(func(_ context.Context, e *hook.BeforeModel) error {
		emitStatus(ch, e.State)
		return nil
	})
	h.OnAfterModel(func(_ context.Context, e *hook.AfterModel) error {
		emitStatus(ch, e.State)
		return nil
	})
	h.OnAfterTool(func(_ context.Context, e *hook.AfterTool) error {
		emitStatus(ch, e.State)
		return nil
	})
	h.OnAfterRun(func(_ context.Context, e *hook.AfterRun) error {
		emitStatus(ch, e.State)
		return nil
	})
}
