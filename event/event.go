package event

import (
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

// Event is a typed run-stream event. Hook events live in package hook.
type Event interface {
	event()
}

// RunStarted is emitted when a run begins.
type RunStarted struct{}

func (RunStarted) event() {}

// RunFinished is emitted with the authoritative result.
type RunFinished struct {
	Result *run.Result
}

func (RunFinished) event() {}

// RunError carries a fatal run error; the stream closes after this.
type RunError struct {
	Err error
}

func (RunError) event() {}

// ModelDelta is an incremental model output chunk.
type ModelDelta struct {
	TextDelta  string
	AudioChunk []byte
	MIME       string
}

func (ModelDelta) event() {}

// ModelFinished is the completed model message for a cycle.
type ModelFinished struct {
	Message    message.Message
	StopReason run.StopReason
	Usage      run.Usage
}

func (ModelFinished) event() {}

// ToolStarted is emitted when a tool call begins.
type ToolStarted struct {
	Call tool.Call
}

func (ToolStarted) event() {}

// ToolFinished is emitted when a tool call completes.
type ToolFinished struct {
	Call   tool.Call
	Result tool.Result
}

func (ToolFinished) event() {}

// Cancelled is emitted when the run is canceled via context.
type Cancelled struct{}

func (Cancelled) event() {}

// Interrupted is emitted when HITL pauses before tool execution.
type Interrupted struct {
	Pending *run.PendingTools
}

func (Interrupted) event() {}
