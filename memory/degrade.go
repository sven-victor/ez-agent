package memory

import (
	"context"
	"fmt"

	"github.com/sven-victor/ez-agent/logger"
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
	// Logger receives degrade diagnostics; nil is a no-op.
	Logger logger.Logger
}

func (d *DegradeMedia) Prepare(ctx context.Context, st *run.State) error {
	if d.Mode == DegradePlaceholder {
		if n := d.applyPlaceholders(st); n > 0 {
			policyLog(d.Logger).Debug("degraded unsupported media parts", "phase", "prepare", "count", n)
		}
	}
	if d.Inner != nil {
		return d.Inner.Prepare(ctx, st)
	}
	return nil
}

func (d *DegradeMedia) Reduce(ctx context.Context, st *run.State) error {
	if d.Mode == DegradePlaceholder {
		if n := d.applyPlaceholders(st); n > 0 {
			policyLog(d.Logger).Debug("degraded unsupported media parts", "phase", "reduce", "count", n)
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
	policyLog(d.Logger).Info("degrade media dropped oldest group", "before", before, "after", len(st.Messages))
	return nil
}

func (d *DegradeMedia) applyPlaceholders(st *run.State) int {
	n := 0
	for i := range st.Messages {
		var c int
		st.Messages[i].Parts, c = degradePartsCounted(st.Messages[i].Parts, d.Caps)
		n += c
	}
	return n
}

func degradeParts(parts []message.Part, caps model.Capabilities) []message.Part {
	out, _ := degradePartsCounted(parts, caps)
	return out
}

func degradePartsCounted(parts []message.Part, caps model.Capabilities) ([]message.Part, int) {
	out := make([]message.Part, 0, len(parts))
	n := 0
	for _, p := range parts {
		switch v := p.(type) {
		case message.Image:
			if caps.ImageIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("image", v.Blob)})
				n++
			}
		case message.Audio:
			if caps.AudioIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("audio", v.Blob)})
				n++
			}
		case message.Video:
			if caps.VideoIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("video", v.Blob)})
				n++
			}
		case message.Document:
			if caps.DocumentIn {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("document", v.Blob)})
				n++
			}
		case message.AudioOut:
			if caps.AudioOut {
				out = append(out, p)
			} else {
				out = append(out, message.Text{Text: placeholder("audio_out", v.Blob)})
				n++
			}
		case message.ToolResult:
			nested, c := degradePartsCounted(v.Parts, caps)
			out = append(out, message.ToolResult{ToolUseID: v.ToolUseID, OK: v.OK, Parts: nested})
			n += c
		default:
			out = append(out, p)
		}
	}
	return out, n
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
