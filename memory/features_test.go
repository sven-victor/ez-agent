package memory_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
)

type fakeSumProvider struct{}

func (fakeSumProvider) Name() string { return "fake-sum" }
func (fakeSumProvider) Caps() model.Capabilities {
	return model.Capabilities{TextIn: true, Streaming: true}
}
func (fakeSumProvider) Stream(ctx context.Context, req model.Request, yield func(event.Event) error) (model.Result, error) {
	return model.Result{
		Message:    message.NewAssistantText("summary of prior turns"),
		StopReason: run.StopEndTurn,
	}, nil
}
func (fakeSumProvider) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	return fakeSumProvider{}.Stream(ctx, req, nil)
}

func assertNoDangling(t *testing.T, msgs []message.Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch v := p.(type) {
			case message.ToolUse:
				pending[v.ID] = true
			case message.ToolResult:
				delete(pending, v.ToolUseID)
			}
		}
	}
	if len(pending) > 0 {
		t.Fatalf("dangling: %v", pending)
	}
}

func TestSummarizeReduceReplacesOldest(t *testing.T) {
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)

	pol := &memory.Summarize{
		Summarizer: &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		Ratio:      0.3,
		KeepRecent: 10,
	}
	before := len(st.Messages)
	if err := pol.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) >= before {
		t.Fatalf("expected shrink: before=%d after=%d", before, len(st.Messages))
	}
	text := message.TextContent(st.Messages[0])
	if !strings.Contains(text, "summary") {
		t.Fatalf("expected summary message, got %q", text)
	}
	if st.Messages[0].Role != message.RoleUser {
		t.Fatalf("summary role=%s", st.Messages[0].Role)
	}
}

func TestSummarizePreservesToolPairs(t *testing.T) {
	st := &run.State{}
	st.InitHistory([]message.Message{
		message.NewUserText("u0"),
		{
			Role: message.RoleAssistant,
			Parts: []message.Part{
				message.ToolUse{ID: "t1", Name: "echo", Input: []byte(`{}`)},
			},
		},
		message.NewToolResultMessage(message.ToolResult{
			ToolUseID: "t1", OK: true, Parts: []message.Part{message.Text{Text: "r"}},
		}),
		message.NewUserText("u1"),
		message.NewUserText("u2"),
		message.NewUserText("u3"),
		message.NewUserText("u4"),
		message.NewUserText("u5"),
		message.NewUserText("u6"),
		message.NewUserText("u7"),
		message.NewUserText("u8"),
		message.NewUserText("u9"),
		message.NewUserText("u10"),
	})
	pol := &memory.Summarize{
		Summarizer: &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		Ratio:      0.5,
		KeepRecent: 3,
	}
	if err := pol.Reduce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	assertNoDangling(t, st.Messages)
}

func TestSummarizeProactivePrepare(t *testing.T) {
	st := &run.State{}
	var msgs []message.Message
	for i := 0; i < 15; i++ {
		msgs = append(msgs, message.NewUserText("m"))
	}
	st.InitHistory(msgs)
	before := len(st.Messages)
	pol := &memory.Summarize{
		Summarizer:           &memory.ProviderSummarizer{Provider: fakeSumProvider{}},
		KeepRecent:           5,
		ProactiveMaxMessages: 10,
	}
	if err := pol.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) >= before {
		t.Fatalf("expected proactive shrink: %d -> %d", before, len(st.Messages))
	}
	if !strings.Contains(message.TextContent(st.Messages[0]), "summary") {
		t.Fatalf("got %q", message.TextContent(st.Messages[0]))
	}
}

func TestSessionMemRoundTrip(t *testing.T) {
	store := memory.NewMemStore()
	s := &memory.Session{
		ID: "s1",
		Messages: []message.Message{
			message.NewUserText("hello"),
			message.NewAssistantText("hi"),
		},
		Values: map[string]any{"k": "v"},
	}
	if err := store.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || message.TextContent(got.Messages[0]) != "hello" {
		t.Fatalf("got %#v", got.Messages)
	}
	if got.Values["k"] != "v" {
		t.Fatalf("values=%v", got.Values)
	}
}

func TestSessionFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := &memory.FileStore{Dir: dir}
	s := &memory.Session{
		ID: "file1",
		Messages: []message.Message{
			message.NewUserParts(
				message.Text{Text: "see"},
				message.Image{Blob: message.Blob{MIME: "image/png", Bytes: []byte("pngdata")}},
			),
		},
	}
	if err := store.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), "file1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("msgs=%d", len(got.Messages))
	}
	img, ok := got.Messages[0].Parts[1].(message.Image)
	if !ok {
		t.Fatalf("part=%T", got.Messages[0].Parts[1])
	}
	if img.Blob.Path == "" && len(img.Blob.Bytes) == 0 {
		t.Fatal("expected path or bytes for image")
	}
}

func TestOffloadReplacesLargeBytes(t *testing.T) {
	store := memory.NewMemBlobStore()
	pol := &memory.Offload{Store: store, MaxBytes: 10, Inner: memory.Noop{}}
	big := make([]byte, 100)
	st := &run.State{}
	st.InitHistory([]message.Message{
		message.NewUserParts(message.Image{Blob: message.Blob{MIME: "image/png", Bytes: big}}),
	})
	if err := pol.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	img := st.Messages[0].Parts[0].(message.Image)
	if len(img.Blob.Bytes) != 0 {
		t.Fatalf("bytes should be cleared, got %d", len(img.Blob.Bytes))
	}
	if !strings.HasPrefix(img.Blob.URL, "mem://") {
		t.Fatalf("url=%q", img.Blob.URL)
	}
}

func TestDegradeMediaPlaceholder(t *testing.T) {
	pol := &memory.DegradeMedia{
		Caps: model.Capabilities{TextIn: true}, // no ImageIn
		Mode: memory.DegradePlaceholder,
	}
	st := &run.State{}
	st.InitHistory([]message.Message{
		message.NewUserParts(
			message.Text{Text: "see"},
			message.Image{Blob: message.Blob{MIME: "image/png", Bytes: []byte("x"), Filename: "shot.png"}},
		),
	})
	if err := pol.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(st.Messages[0].Parts) != 2 {
		t.Fatalf("parts=%d", len(st.Messages[0].Parts))
	}
	if _, ok := st.Messages[0].Parts[1].(message.Image); ok {
		t.Fatal("image should be degraded")
	}
	text := message.TextContent(message.Message{Parts: st.Messages[0].Parts[1:]})
	if !strings.Contains(text, "image omitted") {
		t.Fatalf("text=%q", text)
	}
}

func TestMemLongTerm(t *testing.T) {
	ctx := context.Background()
	lt := memory.NewMemLongTerm()
	if err := lt.Add(ctx, memory.Item{Content: "Alice likes tea", Meta: map[string]string{"who": "alice"}}); err != nil {
		t.Fatal(err)
	}
	items, err := lt.Search(ctx, "tea", 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("search: %v %#v", err, items)
	}
	items, err = lt.Search(ctx, "alice", 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("meta search: %v %#v", err, items)
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := &run.State{Turn: 2, Usage: run.Usage{TotalTokens: 9}, Values: map[string]any{"k": "v"}}
	st.InitHistory([]message.Message{message.NewUserText("hi")})
	st.Pending = &run.PendingTools{Calls: []run.PendingCall{{ID: "1", Name: "echo", Input: []byte(`{}`)}}}

	mem := memory.NewMemCheckpointStore()
	if err := mem.Save(ctx, "r1", st); err != nil {
		t.Fatal(err)
	}
	got, err := mem.Load(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Turn != 2 || got.Usage.TotalTokens != 9 || got.Values["k"] != "v" {
		t.Fatalf("mem load mismatch: %#v", got)
	}
	if got.Pending == nil || len(got.Pending.Calls) != 1 || got.Pending.Calls[0].Name != "echo" {
		t.Fatalf("pending: %#v", got.Pending)
	}

	dir := t.TempDir()
	fs := &memory.FileCheckpointStore{Dir: dir}
	if err := fs.Save(ctx, "r1", st); err != nil {
		t.Fatal(err)
	}
	got2, err := fs.Load(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.Messages) != 1 || message.TextContent(got2.Messages[0]) != "hi" {
		t.Fatalf("file msgs: %#v", got2.Messages)
	}
	if _, err := fs.Load(ctx, "missing"); err == nil {
		t.Fatal("expected missing error")
	}
	_ = filepath.Join(dir, "r1.ckpt.json")
}

func TestOpaqueSerialize(t *testing.T) {
	ctx := context.Background()
	st := &run.State{}
	st.InitHistory([]message.Message{{
		Role: message.RoleAssistant,
		Parts: []message.Part{
			message.Opaque{Vendor: "x", Data: []byte(`{"a":1}`)},
		},
	}})
	fs := &memory.FileCheckpointStore{Dir: t.TempDir()}
	if err := fs.Save(ctx, "o", st); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Load(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	op, ok := got.Messages[0].Parts[0].(message.Opaque)
	if !ok || op.Vendor != "x" {
		t.Fatalf("opaque: %#v", got.Messages[0].Parts)
	}
	var obj map[string]any
	if err := json.Unmarshal(op.Data, &obj); err != nil || obj["a"] != float64(1) {
		t.Fatalf("opaque data: %s err=%v", op.Data, err)
	}
}
