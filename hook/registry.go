package hook

import (
	"context"
	"encoding/json"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

// BeforeRun is fired at the start of a run.
type BeforeRun struct {
	State *run.State
}

// AfterRun is fired at the end of a run.
type AfterRun struct {
	State  *run.State
	Result *run.Result
}

// BeforeModel is fired before each model call.
type BeforeModel struct {
	State *run.State
}

// AfterModel is fired after each model call.
type AfterModel struct {
	State          *run.State
	Result         model.Result
	Retry          bool
	ReplaceMessage *message.Message
}

// BeforeTool is fired before each tool call.
type BeforeTool struct {
	State        *run.State
	Call         tool.Call
	Skip         bool
	Deny         bool
	Reason       string
	ReplaceInput json.RawMessage
}

// AfterTool is fired after each tool call.
type AfterTool struct {
	State         *run.State
	Call          tool.Call
	Result        tool.Result
	ReplaceResult *tool.Result
}

// Handler is a typed hook callback.
type Handler[T any] func(ctx context.Context, e *T) error

// Registry holds multi-subscriber hooks. Before fires in registration order; After in reverse.
type Registry struct {
	beforeRun   []Handler[BeforeRun]
	afterRun    []Handler[AfterRun]
	beforeModel []Handler[BeforeModel]
	afterModel  []Handler[AfterModel]
	beforeTool  []Handler[BeforeTool]
	afterTool   []Handler[AfterTool]
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) OnBeforeRun(h Handler[BeforeRun]) { r.beforeRun = append(r.beforeRun, h) }
func (r *Registry) OnAfterRun(h Handler[AfterRun])   { r.afterRun = append(r.afterRun, h) }
func (r *Registry) OnBeforeModel(h Handler[BeforeModel]) {
	r.beforeModel = append(r.beforeModel, h)
}
func (r *Registry) OnAfterModel(h Handler[AfterModel]) { r.afterModel = append(r.afterModel, h) }
func (r *Registry) OnBeforeTool(h Handler[BeforeTool]) { r.beforeTool = append(r.beforeTool, h) }
func (r *Registry) OnAfterTool(h Handler[AfterTool])   { r.afterTool = append(r.afterTool, h) }

func fireForward[T any](ctx context.Context, handlers []Handler[T], e *T) error {
	for _, h := range handlers {
		if err := h(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func fireReverse[T any](ctx context.Context, handlers []Handler[T], e *T) error {
	var first error
	for i := len(handlers) - 1; i >= 0; i-- {
		if err := handlers[i](ctx, e); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *Registry) FireBeforeRun(ctx context.Context, e *BeforeRun) error {
	return fireForward(ctx, r.beforeRun, e)
}
func (r *Registry) FireAfterRun(ctx context.Context, e *AfterRun) error {
	return fireReverse(ctx, r.afterRun, e)
}
func (r *Registry) FireBeforeModel(ctx context.Context, e *BeforeModel) error {
	return fireForward(ctx, r.beforeModel, e)
}
func (r *Registry) FireAfterModel(ctx context.Context, e *AfterModel) error {
	return fireReverse(ctx, r.afterModel, e)
}
func (r *Registry) FireBeforeTool(ctx context.Context, e *BeforeTool) error {
	return fireForward(ctx, r.beforeTool, e)
}
func (r *Registry) FireAfterTool(ctx context.Context, e *AfterTool) error {
	return fireReverse(ctx, r.afterTool, e)
}

// ToolAdapter adapts Registry to tool.CallHooks for a given run state.
type ToolAdapter struct {
	Registry *Registry
	State    *run.State
}

func (a *ToolAdapter) BeforeTool(ctx context.Context, call tool.Call) (tool.BeforeAction, error) {
	if a == nil || a.Registry == nil {
		return tool.BeforeAction{}, nil
	}
	e := &BeforeTool{State: a.State, Call: call}
	if err := a.Registry.FireBeforeTool(ctx, e); err != nil {
		return tool.BeforeAction{}, err
	}
	return tool.BeforeAction{
		Skip:         e.Skip,
		Deny:         e.Deny,
		Reason:       e.Reason,
		ReplaceInput: e.ReplaceInput,
	}, nil
}

func (a *ToolAdapter) AfterTool(ctx context.Context, call tool.Call, result tool.Result) (tool.Result, error) {
	if a == nil || a.Registry == nil {
		return result, nil
	}
	e := &AfterTool{State: a.State, Call: call, Result: result}
	if err := a.Registry.FireAfterTool(ctx, e); err != nil {
		return result, err
	}
	if e.ReplaceResult != nil {
		return *e.ReplaceResult, nil
	}
	return e.Result, nil
}

// HooksBase embeds empty no-op methods for selective override when using a single Hooks interface.
type HooksBase struct{}

func (HooksBase) BeforeRun(context.Context, *BeforeRun) error     { return nil }
func (HooksBase) AfterRun(context.Context, *AfterRun) error       { return nil }
func (HooksBase) BeforeModel(context.Context, *BeforeModel) error { return nil }
func (HooksBase) AfterModel(context.Context, *AfterModel) error   { return nil }
func (HooksBase) BeforeTool(context.Context, *BeforeTool) error   { return nil }
func (HooksBase) AfterTool(context.Context, *AfterTool) error     { return nil }
