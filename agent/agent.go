package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/loop"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/plugin"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

// Agent is the public entry point for running the turn loop.
type Agent struct {
	provider        model.Provider
	localTools      *tool.MapRegistry
	extraRegs       []tool.Registry
	tools           tool.Registry // live Multi over localTools + extraRegs
	hooks           *hook.Registry
	policy          memory.ContextPolicy
	limits          run.Limits
	system          model.System
	loop            *loop.Loop
	sessionID       string
	sessionStore    memory.SessionStore
	outputSchema    []byte
	outputName      string
	executor        tool.Executor
	retryPolicy     model.RetryPolicy
	interrupter     loop.Interrupter
	toolChoice      tool.Choice
	checkpointID    string
	checkpointStore memory.CheckpointStore
	preferComplete  bool
}

// Option configures Agent construction.
type Option func(*config) error

type config struct {
	provider     model.Provider
	tools        []tool.Registration
	hooks        *hook.Registry
	plugins      []plugin.Plugin
	policy       memory.ContextPolicy
	limits       run.Limits
	system       model.System
	sessionID    string
	sessionStore memory.SessionStore
	outputSchema []byte
	outputName   string
	registries   []tool.Registry
	executor     tool.Executor
	retryPolicy  model.RetryPolicy
	interrupter     loop.Interrupter
	toolChoice      tool.Choice
	checkpointID    string
	checkpointStore memory.CheckpointStore
	preferComplete  bool
}

// WithProvider sets the model provider (required).
func WithProvider(p model.Provider) Option {
	return func(c *config) error {
		c.provider = p
		return nil
	}
}

// WithTools registers tools.
func WithTools(regs ...tool.Registration) Option {
	return func(c *config) error {
		c.tools = append(c.tools, regs...)
		return nil
	}
}

// WithHooks sets a pre-built hook registry (merged with plugins).
func WithHooks(r *hook.Registry) Option {
	return func(c *config) error {
		c.hooks = r
		return nil
	}
}

// WithPlugins installs plugins during New.
func WithPlugins(plugins ...plugin.Plugin) Option {
	return func(c *config) error {
		c.plugins = append(c.plugins, plugins...)
		return nil
	}
}

// WithSystem sets the system prompt text.
func WithSystem(text string) Option {
	return func(c *config) error {
		c.system.Text = text
		return nil
	}
}

// WithSystemConfig sets the full system prompt (text + optional Parts).
func WithSystemConfig(sys model.System) Option {
	return func(c *config) error {
		c.system = sys
		return nil
	}
}

// WithToolChoice controls tool selection for model calls.
func WithToolChoice(c tool.Choice) Option {
	return func(cfg *config) error {
		cfg.toolChoice = c
		return nil
	}
}

// WithLimits sets run limits.
func WithLimits(lim run.Limits) Option {
	return func(c *config) error {
		c.limits = lim
		return nil
	}
}

// WithContextPolicy sets the context policy.
func WithContextPolicy(p memory.ContextPolicy) Option {
	return func(c *config) error {
		c.policy = p
		return nil
	}
}

// WithSession enables Load before Run and Save after Run for the given session id.
func WithSession(id string, store memory.SessionStore) Option {
	return func(c *config) error {
		if id == "" || store == nil {
			return fmt.Errorf("agent: session id and store are required")
		}
		c.sessionID = id
		c.sessionStore = store
		return nil
	}
}

// WithOutputSchema requests native JSON-schema structured output from the provider.
func WithOutputSchema(schema []byte, name string) Option {
	return func(c *config) error {
		c.outputSchema = schema
		c.outputName = name
		return nil
	}
}

// WithToolRegistry attaches an additional tool.Registry (e.g. MCP) without snapshotting.
// Specs/Lookup are delegated live, so MCP RefreshTools is visible on the next model call.
func WithToolRegistry(reg tool.Registry) Option {
	return func(c *config) error {
		if reg != nil {
			c.registries = append(c.registries, reg)
		}
		return nil
	}
}

// WithExecutor sets the tool executor (default Concurrent).
func WithExecutor(ex tool.Executor) Option {
	return func(c *config) error {
		c.executor = ex
		return nil
	}
}

// WithRetryPolicy sets exponential-backoff (or custom) retries for provider errors.
func WithRetryPolicy(p model.RetryPolicy) Option {
	return func(c *config) error {
		c.retryPolicy = p
		return nil
	}
}

// WithInterrupter enables HITL pause before tool execution.
func WithInterrupter(i loop.Interrupter) Option {
	return func(c *config) error {
		c.interrupter = i
		return nil
	}
}

// WithCheckpoint enables mid-run and end-of-run State snapshots under id.
func WithCheckpoint(id string, store memory.CheckpointStore) Option {
	return func(c *config) error {
		if id == "" || store == nil {
			return fmt.Errorf("agent: checkpoint id and store are required")
		}
		c.checkpointID = id
		c.checkpointStore = store
		return nil
	}
}

// WithPreferComplete makes the turn loop call Provider.Complete instead of Stream.
func WithPreferComplete(v bool) Option {
	return func(c *config) error {
		c.preferComplete = v
		return nil
	}
}

// New constructs an Agent. Plugin install failures abort with no half-installed agent.
func New(opts ...Option) (*Agent, error) {
	cfg := &config{
		hooks:  hook.NewRegistry(),
		policy: memory.Noop{},
	}
	for _, o := range opts {
		if err := o(cfg); err != nil {
			return nil, err
		}
	}
	if cfg.provider == nil {
		return nil, fmt.Errorf("agent: provider is required")
	}
	if cfg.hooks == nil {
		cfg.hooks = hook.NewRegistry()
	}

	local := tool.NewRegistry(cfg.tools...)
	host := plugin.NewHost(cfg.hooks, local)
	if err := plugin.InstallAll(host, cfg.plugins); err != nil {
		return nil, err
	}
	extras := append([]tool.Registry(nil), cfg.registries...)
	tools := tool.NewMulti(append([]tool.Registry{local}, extras...)...)

	a := &Agent{
		provider:        cfg.provider,
		localTools:      local,
		extraRegs:       extras,
		tools:           tools,
		hooks:           cfg.hooks,
		policy:          cfg.policy,
		limits:          cfg.limits,
		system:          cfg.system,
		sessionID:       cfg.sessionID,
		sessionStore:    cfg.sessionStore,
		outputSchema:    cfg.outputSchema,
		outputName:      cfg.outputName,
		executor:        cfg.executor,
		retryPolicy:     cfg.retryPolicy,
		interrupter:     cfg.interrupter,
		toolChoice:      cfg.toolChoice,
		checkpointID:    cfg.checkpointID,
		checkpointStore: cfg.checkpointStore,
		preferComplete:  cfg.preferComplete,
	}
	a.rebuildLoop()
	return a, nil
}

func (a *Agent) rebuildLoop() {
	exec := a.executor
	if exec == nil {
		exec = &tool.Concurrent{Registry: a.tools}
	} else {
		switch e := exec.(type) {
		case *tool.Concurrent:
			exec = &tool.Concurrent{Registry: a.tools, Hooks: e.Hooks}
		case *tool.Sequential:
			exec = &tool.Sequential{Registry: a.tools, Hooks: e.Hooks}
		}
	}
	a.loop = loop.New(loop.Config{
		Provider:        a.provider,
		Tools:           a.tools,
		Executor:        exec,
		Hooks:           a.hooks,
		Policy:          a.policy,
		Limits:          a.limits,
		System:          a.system,
		OutputSchema:    a.outputSchema,
		OutputName:      a.outputName,
		RetryPolicy:     a.retryPolicy,
		Interrupter:     a.interrupter,
		ToolChoice:      a.toolChoice,
		CheckpointStore: a.checkpointStore,
		CheckpointID:    a.checkpointID,
		PreferComplete:  a.preferComplete,
	})
}

// Hooks returns the agent hook registry.
func (a *Agent) Hooks() *hook.Registry { return a.hooks }

// Tools returns the local (mutable) MapRegistry used by plugins and WithTools.
// Extra registries from WithToolRegistry are composed live via ToolsRegistry.
func (a *Agent) Tools() *tool.MapRegistry { return a.localTools }

// ToolsRegistry returns the live composed registry (local + extras).
func (a *Agent) ToolsRegistry() tool.Registry { return a.tools }

// Run executes a non-streaming run and returns the authoritative result.
func (a *Agent) Run(ctx context.Context, in run.Input) (*run.Result, error) {
	st, err := a.beginState(ctx, in)
	if err != nil {
		return nil, err
	}
	res, err := a.loop.Run(ctx, st, nil)
	if saveErr := a.saveSession(ctx, st); saveErr != nil && err == nil {
		return res, saveErr
	}
	return res, err
}

// Resume continues a run that stopped with StopInterrupted, executing pending tools
// then returning to the model loop. st must carry Pending from the interrupted Result.
func (a *Agent) Resume(ctx context.Context, st *run.State) (*run.Result, error) {
	if st == nil || st.Pending == nil || len(st.Pending.Calls) == 0 {
		return nil, fmt.Errorf("agent: nothing to resume")
	}
	res, err := a.loop.Run(ctx, st, nil)
	if saveErr := a.saveSession(ctx, st); saveErr != nil && err == nil {
		return res, saveErr
	}
	return res, err
}

// Continue runs the loop from an existing State without appending new user input.
// Use after LoadCheckpoint or to keep chatting from a prior Result via StateFromResult.
func (a *Agent) Continue(ctx context.Context, st *run.State) (*run.Result, error) {
	if st == nil {
		return nil, fmt.Errorf("agent: state is required")
	}
	if st.Values == nil {
		st.Values = map[string]any{}
	}
	res, err := a.loop.Run(ctx, st, nil)
	if saveErr := a.saveSession(ctx, st); saveErr != nil && err == nil {
		return res, saveErr
	}
	return res, err
}

// LoadCheckpoint loads a previously saved State (requires WithCheckpoint).
func (a *Agent) LoadCheckpoint(ctx context.Context) (*run.State, error) {
	if a.checkpointStore == nil || a.checkpointID == "" {
		return nil, fmt.Errorf("agent: checkpoint not configured")
	}
	return a.checkpointStore.Load(ctx, a.checkpointID)
}

// SaveCheckpoint persists st under the configured checkpoint id.
func (a *Agent) SaveCheckpoint(ctx context.Context, st *run.State) error {
	if a.checkpointStore == nil || a.checkpointID == "" {
		return fmt.Errorf("agent: checkpoint not configured")
	}
	return a.checkpointStore.Save(ctx, a.checkpointID, st)
}

// RunStream runs the loop and pushes typed events. The channel is closed when the run ends.
// Fatal errors are delivered as event.RunError before close.
func (a *Agent) RunStream(ctx context.Context, in run.Input) <-chan event.Event {
	ch := make(chan event.Event, 64)
	go func() {
		defer close(ch)
		st, err := a.beginState(ctx, in)
		if err != nil {
			_ = sendEvent(ctx, ch, event.RunError{Err: err})
			return
		}
		emit := func(ev event.Event) error {
			return sendEvent(ctx, ch, ev)
		}
		_, runErr := a.loop.Run(ctx, st, emit)
		if saveErr := a.saveSession(ctx, st); saveErr != nil && runErr == nil {
			_ = sendEvent(ctx, ch, event.RunError{Err: saveErr})
		}
	}()
	return ch
}

// ResumeStream is the streaming form of Resume.
func (a *Agent) ResumeStream(ctx context.Context, st *run.State) <-chan event.Event {
	ch := make(chan event.Event, 64)
	go func() {
		defer close(ch)
		if st == nil || st.Pending == nil {
			_ = sendEvent(ctx, ch, event.RunError{Err: fmt.Errorf("agent: nothing to resume")})
			return
		}
		emit := func(ev event.Event) error {
			return sendEvent(ctx, ch, ev)
		}
		_, runErr := a.loop.Run(ctx, st, emit)
		if saveErr := a.saveSession(ctx, st); saveErr != nil && runErr == nil {
			_ = sendEvent(ctx, ch, event.RunError{Err: saveErr})
		}
	}()
	return ch
}

func sendEvent(ctx context.Context, ch chan<- event.Event, ev event.Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ch <- ev:
		return nil
	}
}

func (a *Agent) beginState(ctx context.Context, in run.Input) (*run.State, error) {
	st := &run.State{Values: map[string]any{}}
	incoming := run.MessagesFromInput(in)
	if a.sessionStore != nil && a.sessionID != "" {
		sess, err := a.sessionStore.Load(ctx, a.sessionID)
		if err != nil {
			if !os.IsNotExist(err) && !isSessionNotFound(err) {
				return nil, err
			}
		} else {
			st.InitHistory(sess.Messages)
			if sess.Values != nil {
				st.Values = sess.Values
			}
			for _, m := range incoming {
				st.AppendMessage(m)
			}
			return st, nil
		}
	}
	st.InitHistory(incoming)
	return st, nil
}

func isSessionNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

func (a *Agent) saveSession(ctx context.Context, st *run.State) error {
	if a.sessionStore == nil || a.sessionID == "" {
		return nil
	}
	return a.sessionStore.Save(ctx, &memory.Session{
		ID:       a.sessionID,
		Messages: append([]message.Message(nil), st.Transcript...),
		Values:   st.Values,
	})
}
