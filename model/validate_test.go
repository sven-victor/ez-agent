package model_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
)

type stubProvider struct {
	caps model.Capabilities
}

func (s stubProvider) Name() string             { return "stub" }
func (s stubProvider) Caps() model.Capabilities { return s.caps }
func (s stubProvider) Stream(context.Context, model.Request, func(event.Event) error) (model.Result, error) {
	return model.Result{}, nil
}
func (s stubProvider) Complete(context.Context, model.Request) (model.Result, error) {
	return model.Result{}, nil
}

func TestValidateRejectsUnsupportedAudio(t *testing.T) {
	p := stubProvider{caps: model.Capabilities{TextIn: true, ImageIn: true, Tools: true}}
	req := model.Request{
		Messages: []message.Message{
			message.NewUserParts(
				message.Text{Text: "hi"},
				message.Audio{Blob: message.Blob{MIME: "audio/wav", Bytes: []byte("x")}},
			),
		},
	}
	err := model.Validate(p, req)
	if !errors.Is(err, model.ErrUnsupportedPart) {
		t.Fatalf("expected ErrUnsupportedPart, got %v", err)
	}
}

func TestValidateAcceptsTextAndImage(t *testing.T) {
	p := stubProvider{caps: model.Capabilities{TextIn: true, ImageIn: true}}
	req := model.Request{
		Messages: []message.Message{
			message.NewUserParts(
				message.Text{Text: "see"},
				message.Image{Blob: message.Blob{MIME: "image/png", Bytes: []byte("png")}},
			),
		},
	}
	if err := model.Validate(p, req); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOpaqueRequiresCap(t *testing.T) {
	p := stubProvider{caps: model.Capabilities{TextIn: true}}
	req := model.Request{
		Messages: []message.Message{{
			Role:  message.RoleAssistant,
			Parts: []message.Part{message.Opaque{Vendor: "x", Data: []byte(`{}`)}},
		}},
	}
	if err := model.Validate(p, req); !errors.Is(err, model.ErrUnsupportedPart) {
		t.Fatalf("expected unsupported, got %v", err)
	}
	p.caps.Opaque = true
	if err := model.Validate(p, req); err != nil {
		t.Fatal(err)
	}
}

func TestValidateVideo(t *testing.T) {
	p := stubProvider{caps: model.Capabilities{TextIn: true, VideoIn: true}}
	req := model.Request{
		Messages: []message.Message{
			message.NewUserParts(message.Video{Blob: message.Blob{MIME: "video/mp4", Bytes: []byte("vid")}}),
		},
	}
	if err := model.Validate(p, req); err != nil {
		t.Fatal(err)
	}
}
