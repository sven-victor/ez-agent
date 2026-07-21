package message

import (
	"encoding/json"
	"time"
)

// Role identifies the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single turn entry in the conversation.
type Message struct {
	Role  Role
	Parts []Part
}

// Part is a closed set of content kinds. Implement part() to satisfy the interface.
type Part interface {
	part()
}

// Text is plain text content.
type Text struct {
	Text string
}

func (Text) part() {}

// Image is an image or screenshot payload.
type Image struct {
	Blob   Blob
	Detail string // optional: low|high|auto
}

func (Image) part() {}

// Audio is audio input (P1+; defined for Validate to reject cleanly).
type Audio struct {
	Blob     Blob
	Duration time.Duration // optional; ignored by adapters that do not need it
}

func (Audio) part() {}

// Video is video input (P1+).
type Video struct {
	Blob     Blob
	Duration time.Duration // optional
}

func (Video) part() {}

// Document is a document such as PDF (P1+).
type Document struct {
	Blob Blob
}

func (Document) part() {}

// AudioOut is model-produced audio (P2).
type AudioOut struct {
	Blob Blob
}

func (AudioOut) part() {}

// ToolUse is a model request to invoke a tool.
type ToolUse struct {
	ID    string
	Name  string
	Input []byte // JSON object
}

func (ToolUse) part() {}

// ToolResult is the result of a tool invocation.
type ToolResult struct {
	ToolUseID string
	OK        bool
	Parts     []Part
}

func (ToolResult) part() {}

// Reasoning is optional model reasoning content.
type Reasoning struct {
	Text      string
	Signature string
}

func (Reasoning) part() {}

// Opaque is a vendor passthrough escape hatch (P2).
// Default Validate rejects it unless Caps.Opaque; adapters may refuse encode.
type Opaque struct {
	Vendor string
	Data   json.RawMessage
}

func (Opaque) part() {}
