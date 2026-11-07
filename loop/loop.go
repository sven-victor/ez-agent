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
	"github.com/sven-victor/ez-agent/logger"
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
	Provider        model.Provider
	Tools           tool.Registry
	Executor        tool.Executor
	Hooks           *hook.Registry
	Policy          memory.ContextPolicy
	Limits          run.Limits
	System          model.System
	MaxOverflow     int
	MaxModelRetry   int
	OutputSchema    []byte
	OutputName      string
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
	// Logger receives framework diagnostics; nil becomes a no-op.
	Logger logger.Logger
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
	cfg.Logger = logger.OrNop(cfg.Logger)
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
	log := l.cfg.Logger

	log.Info("run started", "turn", st.Turn, "messages", len(st.Messages), "pending", st.Pending != nil)
	_ = emit(event.RunStarted{})

	if err := l.cfg.Hooks.FireBeforeRun(ctx, &hook.BeforeRun{State: st}); err != nil {
		log.Error("before-run hook failed", "err", err)
		_ = emit(event.RunError{Err: err})
		return nil, err
	}

	defer func() {
		after := &hook.AfterRun{State: st, Result: res}
		_ = l.cfg.Hooks.FireAfterRun(context.WithoutCancel(ctx), after)
		if res != nil {
			log.Info("run finished",
				"stop_reason", res.StopReason,
				"turns", st.Turn,
				"input_tokens", res.Usage.InputTokens,
				"output_tokens", res.Usage.OutputTokens,
				"total_tokens", res.Usage.TotalTokens,
				"cache_read_tokens", res.Usage.CacheReadTokens,
				"cache_write_tokens", res.Usage.CacheWriteTokens,
			)
			l.saveCheckpoint(context.WithoutCancel(ctx), st)
		} else if err != nil {
			log.Error("run failed", "err", err, "turn", st.Turn)
		}
	}()

	modelRetries := 0
	overflowRetries := 0
	providerRetries := 0

	for {
		// HITL resume: run pending tools first, then continue to the model.
		if st.Pending != nil && len(st.Pending.Calls) > 0 {
			log.Info("resuming pending tools", "count", len(st.Pending.Calls), "turn", st.Turn)
			ended, err := l.executePending(ctx, st, emit)
			if err != nil {
				_ = emit(event.RunError{Err: err})
				return nil, err
			}
			st.Pending = nil
			if ended {
				res = l.finishEndOfChat(st, emit)
				return res, nil
			}
			st.Turn++
			continue
		}

		if reason := checkLimits(st, l.cfg.Limits); reason != "" {
			log.Info("run stopped by limit", "reason", reason, "turn", st.Turn)
			res = stop(st, reason)
			_ = emit(event.RunFinished{Result: res})
			return res, nil
		}
		select {
		case <-ctx.Done():
			log.Info("run canceled", "turn", st.Turn, "err", ctx.Err())
			res = l.finishCanceled(ctx, st)
			_ = emit(event.Cancelled{})
			_ = emit(event.RunFinished{Result: res})
			return res, ctx.Err()
		default:
		}

		beforePrepare := append([]message.Message(nil), st.Messages...)
		if err := l.cfg.Policy.Prepare(ctx, st); err != nil {
			log.Error("context prepare failed", "err", err)
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		if !sameMessages(beforePrepare, st.Messages) {
			log.Debug("context prepared", "before", len(beforePrepare), "after", len(st.Messages))
		}
		if err := l.syncSessionWindow(ctx, beforePrepare, st.Messages); err != nil {
			log.Error("session sync failed", "err", err)
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
		if err := l.cfg.Hooks.FireBeforeModel(ctx, &hook.BeforeModel{State: st, Request: req}); err != nil {
			log.Error("before-model hook failed", "err", err)
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		if err := model.Validate(l.cfg.Provider, req); err != nil {
			log.Error("model request invalid", "err", err)
			_ = emit(event.RunError{Err: err})
			return nil, err
		}

		providerName := ""
		if l.cfg.Provider != nil {
			providerName = l.cfg.Provider.Name()
		}
		log.Debug("model call",
			"provider", providerName,
			"turn", st.Turn,
			"messages", len(req.Messages),
			"tools", len(req.Tools),
			"complete", l.cfg.PreferComplete,
		)

		// fireAfterModel always runs after a provider attempt (success or failure).
		// On failure, Retry/ReplaceMessage from the hook are ignored by callers.
		fireAfterModel := func(result model.Result, callErr error) (*hook.AfterModel, error) {
			after := &hook.AfterModel{State: st, Result: result, Err: callErr}
			if herr := l.cfg.Hooks.FireAfterModel(ctx, after); herr != nil {
				log.Error("after-model hook failed", "err", herr)
				_ = emit(event.RunError{Err: herr})
				return after, herr
			}
			return after, nil
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
			if _, herr := fireAfterModel(mres, err); herr != nil {
				return nil, herr
			}
			if errors.Is(err, model.ErrOverflow) {
				overflowRetries++
				log.Warn("context overflow", "attempt", overflowRetries, "max", l.cfg.MaxOverflow, "err", err)
				if overflowRetries > l.cfg.MaxOverflow {
					log.Error("overflow retries exceeded", "attempts", overflowRetries, "err", err)
					_ = emit(event.RunError{Err: fmt.Errorf("overflow retries exceeded: %w", err)})
					return nil, err
				}
				beforeReduce := append([]message.Message(nil), st.Messages...)
				if rerr := l.cfg.Policy.Reduce(ctx, st); rerr != nil {
					log.Error("context reduce failed", "err", rerr)
					_ = emit(event.RunError{Err: rerr})
					return nil, rerr
				}
				log.Info("context reduced after overflow", "before", len(beforeReduce), "after", len(st.Messages))
				if serr := l.syncSessionWindow(ctx, beforeReduce, st.Messages); serr != nil {
					log.Error("session sync failed", "err", serr)
					_ = emit(event.RunError{Err: serr})
					return nil, serr
				}
				continue
			}
			if l.cfg.RetryPolicy != nil {
				providerRetries++
				if wait, ok := l.cfg.RetryPolicy.ShouldRetry(providerRetries, err); ok {
					log.Warn("provider retry scheduled", "attempt", providerRetries, "wait", wait, "err", err)
					select {
					case <-ctx.Done():
						log.Info("run canceled during provider retry", "err", ctx.Err())
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
				log.Info("run canceled", "turn", st.Turn, "err", ctx.Err())
				res = l.finishCanceled(ctx, st)
				_ = emit(event.Cancelled{})
				_ = emit(event.RunFinished{Result: res})
				return res, ctx.Err()
			default:
			}
			log.Error("model call failed", "provider", providerName, "err", err)
			_ = emit(event.RunError{Err: err})
			return nil, err
		}
		overflowRetries = 0
		providerRetries = 0

		log.Info("model finished",
			"turn", st.Turn,
			"stop_reason", mres.StopReason,
			"input_tokens", mres.Usage.InputTokens,
			"output_tokens", mres.Usage.OutputTokens,
			"cache_read_tokens", mres.Usage.CacheReadTokens,
			"cache_write_tokens", mres.Usage.CacheWriteTokens,
		)

		msg := sanitizeIncompleteToolUse(mres, log)
		if len(l.cfg.OutputSchema) > 0 && msg.StopReason != run.StopToolUse {
			structured, verr := validateStructuredJSON(msg.Message)
			if verr != nil {
				if _, herr := fireAfterModel(msg, verr); herr != nil {
					return nil, herr
				}
				modelRetries++
				log.Warn("structured output invalid", "attempt", modelRetries, "max", l.cfg.MaxModelRetry, "err", verr)
				if modelRetries > l.cfg.MaxModelRetry {
					err := fmt.Errorf("structured output invalid: %w", verr)
					log.Error("structured output retries exceeded", "err", err)
					_ = emit(event.RunError{Err: err})
					return nil, err
				}
				continue
			}
			if aerr := l.appendMessage(ctx, st, msg.Message); aerr != nil {
				if _, herr := fireAfterModel(msg, aerr); herr != nil {
					return nil, herr
				}
				log.Error("append message failed", "err", aerr)
				_ = emit(event.RunError{Err: aerr})
				return nil, aerr
			}
			st.Usage.Add(msg.Usage)
			if _, herr := fireAfterModel(msg, nil); herr != nil {
				return nil, herr
			}
			res = stop(st, msg.StopReason)
			res.Structured = structured
			_ = emit(event.RunFinished{Result: res})
			return res, nil
		}

		if aerr := l.appendMessage(ctx, st, msg.Message); aerr != nil {
			if _, herr := fireAfterModel(msg, aerr); herr != nil {
				return nil, herr
			}
			log.Error("append message failed", "err", aerr)
			_ = emit(event.RunError{Err: aerr})
			return nil, aerr
		}
		st.Usage.Add(msg.Usage)

		after, herr := fireAfterModel(msg, nil)
		if herr != nil {
			return nil, herr
		}
		if after.ReplaceMessage != nil {
			rm := *after.ReplaceMessage
			if rm.ID == "" && len(st.Messages) > 0 {
				rm.ID = st.Messages[len(st.Messages)-1].ID
			}
			message.EnsureID(&rm)
			st.ReplaceLastMessage(rm)
			msg.Message = rm
			log.Debug("after-model replaced message")
			if serr := l.sessionReplaceAll(ctx, st.Messages); serr != nil {
				log.Error("session replace failed", "err", serr)
				_ = emit(event.RunError{Err: serr})
				return nil, serr
			}
		}
		if after.Retry {
			modelRetries++
			log.Warn("after-model retry", "attempt", modelRetries, "max", l.cfg.MaxModelRetry)
			if modelRetries > l.cfg.MaxModelRetry {
				err := fmt.Errorf("after-model retry limit exceeded")
				log.Error("after-model retries exceeded", "err", err)
				_ = emit(event.RunError{Err: err})
				return nil, err
			}
			if l.cfg.RetryPolicy != nil {
				if wait, ok := l.cfg.RetryPolicy.ShouldRetry(modelRetries, fmt.Errorf("after-model retry")); ok {
					select {
					case <-ctx.Done():
						log.Info("run canceled during after-model retry", "err", ctx.Err())
						res = l.finishCanceled(ctx, st)
						_ = emit(event.Cancelled{})
						_ = emit(event.RunFinished{Result: res})
						return res, ctx.Err()
					case <-time.After(wait):
					}
				}
			}
			st.PopLastMessage()
			if serr := l.sessionReplaceAll(ctx, st.Messages); serr != nil {
				log.Error("session replace failed", "err", serr)
				_ = emit(event.RunError{Err: serr})
				return nil, serr
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
			log.Debug("tool_use stop with no calls; ending turn")
			res = stop(st, run.StopEndTurn)
			_ = emit(event.RunFinished{Result: res})
			return res, nil
		}

		if l.cfg.Interrupter != nil {
			interrupt, ierr := l.cfg.Interrupter.BeforeTools(ctx, st, calls)
			if ierr != nil {
				log.Error("interrupter failed", "err", ierr)
				_ = emit(event.RunError{Err: ierr})
				return nil, ierr
			}
			if interrupt {
				pending := pendingFromCalls(calls)
				st.Pending = pending
				log.Info("run interrupted before tools", "tools", len(calls), "turn", st.Turn)
				res = stop(st, run.StopInterrupted)
				res.Pending = pending
				_ = emit(event.Interrupted{Pending: pending})
				_ = emit(event.RunFinished{Result: res})
				return res, nil
			}
		}

		ended, err := l.runTools(ctx, st, emit, calls)
		if err != nil {
			return nil, err
		}
		if ended {
			res = l.finishEndOfChat(st, emit)
			return res, nil
		}
		st.Turn++
		l.saveCheckpoint(ctx, st)
	}
}

func (l *Loop) saveCheckpoint(ctx context.Context, st *run.State) {
	if l.cfg.CheckpointStore == nil || l.cfg.CheckpointID == "" || st == nil {
		return
	}
	if err := l.cfg.CheckpointStore.Save(ctx, l.cfg.CheckpointID, st); err != nil {
		l.cfg.Logger.Warn("checkpoint save failed", "checkpoint_id", l.cfg.CheckpointID, "err", err)
		return
	}
	l.cfg.Logger.Debug("checkpoint saved", "checkpoint_id", l.cfg.CheckpointID, "turn", st.Turn)
}

func (l *Loop) appendMessage(ctx context.Context, st *run.State, m message.Message) error {
	message.EnsureID(&m)
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
	message.EnsureIDs(msgs)
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

func (l *Loop) executePending(ctx context.Context, st *run.State, emit EmitFunc) (bool, error) {
	calls := make([]tool.Call, len(st.Pending.Calls))
	for i, c := range st.Pending.Calls {
		calls[i] = tool.Call{ID: c.ID, Name: c.Name, Input: c.Input}
	}
	return l.runTools(ctx, st, emit, calls)
}

func (l *Loop) runTools(ctx context.Context, st *run.State, emit EmitFunc, calls []tool.Call) (bool, error) {
	log := l.cfg.Logger
	exec := l.executor(st)
	for _, call := range calls {
		log.Info("tool started", "tool", call.Name, "tool_use_id", call.ID, "turn", st.Turn)
		_ = emit(event.ToolStarted{Call: call})
	}
	start := time.Now()
	results, err := exec.Execute(ctx, calls)
	elapsed := time.Since(start)
	// End-of-chat is a result sentinel, not an Execute error. The batch has
	// already run; do not append results or surface RunError.
	if len(results) == len(calls) && hasEndOfChat(results) {
		for i, call := range calls {
			log.Info("tool finished",
				"tool", call.Name,
				"tool_use_id", call.ID,
				"ok", results[i].OK,
				"duration", elapsed,
			)
			_ = emit(event.ToolFinished{Call: call, Result: results[i]})
		}
		if derr := l.dropToolUses(ctx, st); derr != nil {
			log.Error("drop tool uses failed", "err", derr)
			_ = emit(event.RunError{Err: derr})
			return false, derr
		}
		return true, nil
	}
	if err != nil {
		log.Error("tool execution failed", "count", len(calls), "duration", elapsed, "err", err)
		if len(results) == len(calls) {
			for i, call := range calls {
				log.Info("tool finished", "tool", call.Name, "tool_use_id", call.ID, "ok", results[i].OK)
				_ = emit(event.ToolFinished{Call: call, Result: results[i]})
			}
			if aerr := l.appendMessage(ctx, st, message.NewToolResultMessage(tool.ToMessageParts(results)...)); aerr != nil {
				log.Error("append tool results failed", "err", aerr)
				_ = emit(event.RunError{Err: aerr})
				return false, aerr
			}
		} else {
			if aerr := l.appendMessage(ctx, st, syntheticErrorResults(calls, err)); aerr != nil {
				log.Error("append synthetic tool errors failed", "err", aerr)
				_ = emit(event.RunError{Err: aerr})
				return false, aerr
			}
		}
		select {
		case <-ctx.Done():
			log.Info("run canceled during tools", "err", ctx.Err())
			_ = emit(event.Cancelled{})
			_ = emit(event.RunFinished{Result: stop(st, run.StopCanceled)})
			return false, ctx.Err()
		default:
		}
		_ = emit(event.RunError{Err: err})
		return false, err
	}
	for i, call := range calls {
		log.Info("tool finished",
			"tool", call.Name,
			"tool_use_id", call.ID,
			"ok", results[i].OK,
			"duration", elapsed,
		)
		_ = emit(event.ToolFinished{Call: call, Result: results[i]})
	}
	return false, l.appendMessage(ctx, st, message.NewToolResultMessage(tool.ToMessageParts(results)...))
}

func hasEndOfChat(results []tool.Result) bool {
	for _, r := range results {
		if errors.Is(r.Err, tool.ErrEndOfChat) {
			return true
		}
	}
	return false
}

// dropToolUses removes every ToolUse from the last assistant message.
// An assistant message that becomes empty is popped. The session window is
// replaced so a tool_use already appended to the store does not linger.
func (l *Loop) dropToolUses(ctx context.Context, st *run.State) error {
	if st != nil && len(st.Messages) > 0 {
		last := st.Messages[len(st.Messages)-1]
		if last.Role == message.RoleAssistant {
			kept := make([]message.Part, 0, len(last.Parts))
			for _, p := range last.Parts {
				if _, ok := p.(message.ToolUse); ok {
					continue
				}
				kept = append(kept, p)
			}
			if len(kept) == 0 {
				st.PopLastMessage()
			} else {
				last.Parts = kept
				st.ReplaceLastMessage(last)
			}
		}
	}
	msgs := []message.Message(nil)
	if st != nil {
		msgs = st.Messages
	}
	return l.sessionReplaceAll(ctx, msgs)
}

func (l *Loop) finishEndOfChat(st *run.State, emit EmitFunc) *run.Result {
	st.Pending = nil
	l.cfg.Logger.Info("run stopped by end of chat", "turn", st.Turn)
	res := stop(st, run.StopEndOfChat)
	_ = emit(event.RunFinished{Result: res})
	return res
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

func sanitizeIncompleteToolUse(mres model.Result, log logger.Logger) model.Result {
	log = logger.OrNop(log)
	// max_tokens: never execute tool calls (args may look valid yet be truncated mid-stream).
	forceStripAll := mres.StopReason == run.StopMaxTokens
	var parts []message.Part
	stripped := false
	strippedCount := 0
	for _, p := range mres.Message.Parts {
		tu, ok := p.(message.ToolUse)
		if !ok {
			parts = append(parts, p)
			continue
		}
		if forceStripAll || !completeToolUse(tu) {
			stripped = true
			strippedCount++
			continue
		}
		parts = append(parts, tu)
	}
	if !stripped {
		return mres
	}
	log.Warn("incomplete tool calls omitted",
		"count", strippedCount,
		"force_max_tokens", forceStripAll,
		"stop_reason", mres.StopReason,
	)
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
