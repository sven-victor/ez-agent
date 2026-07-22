package run

import "github.com/sven-victor/ez-agent/message"

// StopReason describes why a run or model call stopped.
type StopReason string

const (
	StopEndTurn     StopReason = "end_turn"
	StopToolUse     StopReason = "tool_use" // intermediate only; never final Result
	StopMaxTurns    StopReason = "max_turns"
	StopMaxTokens   StopReason = "max_tokens"
	StopCanceled    StopReason = "canceled"
	StopError       StopReason = "error"
	StopInterrupted StopReason = "interrupted" // HITL pause at tool boundary
)

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Add merges other into u.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.TotalTokens += other.TotalTokens
}

// Limits constrain a run.
type Limits struct {
	MaxTurns        int
	MaxTotalTokens  int
	MaxOutputTokens int
}

// Input is the caller-facing run request.
type Input struct {
	Messages []message.Message
	Text     string // sugar → one user text message
}

// PendingTools holds tool calls waiting after an HITL interrupt.
// Resume executes these without calling the model first.
type PendingTools struct {
	Calls []PendingCall
}

// PendingCall is a serializable tool invocation.
type PendingCall struct {
	ID    string
	Name  string
	Input []byte
}

// State is the typed runtime state for a single Run.
//
// Transcript is the in-run conversation log (Result.Messages). Messages is the
// model window; ContextPolicy may trim or summarize Messages only.
// When SessionStore is enabled, it persists Messages (the effective window).
type State struct {
	Transcript []message.Message
	Messages   []message.Message
	Turn       int
	Usage      Usage
	Values     map[string]any
	Pending    *PendingTools // set when StopInterrupted; cleared on Resume
}

// InitHistory seeds both Transcript and Messages from the same slice.
func (st *State) InitHistory(msgs []message.Message) {
	st.Transcript = append([]message.Message(nil), msgs...)
	st.Messages = append([]message.Message(nil), msgs...)
}

// AppendMessage appends to both Transcript and the model window.
func (st *State) AppendMessage(m message.Message) {
	st.Transcript = append(st.Transcript, m)
	st.Messages = append(st.Messages, m)
}

// ReplaceLastMessage replaces the last message in both buffers.
func (st *State) ReplaceLastMessage(m message.Message) {
	if len(st.Messages) > 0 {
		st.Messages[len(st.Messages)-1] = m
	}
	if len(st.Transcript) > 0 {
		st.Transcript[len(st.Transcript)-1] = m
	}
}

// PopLastMessage removes the last message from both buffers.
func (st *State) PopLastMessage() {
	if len(st.Messages) > 0 {
		st.Messages = st.Messages[:len(st.Messages)-1]
	}
	if len(st.Transcript) > 0 {
		st.Transcript = st.Transcript[:len(st.Transcript)-1]
	}
}

// Result is the authoritative outcome of a Run.
type Result struct {
	Messages   []message.Message
	StopReason StopReason
	Usage      Usage
	Structured []byte        // JSON when OutputSchema was requested
	Pending    *PendingTools // non-nil when interrupted awaiting Resume
}

// MessagesFromInput expands Input into messages to append.
func MessagesFromInput(in Input) []message.Message {
	var msgs []message.Message
	if in.Text != "" {
		msgs = append(msgs, message.NewUserText(in.Text))
	}
	msgs = append(msgs, in.Messages...)
	return msgs
}

// StateFromResult rebuilds a State suitable for Agent.Resume after an interrupt.
func StateFromResult(res *Result) *State {
	if res == nil {
		return &State{Values: map[string]any{}}
	}
	st := &State{
		Usage:   res.Usage,
		Pending: res.Pending,
		Values:  map[string]any{},
	}
	st.InitHistory(res.Messages)
	return st
}
