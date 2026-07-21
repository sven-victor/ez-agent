package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

const defaultOffloadMaxBytes = 32 * 1024

// BlobStore stores binary payloads and returns a retrievable URL or path.
type BlobStore interface {
	Put(ctx context.Context, blob message.Blob) (ref string, err error)
}

// MemBlobStore keeps blobs in memory and returns mem:// refs.
type MemBlobStore struct {
	mu    sync.Mutex
	seq   atomic.Uint64
	blobs map[string]message.Blob
}

// NewMemBlobStore creates an empty in-memory blob store.
func NewMemBlobStore() *MemBlobStore {
	return &MemBlobStore{blobs: make(map[string]message.Blob)}
}

func (s *MemBlobStore) Put(_ context.Context, blob message.Blob) (string, error) {
	id := fmt.Sprintf("mem://%d", s.seq.Add(1))
	s.mu.Lock()
	s.blobs[id] = blob
	s.mu.Unlock()
	return id, nil
}

// Get returns a previously stored blob.
func (s *MemBlobStore) Get(ref string) (message.Blob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blobs[ref]
	return b, ok
}

// FileBlobStore writes blobs under Dir and returns file:// paths.
type FileBlobStore struct {
	Dir string
	seq atomic.Uint64
}

func (s *FileBlobStore) Put(_ context.Context, blob message.Blob) (string, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", err
	}
	name := blob.Filename
	if name == "" {
		name = fmt.Sprintf("blob_%d", s.seq.Add(1))
	} else {
		name = fmt.Sprintf("%d_%s", s.seq.Add(1), filepath.Base(name))
	}
	path := filepath.Join(s.Dir, name)
	data := blob.Bytes
	if len(data) == 0 && blob.Path != "" {
		var err error
		data, err = os.ReadFile(blob.Path)
		if err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file://" + path, nil
}

// Offload replaces oversized inline Bytes with BlobStore refs before Inner runs.
type Offload struct {
	Inner    ContextPolicy
	Store    BlobStore
	MaxBytes int // default 32KiB
}

func (o *Offload) maxBytes() int {
	if o.MaxBytes <= 0 {
		return defaultOffloadMaxBytes
	}
	return o.MaxBytes
}

func (o *Offload) Prepare(ctx context.Context, st *run.State) error {
	if err := o.offloadMessages(ctx, st); err != nil {
		return err
	}
	if o.Inner != nil {
		return o.Inner.Prepare(ctx, st)
	}
	return nil
}

func (o *Offload) Reduce(ctx context.Context, st *run.State) error {
	before := len(st.Messages)
	if err := o.offloadMessages(ctx, st); err != nil {
		return err
	}
	if o.Inner != nil {
		if err := o.Inner.Reduce(ctx, st); err != nil {
			if len(st.Messages) >= before {
				return err
			}
			return nil
		}
		return nil
	}
	// Without Inner, force a drop so Reduce always makes progress.
	if len(st.Messages) == 0 {
		return fmt.Errorf("offload reduce: no messages to reduce")
	}
	st.Messages = dropOldestGroup(st.Messages)
	if len(st.Messages) >= before {
		return fmt.Errorf("offload reduce: failed to reduce context")
	}
	return nil
}

func (o *Offload) offloadMessages(ctx context.Context, st *run.State) error {
	if o.Store == nil {
		return nil
	}
	max := o.maxBytes()
	for i := range st.Messages {
		parts, err := o.offloadParts(ctx, st.Messages[i].Parts, max)
		if err != nil {
			return err
		}
		st.Messages[i].Parts = parts
	}
	return nil
}

func (o *Offload) offloadParts(ctx context.Context, parts []message.Part, max int) ([]message.Part, error) {
	out := make([]message.Part, len(parts))
	for i, p := range parts {
		switch v := p.(type) {
		case message.Image:
			b, err := o.maybeOffload(ctx, v.Blob, max)
			if err != nil {
				return nil, err
			}
			out[i] = message.Image{Blob: b, Detail: v.Detail}
		case message.Audio:
			b, err := o.maybeOffload(ctx, v.Blob, max)
			if err != nil {
				return nil, err
			}
			out[i] = message.Audio{Blob: b}
		case message.Video:
			b, err := o.maybeOffload(ctx, v.Blob, max)
			if err != nil {
				return nil, err
			}
			out[i] = message.Video{Blob: b}
		case message.Document:
			b, err := o.maybeOffload(ctx, v.Blob, max)
			if err != nil {
				return nil, err
			}
			out[i] = message.Document{Blob: b}
		case message.ToolResult:
			nested, err := o.offloadParts(ctx, v.Parts, max)
			if err != nil {
				return nil, err
			}
			out[i] = message.ToolResult{ToolUseID: v.ToolUseID, OK: v.OK, Parts: nested}
		default:
			out[i] = p
		}
	}
	return out, nil
}

func (o *Offload) maybeOffload(ctx context.Context, b message.Blob, max int) (message.Blob, error) {
	if len(b.Bytes) <= max {
		return b, nil
	}
	ref, err := o.Store.Put(ctx, b)
	if err != nil {
		return message.Blob{}, err
	}
	return message.Blob{
		MIME:     b.MIME,
		URL:      ref,
		Filename: b.Filename,
		Size:     int64(len(b.Bytes)),
	}, nil
}

var (
	_ ContextPolicy = (*Offload)(nil)
	_ BlobStore     = (*MemBlobStore)(nil)
	_ BlobStore     = (*FileBlobStore)(nil)
)
