package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/tool"
)

func builtinTools() []tool.Registration {
	return []tool.Registration{
		currentTimeTool(),
		formatTimeTool(),
		randomStringTool(),
	}
}

func currentTimeTool() tool.Registration {
	type input struct {
		Timezone string `json:"timezone"`
	}
	schema := `{
		"type": "object",
		"properties": {
			"timezone": {
				"type": "string",
				"description": "IANA timezone (e.g. Asia/Shanghai, UTC). Defaults to local."
			}
		},
		"additionalProperties": false
	}`
	return tool.Registration{
		Spec: tool.Spec{
			Name:        "current_time",
			Description: "Get the current date and time, optionally in a specific timezone.",
			InputSchema: json.RawMessage(schema),
		},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var in input
			if len(call.Input) > 0 {
				if err := json.Unmarshal(call.Input, &in); err != nil {
					return toolFail(call.ID, "invalid input: "+err.Error()), nil
				}
			}
			loc := time.Local
			if in.Timezone != "" {
				var err error
				loc, err = time.LoadLocation(in.Timezone)
				if err != nil {
					return toolFail(call.ID, "unknown timezone: "+err.Error()), nil
				}
			}
			now := time.Now().In(loc)
			text := fmt.Sprintf("%s (%s)", now.Format(time.RFC3339), loc.String())
			return toolOK(call.ID, text), nil
		},
	}
}

func formatTimeTool() tool.Registration {
	type input struct {
		Time     string `json:"time"`
		Layout   string `json:"layout"`
		Timezone string `json:"timezone"`
	}
	schema := `{
		"type": "object",
		"properties": {
			"time": {
				"type": "string",
				"description": "Input time as RFC3339, or empty for now"
			},
			"layout": {
				"type": "string",
				"description": "Go time layout (e.g. 2006-01-02 15:04:05). Defaults to RFC3339."
			},
			"timezone": {
				"type": "string",
				"description": "IANA timezone to convert into before formatting"
			}
		},
		"additionalProperties": false
	}`
	return tool.Registration{
		Spec: tool.Spec{
			Name:        "format_time",
			Description: "Parse a time (or use now) and format it with a Go layout, optionally converting timezone.",
			InputSchema: json.RawMessage(schema),
		},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var in input
			if len(call.Input) > 0 {
				if err := json.Unmarshal(call.Input, &in); err != nil {
					return toolFail(call.ID, "invalid input: "+err.Error()), nil
				}
			}
			var t time.Time
			if strings.TrimSpace(in.Time) == "" {
				t = time.Now()
			} else {
				parsed, err := time.Parse(time.RFC3339, in.Time)
				if err != nil {
					parsed, err = time.Parse(time.RFC3339Nano, in.Time)
				}
				if err != nil {
					return toolFail(call.ID, "parse time (want RFC3339): "+err.Error()), nil
				}
				t = parsed
			}
			if in.Timezone != "" {
				loc, err := time.LoadLocation(in.Timezone)
				if err != nil {
					return toolFail(call.ID, "unknown timezone: "+err.Error()), nil
				}
				t = t.In(loc)
			}
			layout := in.Layout
			if layout == "" {
				layout = time.RFC3339
			}
			return toolOK(call.ID, t.Format(layout)), nil
		},
	}
}

func randomStringTool() tool.Registration {
	type input struct {
		Length  int    `json:"length"`
		Charset string `json:"charset"`
	}
	schema := `{
		"type": "object",
		"properties": {
			"length": {
				"type": "integer",
				"description": "Length of the string (1-256). Defaults to 16.",
				"minimum": 1,
				"maximum": 256
			},
			"charset": {
				"type": "string",
				"description": "Character set: alnum (default), alpha, digit, hex, or a custom string of allowed characters"
			}
		},
		"additionalProperties": false
	}`
	return tool.Registration{
		Spec: tool.Spec{
			Name:        "random_string",
			Description: "Generate a cryptographically random string.",
			InputSchema: json.RawMessage(schema),
		},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var in input
			if len(call.Input) > 0 {
				if err := json.Unmarshal(call.Input, &in); err != nil {
					return toolFail(call.ID, "invalid input: "+err.Error()), nil
				}
			}
			n := in.Length
			if n == 0 {
				n = 16
			}
			if n < 1 || n > 256 {
				return toolFail(call.ID, "length must be between 1 and 256"), nil
			}
			chars, err := resolveCharset(in.Charset)
			if err != nil {
				return toolFail(call.ID, err.Error()), nil
			}
			out := make([]byte, n)
			max := big.NewInt(int64(len(chars)))
			for i := 0; i < n; i++ {
				v, err := rand.Int(rand.Reader, max)
				if err != nil {
					return toolFail(call.ID, "rng: "+err.Error()), nil
				}
				out[i] = chars[v.Int64()]
			}
			return toolOK(call.ID, string(out)), nil
		},
	}
}

func resolveCharset(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "alnum":
		return "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", nil
	case "alpha":
		return "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", nil
	case "digit", "digits":
		return "0123456789", nil
	case "hex":
		return "0123456789abcdef", nil
	default:
		if len(name) < 2 {
			return "", fmt.Errorf("charset must have at least 2 characters")
		}
		return name, nil
	}
}

func toolOK(id, text string) tool.Result {
	return tool.Result{
		ToolUseID: id,
		OK:        true,
		Content:   []message.Part{message.Text{Text: text}},
	}
}

func toolFail(id, text string) tool.Result {
	return tool.Result{
		ToolUseID: id,
		OK:        false,
		Content:   []message.Part{message.Text{Text: text}},
	}
}
