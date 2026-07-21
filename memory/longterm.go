package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// LongTerm is cross-session durable memory (facts), orthogonal to ContextPolicy / Session.
type LongTerm interface {
	Search(ctx context.Context, query string, k int) ([]Item, error)
	Add(ctx context.Context, items ...Item) error
}

// Item is one long-term memory entry.
type Item struct {
	ID      string
	Content string
	Meta    map[string]string
}

// MemLongTerm is an in-memory LongTerm for tests and simple apps.
type MemLongTerm struct {
	mu    sync.RWMutex
	items []Item
	seq   int
}

// NewMemLongTerm creates an empty MemLongTerm.
func NewMemLongTerm() *MemLongTerm {
	return &MemLongTerm{}
}

func (m *MemLongTerm) Add(_ context.Context, items ...Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range items {
		if it.ID == "" {
			m.seq++
			it.ID = fmt.Sprintf("mem_%d", m.seq)
		}
		if it.Meta == nil {
			it.Meta = map[string]string{}
		}
		// Replace same ID if present.
		replaced := false
		for i, existing := range m.items {
			if existing.ID == it.ID {
				m.items[i] = it
				replaced = true
				break
			}
		}
		if !replaced {
			m.items = append(m.items, it)
		}
	}
	return nil
}

func (m *MemLongTerm) Search(_ context.Context, query string, k int) ([]Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if k <= 0 {
		k = 5
	}
	q := strings.ToLower(strings.TrimSpace(query))
	tokens := searchTokens(q)
	var scored []Item
	for _, it := range m.items {
		hay := strings.ToLower(it.Content)
		for _, v := range it.Meta {
			hay += " " + strings.ToLower(v)
		}
		if q == "" || strings.Contains(hay, q) || tokensMatch(hay, tokens) {
			scored = append(scored, it)
		}
	}
	if len(scored) > k {
		scored = scored[:k]
	}
	out := make([]Item, len(scored))
	copy(out, scored)
	return out, nil
}

func searchTokens(q string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(q, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '-')
	}) {
		if len(t) >= 3 {
			out = append(out, t)
		}
	}
	return out
}

func tokensMatch(hay string, tokens []string) bool {
	for _, t := range tokens {
		if strings.Contains(hay, t) {
			return true
		}
	}
	return false
}

var _ LongTerm = (*MemLongTerm)(nil)
