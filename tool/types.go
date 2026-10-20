package tool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sven-victor/ez-agent/message"
)

// ErrEndOfChat ends the agent loop after this tool's side effects have run.
// Observers still receive ToolFinished. The batch is not written into history
// and the model is not called again. Return it from the handler; optionally
// set Result.Err to the same value. Detect it with errors.Is.
var ErrEndOfChat = errors.New("end of chat")

// Spec describes a tool for the model.
type Spec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Choice controls tool selection for model calls.
type Choice struct {
	Type string // auto|none|required|named
	Name string // when Type == named
}

// Call is a single tool invocation request.
type Call struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Result is the outcome of a tool call.
// Handler business failure should set OK=false; framework errors are normalized the same way by the Executor.
// ErrEndOfChat is not a business failure: OK and Content are kept, and a zero Result becomes OK=true.
type Result struct {
	ToolUseID string
	OK        bool
	Content   []message.Part
	Err       error
}

// Handler executes a tool call.
type Handler func(ctx context.Context, call Call) (Result, error)

// Registration pairs a Spec with its Handler.
type Registration struct {
	Spec    Spec
	Handler Handler
}

// Registry looks up tools by name.
type Registry interface {
	Specs() []Spec
	Lookup(name string) (Handler, bool)
}

// Executor runs a batch of tool calls.
type Executor interface {
	Execute(ctx context.Context, calls []Call) ([]Result, error)
}

// CallHooks is an optional hook sink for the executor (avoids tool↔hook import cycles).
type CallHooks interface {
	BeforeTool(ctx context.Context, call Call) (BeforeAction, error)
	AfterTool(ctx context.Context, call Call, result Result) (Result, error)
}

// BeforeAction is the writable outcome of BeforeTool.
type BeforeAction struct {
	Skip         bool
	Deny         bool
	Reason       string
	ReplaceInput json.RawMessage
}
