package tool

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sven-victor/ez-agent/message"
	"golang.org/x/sync/errgroup"
)

// Concurrent executes tool calls in parallel.
// One failure becomes OK=false; siblings still run. Unknown tools become error results.
type Concurrent struct {
	Registry Registry
	Hooks    CallHooks // optional
}

// Execute runs all calls concurrently and returns results in the same order as calls.
func (e *Concurrent) Execute(ctx context.Context, calls []Call) ([]Result, error) {
	results := make([]Result, len(calls))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	for i, call := range calls {
		i, call := i, call
		g.Go(func() error {
			res := runOne(gctx, e.Registry, e.Hooks, call)
			mu.Lock()
			results[i] = res
			mu.Unlock()
			return nil // never fail the group; pairing requires a result per call
		})
	}
	_ = g.Wait()

	// If parent ctx canceled, ensure every result is filled (errgroup cancels children).
	for i, call := range calls {
		if results[i].ToolUseID == "" {
			results[i] = errorResult(call, ctx.Err())
		}
	}
	return results, nil
}

func runOne(ctx context.Context, reg Registry, hooks CallHooks, call Call) Result {
	// AfterTool runs whenever BeforeTool does not fail (including deny/skip,
	// handler errors, cancel, and unknown tools).
	after := func(res Result) Result {
		if hooks == nil {
			return res
		}
		res, _ = hooks.AfterTool(ctx, call, res)
		return res
	}

	if hooks != nil {
		act, err := hooks.BeforeTool(ctx, call)
		if err != nil {
			// BeforeTool failed: do not run AfterTool.
			return errorResult(call, err)
		}
		if act.Deny || act.Skip {
			reason := act.Reason
			if reason == "" {
				if act.Deny {
					reason = "tool denied by hook"
				} else {
					reason = "tool skipped by hook"
				}
			}
			return after(Result{
				ToolUseID: call.ID,
				OK:        false,
				Content:   []message.Part{message.Text{Text: reason}},
			})
		}
		if len(act.ReplaceInput) > 0 {
			call.Input = act.ReplaceInput
		}
	}

	select {
	case <-ctx.Done():
		return after(errorResult(call, ctx.Err()))
	default:
	}

	if reg == nil {
		return after(errorResult(call, fmt.Errorf("unknown tool: %s", call.Name)))
	}
	h, ok := reg.Lookup(call.Name)
	if !ok {
		return after(Result{
			ToolUseID: call.ID,
			OK:        false,
			Content:   []message.Part{message.Text{Text: fmt.Sprintf("unknown tool: %s", call.Name)}},
		})
	}

	res, err := h(ctx, call)
	// A real handler error still becomes OK=false. ErrEndOfChat keeps the
	// handler result so the loop can see the sentinel on Result.Err.
	if signal := endOfChatSignal(err, res.Err); signal != nil {
		res = prepareEndOfChat(call, res, signal)
		res = after(res)
		if !errors.Is(res.Err, ErrEndOfChat) {
			res.Err = signal
		}
		return res
	}
	if err != nil {
		res = errorResult(call, err)
	} else if res.ToolUseID == "" {
		res.ToolUseID = call.ID
	}
	return after(res)
}

// endOfChatSignal returns the handler's end-of-chat error, if any.
// A different non-nil handler error is a real failure and yields no signal.
func endOfChatSignal(err, resErr error) error {
	if err != nil && !errors.Is(err, ErrEndOfChat) {
		return nil
	}
	if errors.Is(err, ErrEndOfChat) {
		return err
	}
	if errors.Is(resErr, ErrEndOfChat) {
		return resErr
	}
	return nil
}

// prepareEndOfChat keeps handler OK and Content. A zero Result gets the call
// id, OK=true, and empty Content. Err is the sentinel the loop scans for.
func prepareEndOfChat(call Call, res Result, signal error) Result {
	if res.ToolUseID == "" && !res.OK && len(res.Content) == 0 && res.Err == nil {
		return Result{ToolUseID: call.ID, OK: true, Err: signal}
	}
	if res.ToolUseID == "" {
		res.ToolUseID = call.ID
	}
	res.Err = signal
	return res
}

func errorResult(call Call, err error) Result {
	msg := "tool error"
	if err != nil {
		msg = err.Error()
	}
	return Result{
		ToolUseID: call.ID,
		OK:        false,
		Content:   []message.Part{message.Text{Text: msg}},
		Err:       err,
	}
}

// ToMessageParts converts tool results into message ToolResult parts.
func ToMessageParts(results []Result) []message.ToolResult {
	out := make([]message.ToolResult, len(results))
	for i, r := range results {
		out[i] = message.ToolResult{
			ToolUseID: r.ToolUseID,
			OK:        r.OK,
			Parts:     r.Content,
		}
	}
	return out
}
