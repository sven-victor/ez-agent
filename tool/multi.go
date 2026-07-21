package tool

// Multi is a live-delegating registry over several backends.
// Specs/Lookup always query the underlying registries (no snapshot),
// so MCP RefreshTools is visible to the agent without re-Compose.
// On name conflicts, later registries win.
type Multi struct {
	regs []Registry
}

// NewMulti builds a Multi. Nil entries are ignored.
func NewMulti(regs ...Registry) *Multi {
	m := &Multi{}
	for _, r := range regs {
		if r != nil {
			m.regs = append(m.regs, r)
		}
	}
	return m
}

// Specs merges specs from all registries; later names override earlier ones
// while preserving first-seen order for unique names.
func (m *Multi) Specs() []Spec {
	if m == nil || len(m.regs) == 0 {
		return nil
	}
	order := make([]string, 0)
	byName := make(map[string]Spec)
	for _, r := range m.regs {
		for _, spec := range r.Specs() {
			if _, ok := byName[spec.Name]; !ok {
				order = append(order, spec.Name)
			}
			byName[spec.Name] = spec
		}
	}
	out := make([]Spec, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}

// Lookup searches registries in reverse order so later registrations win.
func (m *Multi) Lookup(name string) (Handler, bool) {
	if m == nil {
		return nil, false
	}
	for i := len(m.regs) - 1; i >= 0; i-- {
		if h, ok := m.regs[i].Lookup(name); ok {
			return h, true
		}
	}
	return nil, false
}

var _ Registry = (*Multi)(nil)
