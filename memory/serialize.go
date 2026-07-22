package memory

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sven-victor/ez-agent/message"
)

type wireMessage struct {
	ID    string     `json:"id,omitempty"`
	Role  string     `json:"role"`
	Parts []wirePart `json:"parts"`
}

type wirePart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Detail    string          `json:"detail,omitempty"`
	MIME      string          `json:"mime,omitempty"`
	URL       string          `json:"url,omitempty"`
	Path      string          `json:"path,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	Size      int64           `json:"size,omitempty"`
	BytesB64  string          `json:"bytes_b64,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	OK        *bool           `json:"ok,omitempty"`
	Parts     []wirePart      `json:"parts,omitempty"`
	Signature string          `json:"signature,omitempty"`
	DurationNs int64           `json:"duration_ns,omitempty"`
}

func encodeMessagesForStore(msgs []message.Message, blobDir string) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		wm := wireMessage{ID: m.ID, Role: string(m.Role)}
		for _, p := range m.Parts {
			wp, err := encodePart(p, blobDir)
			if err != nil {
				return nil, err
			}
			wm.Parts = append(wm.Parts, wp)
		}
		out = append(out, wm)
	}
	return out, nil
}

func encodePart(p message.Part, blobDir string) (wirePart, error) {
	switch v := p.(type) {
	case message.Text:
		return wirePart{Type: "text", Text: v.Text}, nil
	case message.Image:
		return encodeBlobPart("image", v.Blob, v.Detail, 0, blobDir)
	case message.Audio:
		return encodeBlobPart("audio", v.Blob, "", v.Duration, blobDir)
	case message.Video:
		return encodeBlobPart("video", v.Blob, "", v.Duration, blobDir)
	case message.Document:
		return encodeBlobPart("document", v.Blob, "", 0, blobDir)
	case message.AudioOut:
		return encodeBlobPart("audio_out", v.Blob, "", 0, blobDir)
	case message.ToolUse:
		return wirePart{Type: "tool_use", ID: v.ID, Name: v.Name, Input: v.Input}, nil
	case message.ToolResult:
		ok := v.OK
		wp := wirePart{Type: "tool_result", ToolUseID: v.ToolUseID, OK: &ok}
		for _, nested := range v.Parts {
			np, err := encodePart(nested, blobDir)
			if err != nil {
				return wirePart{}, err
			}
			wp.Parts = append(wp.Parts, np)
		}
		return wp, nil
	case message.Reasoning:
		return wirePart{Type: "reasoning", Text: v.Text, Signature: v.Signature}, nil
	case message.Opaque:
		return wirePart{Type: "opaque", Name: v.Vendor, Input: v.Data}, nil
	default:
		return wirePart{}, fmt.Errorf("encode: unsupported part %T", p)
	}
}

func encodeBlobPart(typ string, b message.Blob, detail string, dur time.Duration, blobDir string) (wirePart, error) {
	wp := wirePart{
		Type:       typ,
		Detail:     detail,
		MIME:       b.MIME,
		URL:        b.URL,
		Path:       b.Path,
		Filename:   b.Filename,
		Size:       b.Size,
		DurationNs: int64(dur),
	}
	if len(b.Bytes) == 0 {
		return wp, nil
	}
	// Prefer offloading bytes to a side file when a blob dir is available.
	if blobDir != "" {
		if err := os.MkdirAll(blobDir, 0o755); err != nil {
			return wirePart{}, err
		}
		name := b.Filename
		if name == "" {
			name = fmt.Sprintf("blob_%d", len(b.Bytes))
		}
		path := filepath.Join(blobDir, name)
		if err := os.WriteFile(path, b.Bytes, 0o644); err != nil {
			return wirePart{}, err
		}
		wp.Path = path
		wp.BytesB64 = ""
		return wp, nil
	}
	wp.BytesB64 = base64.StdEncoding.EncodeToString(b.Bytes)
	return wp, nil
}

func decodeMessages(msgs []wireMessage) ([]message.Message, error) {
	out := make([]message.Message, 0, len(msgs))
	for _, wm := range msgs {
		m := message.Message{ID: wm.ID, Role: message.Role(wm.Role)}
		for _, wp := range wm.Parts {
			p, err := decodePart(wp)
			if err != nil {
				return nil, err
			}
			m.Parts = append(m.Parts, p)
		}
		out = append(out, m)
	}
	return out, nil
}

func decodePart(wp wirePart) (message.Part, error) {
	switch wp.Type {
	case "text":
		return message.Text{Text: wp.Text}, nil
	case "image":
		b, err := decodeBlob(wp)
		if err != nil {
			return nil, err
		}
		return message.Image{Blob: b, Detail: wp.Detail}, nil
	case "audio":
		b, err := decodeBlob(wp)
		if err != nil {
			return nil, err
		}
		return message.Audio{Blob: b, Duration: time.Duration(wp.DurationNs)}, nil
	case "video":
		b, err := decodeBlob(wp)
		if err != nil {
			return nil, err
		}
		return message.Video{Blob: b, Duration: time.Duration(wp.DurationNs)}, nil
	case "document":
		b, err := decodeBlob(wp)
		if err != nil {
			return nil, err
		}
		return message.Document{Blob: b}, nil
	case "audio_out":
		b, err := decodeBlob(wp)
		if err != nil {
			return nil, err
		}
		return message.AudioOut{Blob: b}, nil
	case "opaque":
		return message.Opaque{Vendor: wp.Name, Data: wp.Input}, nil
	case "tool_use":
		return message.ToolUse{ID: wp.ID, Name: wp.Name, Input: wp.Input}, nil
	case "tool_result":
		ok := true
		if wp.OK != nil {
			ok = *wp.OK
		}
		tr := message.ToolResult{ToolUseID: wp.ToolUseID, OK: ok}
		for _, nested := range wp.Parts {
			np, err := decodePart(nested)
			if err != nil {
				return nil, err
			}
			tr.Parts = append(tr.Parts, np)
		}
		return tr, nil
	case "reasoning":
		return message.Reasoning{Text: wp.Text, Signature: wp.Signature}, nil
	default:
		return nil, fmt.Errorf("decode: unknown part type %q", wp.Type)
	}
}

func decodeBlob(wp wirePart) (message.Blob, error) {
	b := message.Blob{
		MIME:     wp.MIME,
		URL:      wp.URL,
		Path:     wp.Path,
		Filename: wp.Filename,
		Size:     wp.Size,
	}
	if wp.BytesB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(wp.BytesB64)
		if err != nil {
			return message.Blob{}, err
		}
		b.Bytes = raw
	}
	return b, nil
}
