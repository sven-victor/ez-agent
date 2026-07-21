package memory

import (
	"context"
	"fmt"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
)

// DegradeMode controls unsupported media handling.
type DegradeMode int

const (
	// DegradeFail leaves parts unchanged so model.Validate returns ErrUnsupportedPart.
	DegradeFail DegradeMode = iota
	// DegradePlaceholder replaces unsupported media Parts with explanatory Text.
	DegradePlaceholder
)

// DegradeMedia rewrites Messages so unsupported media matches provider Caps.
// Compose before other policies: DegradeMedia{Caps: p.Caps(), Mode: DegradePlaceholder, Inner: window}.
type DegradeMedia struct {
	Caps  model.Capabilities
	Mode  DegradeMode
	Inner ContextPolicy
}

func (d *DegradeMedia) Prepare(ctx context.Context, st *run.State) error {
	if d.Mode == DegradePlaceholder {
		for i := range st.Messages {
			st.Messages[i].Parts = degradeParts(st.Messages[i].Parts, d.Caps)
		}
	}
	if d.Inner != nil {
		return d.Inner.Prepare(ctx, st)
	}
	return nil
}

func (d *DegradeMedia) Reduce(ctx context.Context, st *run.State) error {
	if d.Mode == DegradePlaceholder {
		for i := range st.Messages {
			st.Messages[i].Parts = degradeParts(st.Messages[i].Parts, d.Caps)
		}
	}
	if d.Inner != nil {
		return d.Inner.Reduce(ctx, st)
	}
	if d.Mode == DegradeFail {
		return fmt.Errorf("degrade media: no inner policy to reduce")
	}
	before := len(st.Messages)
	if before == 0 {
		return fmt.Errorf("degrade media: no messages to reduce")
	}
	st.Messages = dropOldestGroup(st.Messages)
	if len(st.Messages) >= before {
		return fmt.Errorf("degrade media: failed to reduce context")
	}
	return nil
}

func degradeParts(parts []message.Part, caps model.Capabilities) []message.Part {
	out := make([]message.Part, 0, len(parts))
	for _, p := range parts {
		switch v := p.(type) {
		case message.Image:
			if caps.ImageIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("image", v.Blob)})
			}
		case message.Audio:
			if caps.AudioIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("audio", v.Blob)})
			}
		case message.Video:
			if caps.VideoIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("video", v.Blob)})
			}
		case message.Document:
			if caps.DocumentIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("document", v.Blob)})
			}
		case message.AudioOut:
			if caps.AudioOut {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("audio_out", v.Blob)})
			}
		case message.ToolResult:
			nested := degradeParts(v.Parts, caps)
			out = append(out, message.ToolResult{ToolUseID: v.ToolUseID, OK: v.OK, Parts: nested})
		default:
			out = append(out, p)
		}
	}
	return out
}

func placeholder(kind string, b message.Blob) string {
	name := b.Filename
	if name == "" {
		name = b.MIME
	}
	if name == "" {
		name = kind
	}
	return fmt.Sprintf("[%s omitted: %s]", kind, name)
}

var _ ContextPolicy = (*DegradeMedia)(nil)
