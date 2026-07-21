package memory

import (
	"encoding/json"

	"github.com/sven-victor/ez-agent/message"
)

// EstimateTokens returns a coarse token estimate for msgs (≈ chars/4).
// Good enough for proactive thresholds; not a real tokenizer.
func EstimateTokens(msgs []message.Message) int {
	n := 0
	for _, m := range msgs {
		n += estimateMessageTokens(m)
	}
	return n
}

func estimateMessageTokens(m message.Message) int {
	n := 4 // role overhead
	for _, p := range m.Parts {
		n += (estimatePartBytes(p) + 3) / 4
	}
	return n
}

func estimatePartBytes(p message.Part) int {
	switch v := p.(type) {
	case message.Text:
		return len(v.Text)
	case message.Reasoning:
		return len(v.Text) + len(v.Signature)
	case message.ToolUse:
		return len(v.Name) + len(v.Input) + len(v.ID)
	case message.ToolResult:
		n := len(v.ToolUseID)
		for _, sp := range v.Parts {
			n += estimatePartBytes(sp)
		}
		return n
	case message.Opaque:
		return len(v.Data) + len(v.Vendor)
	case message.Image:
		return blobBytes(v.Blob)
	case message.Audio:
		return blobBytes(v.Blob)
	case message.Video:
		return blobBytes(v.Blob)
	case message.Document:
		return blobBytes(v.Blob)
	case message.AudioOut:
		return blobBytes(v.Blob)
	default:
		b, _ := json.Marshal(p)
		return len(b)
	}
}

func blobBytes(b message.Blob) int {
	if len(b.Bytes) > 0 {
		return len(b.Bytes)
	}
	return len(b.URL) + len(b.Path) + len(b.MIME)
}
