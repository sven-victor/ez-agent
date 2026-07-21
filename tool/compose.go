package tool

// Compose merges multiple registries into one MapRegistry (later names win on conflict).
func Compose(regs ...Registry) *MapRegistry {
	out := NewRegistry()
	for _, r := range regs {
		if r == nil {
			continue
		}
		for _, spec := range r.Specs() {
			h, ok := r.Lookup(spec.Name)
			if !ok {
				continue
			}
			_ = out.Register(Registration{Spec: spec, Handler: h})
		}
	}
	return out
}
