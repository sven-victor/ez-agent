package tool

import "sync"

// MapRegistry is an in-memory tool registry.
type MapRegistry struct {
	mu    sync.RWMutex
	order []string
	specs map[string]Spec
	hands map[string]Handler
}

// NewRegistry builds a MapRegistry from registrations.
func NewRegistry(tools ...Registration) *MapRegistry {
	r := &MapRegistry{
		specs: make(map[string]Spec),
		hands: make(map[string]Handler),
	}
	for _, t := range tools {
		_ = r.Register(t)
	}
	return r
}

// Register adds or replaces a tool.
func (r *MapRegistry) Register(reg Registration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.specs[reg.Spec.Name]; !ok {
		r.order = append(r.order, reg.Spec.Name)
	}
	r.specs[reg.Spec.Name] = reg.Spec
	r.hands[reg.Spec.Name] = reg.Handler
	return nil
}

// Specs returns tool specs in registration order.
func (r *MapRegistry) Specs() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.specs[name])
	}
	return out
}

// Lookup returns the handler for name.
func (r *MapRegistry) Lookup(name string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.hands[name]
	return h, ok
}
