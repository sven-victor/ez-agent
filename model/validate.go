package model

import (
	"errors"
	"fmt"

	"github.com/sven-victor/ez-agent/message"
)

var (
	// ErrUnsupportedPart is returned when a message part is not supported by Caps.
	ErrUnsupportedPart = errors.New("unsupported part")
	// ErrOverflow indicates the provider reported a context length overflow.
	ErrOverflow = errors.New("context overflow")
)

// Validate checks that every part in req is supported by p.Caps().
func Validate(p Provider, req Request) error {
	caps := p.Caps()
	if err := validateParts(caps, req.System.Parts, p.Name(), "system"); err != nil {
		return err
	}
	if len(req.System.Parts) > 0 && !caps.SystemParts {
		return fmt.Errorf("%w: system parts on provider %s", ErrUnsupportedPart, p.Name())
	}
	for i, m := range req.Messages {
		if err := validateParts(caps, m.Parts, p.Name(), fmt.Sprintf("messages[%d]", i)); err != nil {
			return err
		}
	}
	if len(req.Tools) > 0 && !caps.Tools {
		return fmt.Errorf("%w: tools on provider %s", ErrUnsupportedPart, p.Name())
	}
	if len(req.OutputSchema) > 0 && !caps.JSONSchema {
		return fmt.Errorf("%w: json schema on provider %s", ErrUnsupportedPart, p.Name())
	}
	return nil
}

func validateParts(caps Capabilities, parts []message.Part, provider, where string) error {
	for _, part := range parts {
		if !supported(caps, part) {
			return fmt.Errorf("%w: %T in %s on provider %s", ErrUnsupportedPart, part, where, provider)
		}
		if img, ok := part.(message.Image); ok {
			if err := img.Blob.Validate(); err != nil {
				return fmt.Errorf("invalid image blob in %s: %w", where, err)
			}
		}
		if aud, ok := part.(message.Audio); ok {
			if err := aud.Blob.Validate(); err != nil {
				return fmt.Errorf("invalid audio blob in %s: %w", where, err)
			}
		}
		if doc, ok := part.(message.Document); ok {
			if err := doc.Blob.Validate(); err != nil {
				return fmt.Errorf("invalid document blob in %s: %w", where, err)
			}
		}
		if vid, ok := part.(message.Video); ok {
			if err := vid.Blob.Validate(); err != nil {
				return fmt.Errorf("invalid video blob in %s: %w", where, err)
			}
		}
		if ao, ok := part.(message.AudioOut); ok {
			if err := ao.Blob.Validate(); err != nil {
				return fmt.Errorf("invalid audio_out blob in %s: %w", where, err)
			}
		}
		if tr, ok := part.(message.ToolResult); ok {
			if err := validateParts(caps, tr.Parts, provider, where+".tool_result"); err != nil {
				return err
			}
		}
	}
	return nil
}

func supported(caps Capabilities, part message.Part) bool {
	switch part.(type) {
	case message.Text:
		return caps.TextIn
	case message.Image:
		return caps.ImageIn
	case message.Audio:
		return caps.AudioIn
	case message.Video:
		return caps.VideoIn
	case message.Document:
		return caps.DocumentIn
	case message.AudioOut:
		return caps.AudioOut
	case message.Opaque:
		return caps.Opaque
	case message.ToolUse, message.ToolResult:
		return caps.Tools
	case message.Reasoning:
		return true
	default:
		return false
	}
}
