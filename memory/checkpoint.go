package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

// CheckpointStore persists run.State snapshots for resume across processes.
type CheckpointStore interface {
	Save(ctx context.Context, id string, st *run.State) error
	Load(ctx context.Context, id string) (*run.State, error)
}

// MemCheckpointStore is an in-memory CheckpointStore.
type MemCheckpointStore struct {
	mu   sync.RWMutex
	data map[string]*run.State
}

func NewMemCheckpointStore() *MemCheckpointStore {
	return &MemCheckpointStore{data: make(map[string]*run.State)}
}

func (s *MemCheckpointStore) Save(_ context.Context, id string, st *run.State) error {
	if id == "" || st == nil {
		return fmt.Errorf("checkpoint: id and state required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := cloneState(st)
	s.data[id] = cp
	return nil
}

func (s *MemCheckpointStore) Load(_ context.Context, id string) (*run.State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("checkpoint %q not found", id)
	}
	return cloneState(st), nil
}

// FileCheckpointStore persists checkpoints as JSON under Dir.
type FileCheckpointStore struct {
	Dir string
}

func (s *FileCheckpointStore) path(id string) string {
	return filepath.Join(s.Dir, id+".ckpt.json")
}

func (s *FileCheckpointStore) Save(_ context.Context, id string, st *run.State) error {
	if id == "" || st == nil {
		return fmt.Errorf("checkpoint: id and state required")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	wire, err := encodeCheckpoint(st)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(id), b, 0o644)
}

func (s *FileCheckpointStore) Load(_ context.Context, id string) (*run.State, error) {
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	var wire checkpointWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	return decodeCheckpoint(wire)
}

type checkpointWire struct {
	SavedAt    time.Time         `json:"saved_at"`
	Turn       int               `json:"turn"`
	Usage      run.Usage         `json:"usage"`
	Values     map[string]any    `json:"values,omitempty"`
	Pending    *run.PendingTools `json:"pending,omitempty"`
	Transcript []wireMessage     `json:"transcript"`
	Messages   []wireMessage     `json:"messages"`
}

func encodeCheckpoint(st *run.State) (checkpointWire, error) {
	tr, err := encodeMessagesForStore(st.Transcript, "")
	if err != nil {
		return checkpointWire{}, err
	}
	msgs, err := encodeMessagesForStore(st.Messages, "")
	if err != nil {
		return checkpointWire{}, err
	}
	return checkpointWire{
		SavedAt:    time.Now().UTC(),
		Turn:       st.Turn,
		Usage:      st.Usage,
		Values:     st.Values,
		Pending:    st.Pending,
		Transcript: tr,
		Messages:   msgs,
	}, nil
}

func decodeCheckpoint(w checkpointWire) (*run.State, error) {
	tr, err := decodeMessages(w.Transcript)
	if err != nil {
		return nil, err
	}
	msgs, err := decodeMessages(w.Messages)
	if err != nil {
		return nil, err
	}
	st := &run.State{
		Transcript: tr,
		Messages:   msgs,
		Turn:       w.Turn,
		Usage:      w.Usage,
		Values:     w.Values,
		Pending:    w.Pending,
	}
	if st.Values == nil {
		st.Values = map[string]any{}
	}
	return st, nil
}

func cloneState(st *run.State) *run.State {
	cp := &run.State{
		Turn:  st.Turn,
		Usage: st.Usage,
	}
	cp.Transcript = append([]message.Message(nil), st.Transcript...)
	cp.Messages = append([]message.Message(nil), st.Messages...)
	if st.Values != nil {
		cp.Values = make(map[string]any, len(st.Values))
		for k, v := range st.Values {
			cp.Values[k] = v
		}
	} else {
		cp.Values = map[string]any{}
	}
	if st.Pending != nil {
		p := *st.Pending
		p.Calls = append([]run.PendingCall(nil), st.Pending.Calls...)
		cp.Pending = &p
	}
	return cp
}

var (
	_ CheckpointStore = (*MemCheckpointStore)(nil)
	_ CheckpointStore = (*FileCheckpointStore)(nil)
)
