package plugin

import (
	"fmt"

	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/tool"
)

// Plugin packs hooks and tools installed at Agent construction.
type Plugin interface {
	Name() string
	Install(h *Host) error
}

// Host is the install surface for plugins.
type Host struct {
	Hooks *hook.Registry
	Tools *tool.MapRegistry
}

// NewHost creates a Host with the given registries (nil creates new ones).
func NewHost(hooks *hook.Registry, tools *tool.MapRegistry) *Host {
	if hooks == nil {
		hooks = hook.NewRegistry()
	}
	if tools == nil {
		tools = tool.NewRegistry()
	}
	return &Host{Hooks: hooks, Tools: tools}
}

// RegisterTool registers a tool on the host.
func (h *Host) RegisterTool(reg tool.Registration) {
	_ = h.Tools.Register(reg)
}

// OnBeforeModel registers a before-model hook.
func (h *Host) OnBeforeModel(fn hook.Handler[hook.BeforeModel]) {
	h.Hooks.OnBeforeModel(fn)
}

// OnAfterModel registers an after-model hook.
func (h *Host) OnAfterModel(fn hook.Handler[hook.AfterModel]) {
	h.Hooks.OnAfterModel(fn)
}

// OnBeforeTool registers a before-tool hook.
func (h *Host) OnBeforeTool(fn hook.Handler[hook.BeforeTool]) {
	h.Hooks.OnBeforeTool(fn)
}

// OnAfterTool registers an after-tool hook.
func (h *Host) OnAfterTool(fn hook.Handler[hook.AfterTool]) {
	h.Hooks.OnAfterTool(fn)
}

// OnBeforeRun registers a before-run hook.
func (h *Host) OnBeforeRun(fn hook.Handler[hook.BeforeRun]) {
	h.Hooks.OnBeforeRun(fn)
}

// OnAfterRun registers an after-run hook.
func (h *Host) OnAfterRun(fn hook.Handler[hook.AfterRun]) {
	h.Hooks.OnAfterRun(fn)
}

// InstallAll installs plugins; on failure returns an error and does not leave a partial host
// for callers that discard the host (Agent.New builds fresh then installs).
func InstallAll(h *Host, plugins []Plugin) error {
	for _, p := range plugins {
		if p == nil {
			continue
		}
		if err := p.Install(h); err != nil {
			return fmt.Errorf("plugin %q: %w", p.Name(), err)
		}
	}
	return nil
}
