package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

const (
	defaultMaxOverflowRetry   = 3
	defaultMaxAfterModelRetry = 3
)

// Config holds loop dependencies.
type Config struct {
	Provider      model.Provider
	Tools         tool.Registry
	Executor      tool.Executor
	Hooks         *hook.Registry
	Policy        memory.ContextPolicy
	Limits        run.Limits
	System        model.System
	MaxOverflow   int
	MaxModelRetry int
	OutputSchema  []byte
	OutputName    string
	RetryPolicy     model.RetryPolicy
	Interrupter     Interrupter
	ToolChoice      tool.Choice
	CheckpointStore memory.CheckpointStore
	CheckpointID    string
	// SessionStore + SessionID persist the effective model window mid-run.
	// Append on new messages; ReplaceAll after context Prepare/Reduce shrinks the window.
	SessionStore memory.SessionStore
	SessionID    string
	// PreferComplete uses Provider.Complete instead of Stream for model calls.
	PreferComplete bool
}

// Loop is the turn state machine.
type Loop struct {
	cfg Config
}

// New creates a Loop.
func New(cfg Config) *Loop {
	if cfg.Policy == nil {
		cfg.Policy = memory.Noop{}
	}
	if cfg.Hooks == nil {
		cfg.Hooks = hook.NewRegistry()
	}
	if cfg.MaxOverflow <= 0 {
		cfg.MaxOverflow = defaultMaxOverflowRetry
	}
	if cfg.MaxModelRetry <= 0 {
		cfg.MaxModelRetry = defaultMaxAfterModelRetry
	}
	return &Loop{cfg: cfg}
}

// EmitFunc receives streaming events during a run.
type EmitFunc func(event.Event) error

// Run executes the agent loop until a terminal stop reason.
func (l *Loop) Run(ctx context.Context, st *run.State, emit EmitFunc) (res *run.Result, err error) {
	if st.Values == nil {
		st.Values = map[string]any{}
	}
	if len(st.Transcript) == 0 && len(st.Messages) > 0 {
		st.Transcript = append([]message.Message(nil), st.Messages...)
	}
	if len(st.Messages) == 0 && len(st.Transcript) > 0 {
		st.Messages = append([]message.Message(nil), st.Transcript...)
	}
	if emit == nil {
		emit = func(event.Event) error { return nil }
	}

	_ = emit(event.RunStarted{})

	if err := l.cfg.Hooks.FireBeforeRun(ctx, &hook.BeforeRun{State: st}); err != nil {
		_ = emit(event.RunError{Err: err})
		return nil, err
	}

	defer func() {
		after := &hook.AfterRun{State: st, Result: res}
		_ = l.cfg.Hooks.FireAfterRun(context.WithoutCancel(ctx), after)
		if res != nil {
			l.saveCheckpoint(context.WithoutCancel(ctx), st)
		}
	}()

	modelRetries := 0
	overflowRetries := 0
	providerRetries := 0

	for {
		// HITL resume: run pending tools first, then continue to the model.
		if st.Pending != nil && len(st.Pending.Calls) > 0 {
			if err := l.executePending(ctx, st, emit); err != nil {
				_ = emit(event.RunError{Err: err})
				return nil, err
			}
			st.Pending = nil
			st.Turn++
			continue
		}

		if reason := checkLimits(st, l.cfg.Limits); reason != "" {
			res = stop(st, reason)
			_ = emit(event.RunFinished{Result: res})
			return res, nil
		}
		select {
		case <-ctx.Done():
			res = l.finishCanceled(ctx, st)
			_ = emit(event.Cancelled{})
			_ = emit(event.RunFinished{Result: res})
			return res, ctx.Err()
		default:
		}

		beforePrepare := append([]message.Message(nil), st.Messages...)
		if err := l.cfg.Policy.Prepare(ctx, st); err != nil {
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		if err := l.syncSessionWindow(ctx, beforePrepare, st.Messages); err != nil {
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		if err := l.cfg.Hooks.FireBeforeModel(ctx, &hook.BeforeModel{State: st}); err != nil {
			_ = emit(event.RunError{Err: err})
			return nil, err
		}

		req := model.Request{
			Messages:     append([]message.Message(nil), st.Messages...),
			System:       l.cfg.System,
			OutputSchema: l.cfg.OutputSchema,
			OutputName:   l.cfg.OutputName,
			MaxTokens:    l.cfg.Limits.MaxOutputTokens,
			ToolChoice:   l.cfg.ToolChoice,
		}
		if l.cfg.Tools != nil {
			req.Tools = l.cfg.Tools.Specs()
		}
		if err := model.Validate(l.cfg.Provider, req); err != nil {
			_ = emit(event.RunError{Err: err})
			return nil, err
		}

		var mres model.Result
		var err error
		if l.cfg.PreferComplete {
			mres, err = l.cfg.Provider.Complete(ctx, req)
			if err == nil {
				_ = emit(event.ModelFinished{
					Message:    mres.Message,
					StopReason: mres.StopReason,
					Usage:      mres.Usage,
				})
			}
		} else {
			mres, err = l.cfg.Provider.Stream(ctx, req, emit)
		}
		if err != nil {
			if errors.Is(err, model.ErrOverflow) {
				overflowRetries++
				if overflowRetries > l.cfg.MaxOverflow {
					_ = emit(event.RunError{Err: fmt.Errorf("overflow retries exceeded: %w", err)})
					return nil, err
				}
				beforeReduce := append([]message.Message(nil), st.Messages...)
				if rerr := l.cfg.Policy.Reduce(ctx, st); rerr != nil {
					_ = emit(event.RunError{Err: rerr})
					return nil, rerr
				}
				if serr := l.syncSessionWindow(ctx, beforeReduce, st.Messages); serr != nil {
					_ = emit(event.RunError{Err: serr})
					return nil, serr
				}
				continue
			}
			if l.cfg.RetryPolicy != nil {
				providerRetries++
				if wait, ok := l.cfg.RetryPolicy.ShouldRetry(providerRetries, err); ok {
					select {
					case <-ctx.Done():
						res = l.finishCanceled(ctx, st)
						_ = emit(event.Cancelled{})
						_ = emit(event.RunFinished{Result: res})
						return res, ctx.Err()
					case <-time.After(wait):
						continue
					}
				}
			}
			select {
			case <-ctx.Done():
				res = l.finishCanceled(ctx, st)
				_ = emit(event.Cancelled{})
				_ = emit(event.RunFinished{Result: res})
				return res, ctx.Err()
			default:
			}
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		overflowRetries = 0
		providerRetries = 0

		msg := sanitizeIncompleteToolUse(mres)
		if len(l.cfg.OutputSchema) > 0 && msg.StopReason != run.StopToolUse {
			structured, verr := validateStructuredJSON(msg.Message)
			if verr != nil {
				modelRetries++
				if modelRetries > l.cfg.MaxModelRetry {
					err := fmt.Errorf("structured output invalid: %w", verr)
					_ = emit(event.RunError{Err: err})
					return nil, err
				}
				continue
			}
			if err := l.appendMessage(ctx, st, msg.Message); err != nil {
				_ = emit(event.RunError{Err: err})
				return nil, err
			}
			st.Usage.Add(msg.Usage)
			res = stop(st, msg.StopReason)
			res.Structured = structured
			_ = emit(event.RunFinished{Result: res})
			return res, nil
		}

		if err := l.appendMessage(ctx, st, msg.Message); err != nil {
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		st.Usage.Add(msg.Usage)

		after := &hook.AfterModel{State: st, Result: msg}
		if err := l.cfg.Hooks.FireAfterModel(ctx, after); err != nil {
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		if after.ReplaceMessage != nil {
			st.ReplaceLastMessage(*after.ReplaceMessage)
			msg.Message = *after.ReplaceMessage
			if err := l.sessionReplaceAll(ctx, st.Messages); err != nil {
				_ = emit(event.RunError{Err: err})
				return nil, err
			}
		}
		if after.Retry {
			modelRetries++
			if modelRetries > l.cfg.MaxModelRetry {
				err := fmt.Errorf("after-model retry limit exceeded")
				_ = emit(event.RunError{Err: err})
				return nil, err
			}
			if l.cfg.RetryPolicy != nil {
				if wait, ok := l.cfg.RetryPolicy.ShouldRetry(modelRetries, fmt.Errorf("after-model retry")); ok {
					select {
					case <-ctx.Done():
						res = l.finishCanceled(ctx, st)
						_ = emit(event.Cancelled{})
						_ = emit(event.RunFinished{Result: res})
						return res, ctx.Err()
					case <-time.After(wait):
					}
				}
			}
			st.PopLastMessage()
			if err := l.sessionReplaceAll(ctx, st.Messages); err != nil {
				_ = emit(event.RunError{Err: err})
				return nil, err
			}
			continue
		}
		modelRetries = 0

		if msg.StopReason != run.StopToolUse {
			res = stop(st, msg.StopReason)
			_ = emit(event.RunFinished{Result: res})
			return res, nil
		}

		calls := toolCalls(msg.Message)
		if len(calls) == 0 {
			res = stop(st, run.StopEndTurn)
			_ = emit(event.RunFinished{Result: res})
			return res, nil
		}

		if l.cfg.Interrupter != nil {
			interrupt, ierr := l.cfg.Interrupter.BeforeTools(ctx, st, calls)
			if ierr != nil {
				_ = emit(event.RunError{Err: ierr})
				return nil, ierr
			}
			if interrupt {
				pending := pendingFromCalls(calls)
				st.Pending = pending
				res = stop(st, run.StopInterrupted)
				res.Pending = pending
				_ = emit(event.Interrupted{Pending: pending})
				_ = emit(event.RunFinished{Result: res})
				return res, nil
			}
		}

		if err := l.runTools(ctx, st, emit, calls); err != nil {
			return nil, err
		}
		st.Turn++
		l.saveCheckpoint(ctx, st)
	}
}

func (l *Loop) saveCheckpoint(ctx context.Context, st *run.State) {
	if l.cfg.CheckpointStore == nil || l.cfg.CheckpointID == "" || st == nil {
		return
	}
	_ = l.cfg.CheckpointStore.Save(ctx, l.cfg.CheckpointID, st)
}

func (l *Loop) appendMessage(ctx context.Context, st *run.State, m message.Message) error {
	st.AppendMessage(m)
	return l.sessionAppend(ctx, m)
}

func (l *Loop) sessionAppend(ctx context.Context, msgs ...message.Message) error {
	if l.cfg.SessionStore == nil || l.cfg.SessionID == "" || len(msgs) == 0 {
		return nil
	}
	return l.cfg.SessionStore.Append(ctx, l.cfg.SessionID, msgs...)
}

func (l *Loop) sessionReplaceAll(ctx context.Context, msgs []message.Message) error {
	if l.cfg.SessionStore == nil || l.cfg.SessionID == "" {
		return nil
	}
	return l.cfg.SessionStore.ReplaceAll(ctx, l.cfg.SessionID, msgs)
}

func (l *Loop) syncSessionWindow(ctx context.Context, before, after []message.Message) error {
	if sameMessages(before, after) {
		return nil
	}
	return l.sessionReplaceAll(ctx, after)
}

func sameMessages(a, b []message.Message) bool {
	return reflect.DeepEqual(a, b)
}

func (l *Loop) executePending(ctx context.Context, st *run.State, emit EmitFunc) error {
	calls := make([]tool.Call, len(st.Pending.Calls))
	for i, c := range st.Pending.Calls {
		calls[i] = tool.Call{ID: c.ID, Name: c.Name, Input: c.Input}
	}
	return l.runTools(ctx, st, emit, calls)
}

func (l *Loop) runTools(ctx context.Context, st *run.State, emit EmitFunc, calls []tool.Call) error {
	exec := l.executor(st)
	for _, call := range calls {
		_ = emit(event.ToolStarted{Call: call})
	}
	results, err := exec.Execute(ctx, calls)
	if err != nil {
		if len(results) == len(calls) {
			for i, call := range calls {
				_ = emit(event.ToolFinished{Call: call, Result: results[i]})
			}
			if aerr := l.appendMessage(ctx, st, message.NewToolResultMessage(tool.ToMessageParts(results)...)); aerr != nil {
				_ = emit(event.RunError{Err: aerr})
				return aerr
			}
		} else {
			if aerr := l.appendMessage(ctx, st, syntheticErrorResults(calls, err)); aerr != nil {
				_ = emit(event.RunError{Err: aerr})
				return aerr
			}
		}
		select {
		case <-ctx.Done():
			_ = emit(event.Cancelled{})
			_ = emit(event.RunFinished{Result: stop(st, run.StopCanceled)})
			return ctx.Err()
		default:
		}
		_ = emit(event.RunError{Err: err})
		return err
	}
	for i, call := range calls {
		_ = emit(event.ToolFinished{Call: call, Result: results[i]})
	}
	return l.appendMessage(ctx, st, message.NewToolResultMessage(tool.ToMessageParts(results)...))
}

func pendingFromCalls(calls []tool.Call) *run.PendingTools {
	p := &run.PendingTools{Calls: make([]run.PendingCall, len(calls))}
	for i, c := range calls {
		p.Calls[i] = run.PendingCall{ID: c.ID, Name: c.Name, Input: c.Input}
	}
	return p
}

func (l *Loop) executor(st *run.State) tool.Executor {
	hooks := &hook.ToolAdapter{Registry: l.cfg.Hooks, State: st}
	if l.cfg.Executor != nil {
		switch e := l.cfg.Executor.(type) {
		case *tool.Concurrent:
			return &tool.Concurrent{Registry: e.Registry, Hooks: hooks}
		case *tool.Sequential:
			return &tool.Sequential{Registry: e.Registry, Hooks: hooks}
		default:
			return l.cfg.Executor
		}
	}
	reg := l.cfg.Tools
	if reg == nil {
		reg = tool.NewRegistry()
	}
	return &tool.Concurrent{Registry: reg, Hooks: hooks}
}

func sanitizeIncompleteToolUse(mres model.Result) model.Result {
	// max_tokens: never execute tool calls (args may look valid yet be truncated mid-stream).
	forceStripAll := mres.StopReason == run.StopMaxTokens
	var parts []message.Part
	stripped := false
	for _, p := range mres.Message.Parts {
		tu, ok := p.(message.ToolUse)
		if !ok {
			parts = append(parts, p)
			continue
		}
		if forceStripAll || !completeToolUse(tu) {
			stripped = true
			continue
		}
		parts = append(parts, tu)
	}
	if !stripped {
		return mres
	}
	note := "[incomplete tool call omitted]"
	if forceStripAll {
		note = "[incomplete tool call omitted due to max_tokens]"
	}
	parts = append(parts, message.Text{Text: note})
	mres.Message.Parts = parts
	if len(message.ToolUses(mres.Message)) == 0 && mres.StopReason == run.StopToolUse {
		if forceStripAll {
			mres.StopReason = run.StopMaxTokens
		} else {
			mres.StopReason = run.StopEndTurn
		}
	}
	return mres
}

// completeToolUse reports whether a tool call is safe to execute.
func completeToolUse(tu message.ToolUse) bool {
	if tu.ID == "" || tu.Name == "" {
		return false
	}
	input := tu.Input
	if len(input) == 0 {
		input = []byte("{}")
	}
	if !json.Valid(input) {
		return false
	}
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return false
	}
	_, ok := v.(map[string]any)
	return ok
}

func toolCalls(m message.Message) []tool.Call {
	uses := message.ToolUses(m)
	out := make([]tool.Call, len(uses))
	for i, u := range uses {
		out[i] = tool.Call{ID: u.ID, Name: u.Name, Input: u.Input}
	}
	return out
}

func syntheticErrorResults(calls []tool.Call, err error) message.Message {
	results := make([]message.ToolResult, len(calls))
	msg := "tool error"
	if err != nil {
		msg = err.Error()
	}
	for i, c := range calls {
		results[i] = message.ToolResult{
			ToolUseID: c.ID,
			OK:        false,
			Parts:     []message.Part{message.Text{Text: msg}},
		}
	}
	return message.NewToolResultMessage(results...)
}

func validateStructuredJSON(m message.Message) ([]byte, error) {
	text := strings.TrimSpace(message.TextContent(m))
	if text == "" {
		return nil, fmt.Errorf("empty model text")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return nil, err
	}
	return []byte(text), nil
}

func checkLimits(st *run.State, lim run.Limits) run.StopReason {
	if lim.MaxTurns > 0 && st.Turn >= lim.MaxTurns {
		return run.StopMaxTurns
	}
	if lim.MaxTotalTokens > 0 && st.Usage.TotalTokens >= lim.MaxTotalTokens {
		return run.StopMaxTokens
	}
	return ""
}

func stop(st *run.State, reason run.StopReason) *run.Result {
	src := st.Transcript
	if len(src) == 0 {
		src = st.Messages
	}
	return &run.Result{
		Messages:   append([]message.Message(nil), src...),
		StopReason: reason,
		Usage:      st.Usage,
		Pending:    st.Pending,
	}
}

func (l *Loop) finishCanceled(ctx context.Context, st *run.State) *run.Result {
	if n := len(st.Messages); n > 0 {
		last := st.Messages[n-1]
		uses := message.ToolUses(last)
		if len(uses) > 0 && last.Role == message.RoleAssistant {
			results := make([]message.ToolResult, len(uses))
			for i, u := range uses {
				results[i] = message.ToolResult{
					ToolUseID: u.ID,
					OK:        false,
					Parts:     []message.Part{message.Text{Text: "canceled"}},
				}
			}
			_ = l.appendMessage(ctx, st, message.NewToolResultMessage(results...))
		}
	}
	return stop(st, run.StopCanceled)
}
