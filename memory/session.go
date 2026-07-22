package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sven-victor/ez-agent/message"
)

// Session is a persisted conversation window (effective model messages).
type Session struct {
	ID       string
	Messages []message.Message
	Values   map[string]any
}

// SessionStore loads and saves sessions.
//
// Messages are the effective model window (post-summarize / trim), not a
// forever-growing transcript. Append grows that window; ReplaceAll replaces it
// after context reduction (summarize / window trim).
//
// Each message should carry a stable Message.ID (assigned on Append when empty)
// so SQL adapters can map one message to one row. ReplaceAll after summarize
// typically introduces a new summary ID while recent messages keep theirs.
type SessionStore interface {
	Load(ctx context.Context, id string) (*Session, error)
	Save(ctx context.Context, s *Session) error
	Append(ctx context.Context, id string, msgs ...message.Message) error
	ReplaceAll(ctx context.Context, id string, msgs []message.Message) error
}

// MemStore is an in-memory SessionStore for tests.
type MemStore struct {
	mu   sync.RWMutex
	data map[string]*Session
}

// NewMemStore creates an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{data: make(map[string]*Session)}
}

func (m *MemStore) Load(_ context.Context, id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.data[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return cloneSession(s), nil
}

func (m *MemStore) Save(_ context.Context, s *Session) error {
	if s == nil || s.ID == "" {
		return fmt.Errorf("session: id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := cloneSession(s)
	message.EnsureIDs(cp.Messages)
	m.data[s.ID] = cp
	return nil
}

func (m *MemStore) Append(_ context.Context, id string, msgs ...message.Message) error {
	if id == "" {
		return fmt.Errorf("session: id is required")
	}
	if len(msgs) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.data[id]
	if s == nil {
		s = &Session{ID: id, Values: map[string]any{}}
		m.data[id] = s
	}
	s.Messages = append(s.Messages, cloneMessagesWithIDs(msgs)...)
	return nil
}

func (m *MemStore) ReplaceAll(_ context.Context, id string, msgs []message.Message) error {
	if id == "" {
		return fmt.Errorf("session: id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.data[id]
	if s == nil {
		s = &Session{ID: id, Values: map[string]any{}}
		m.data[id] = s
	}
	s.Messages = cloneMessagesWithIDs(msgs)
	return nil
}

func cloneSession(s *Session) *Session {
	cp := *s
	cp.Messages = append([]message.Message(nil), s.Messages...)
	if s.Values != nil {
		cp.Values = make(map[string]any, len(s.Values))
		for k, v := range s.Values {
			cp.Values[k] = v
		}
	}
	return &cp
}

func cloneMessagesWithIDs(msgs []message.Message) []message.Message {
	out := append([]message.Message(nil), msgs...)
	message.EnsureIDs(out)
	return out
}

// FileStore persists sessions as JSON under Dir.
type FileStore struct {
	Dir string
}

func (f *FileStore) path(id string) string {
	return filepath.Join(f.Dir, id+".json")
}

func (f *FileStore) Load(_ context.Context, id string) (*Session, error) {
	b, err := os.ReadFile(f.path(id))
	if err != nil {
		return nil, err
	}
	var wire sessionWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	msgs, err := decodeMessages(wire.Messages)
	if err != nil {
		return nil, err
	}
	return &Session{ID: wire.ID, Messages: msgs, Values: wire.Values}, nil
}

func (f *FileStore) Save(ctx context.Context, s *Session) error {
	if s == nil || s.ID == "" {
		return fmt.Errorf("session: id is required")
	}
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return err
	}
	msgs := cloneMessagesWithIDs(s.Messages)
	blobDir := filepath.Join(f.Dir, s.ID+"_blobs")
	encoded, err := encodeMessagesForStore(msgs, blobDir)
	if err != nil {
		return err
	}
	wire := sessionWire{ID: s.ID, Messages: encoded, Values: s.Values}
	b, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.path(s.ID), b, 0o644)
}

func (f *FileStore) Append(ctx context.Context, id string, msgs ...message.Message) error {
	if id == "" {
		return fmt.Errorf("session: id is required")
	}
	if len(msgs) == 0 {
		return nil
	}
	s, err := f.loadOrEmpty(ctx, id)
	if err != nil {
		return err
	}
	s.Messages = append(s.Messages, cloneMessagesWithIDs(msgs)...)
	return f.Save(ctx, s)
}

func (f *FileStore) ReplaceAll(ctx context.Context, id string, msgs []message.Message) error {
	if id == "" {
		return fmt.Errorf("session: id is required")
	}
	s, err := f.loadOrEmpty(ctx, id)
	if err != nil {
		return err
	}
	s.Messages = cloneMessagesWithIDs(msgs)
	return f.Save(ctx, s)
}

func (f *FileStore) loadOrEmpty(ctx context.Context, id string) (*Session, error) {
	s, err := f.Load(ctx, id)
	if err == nil {
		return s, nil
	}
	if os.IsNotExist(err) || strings.Contains(err.Error(), "not found") {
		return &Session{ID: id, Values: map[string]any{}}, nil
	}
	return nil, err
}

type sessionWire struct {
	ID       string         `json:"id"`
	Messages []wireMessage  `json:"messages"`
	Values   map[string]any `json:"values,omitempty"`
}

var (
	_ SessionStore = (*MemStore)(nil)
	_ SessionStore = (*FileStore)(nil)
)
