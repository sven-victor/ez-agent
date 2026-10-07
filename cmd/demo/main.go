// Command demo is an interactive chat agent (Claude Code–style REPL) with
// built-in tools and optional MCP servers.
//
// Configuration via .env:
//
//	API_KEY=sk-...
//	OPENAI_BASE_URL=https://api.openai.com/v1
//	ANTHROPIC_BASE_URL=https://api.anthropic.com
//
// Usage:
//
//	go run ./cmd/demo                              # interactive REPL (terminal)
//	go run ./cmd/demo -ui tui                       # interactive TUI
//	go run ./cmd/demo -prompt "What time is it?"   # one-shot, then exit
//	go run ./cmd/demo -max-turns 10                # per-message tool/model loop cap (0=unlimited)
//	go run ./cmd/demo -summarize-at 40             # auto-summarize when window exceeds N messages
//	go run ./cmd/demo -summarize-tokens 80000      # also trigger on coarse token estimate
//	go run ./cmd/demo -segmented-summary           # tool-driven segmented fallback on overflow
//	go run ./cmd/demo -provider anthropic
//	go run ./cmd/demo -session chat1
//	go run ./cmd/demo -mcp-stdio "npx -y @modelcontextprotocol/server-everything"
//	go run ./cmd/demo -mcp-http http://localhost:8080/mcp
//	go run ./cmd/demo -mcp-config mcp.json
//	go run ./cmd/demo -a2a-url http://127.0.0.1:8080
//	go run ./cmd/demo -a2a-serve :8080
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/internal/livetest"
	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
)

func main() {
	prompt := flag.String("prompt", "", "one-shot user message (skip interactive REPL)")
	modelID := flag.String("model", "", "model id (default: LIVE_*_MODEL / LIVE_MODEL / claude-sonnet-4-20250514)")
	providerName := flag.String("provider", "openai", "provider: openai | anthropic")
	sessionID := flag.String("session", "", "session id (persists under .sessions/; default \"demo\" in interactive mode)")
	maxTurns := flag.Int("max-turns", 20, "max model↔tool turns per user message (0=unlimited)")
	summarizeAt := flag.Int("summarize-at", 40, "proactively summarize when model window exceeds N messages (0=overflow-only)")
	summaryKeep := flag.Int("summary-keep", 10, "messages to keep recent when summarizing")
	summarizeTokens := flag.Int("summarize-tokens", 0, "proactively summarize when estimated tokens exceed N (0=off)")
	noSummarize := flag.Bool("no-summarize", false, "disable automatic context summarization")
	segmentedSummary := flag.Bool("segmented-summary", false, "enable tool-driven segmented summarization fallback on overflow")
	logLevel := flag.String("log-level", "info", "framework log level: debug|info|warn|error|off")
	uiMode := flag.String("ui", "terminal", "interactive UI: terminal | tui")

	var mcpCfg mcpConfig
	flag.StringVar(&mcpCfg.ConfigFile, "mcp-config", "", "MCP servers JSON file (mcpServers; FastMCP/Cursor style)")
	flag.Func("mcp-stdio", "MCP stdio server as \"command [args...]\" (repeatable)", func(s string) error {
		mcpCfg.Stdio = append(mcpCfg.Stdio, s)
		return nil
	})
	flag.Func("mcp-http", "MCP streamable HTTP URL (repeatable)", func(s string) error {
		mcpCfg.HTTP = append(mcpCfg.HTTP, s)
		return nil
	})
	flag.Func("mcp-sse", "MCP SSE URL (repeatable)", func(s string) error {
		mcpCfg.SSE = append(mcpCfg.SSE, s)
		return nil
	})
	var a2aCfg a2aConfig
	flag.StringVar(&a2aCfg.Serve, "a2a-serve", "", "listen address to expose this agent as an A2A JSON-RPC server (e.g. :8080)")
	flag.Func("a2a-url", "A2A Agent Card base URL (repeatable); remote skills become tools", func(s string) error {
		a2aCfg.URLs = append(a2aCfg.URLs, s)
		return nil
	})
	flag.Parse()

	cfg := demoConfig{
		Prompt:           *prompt,
		ModelID:          *modelID,
		ProviderName:     *providerName,
		SessionID:        *sessionID,
		MaxTurns:         *maxTurns,
		SummarizeAt:      *summarizeAt,
		SummaryKeep:      *summaryKeep,
		SummarizeTokens:  *summarizeTokens,
		NoSummarize:      *noSummarize,
		SegmentedSummary: *segmentedSummary,
		LogLevel:         *logLevel,
		UI:               strings.ToLower(strings.TrimSpace(*uiMode)),
		MCP:              mcpCfg,
		A2A:              a2aCfg,
	}
	if err := realMain(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type demoConfig struct {
	Prompt           string
	ModelID          string
	ProviderName     string
	SessionID        string
	MaxTurns         int
	SummarizeAt      int
	SummaryKeep      int
	SummarizeTokens  int
	NoSummarize      bool
	SegmentedSummary bool
	LogLevel         string
	UI               string // terminal | tui
	Logger           logger.Logger
	MCP              mcpConfig
	A2A              a2aConfig
}

func realMain(cfg demoConfig) error {
	if cfg.MaxTurns < 0 {
		return fmt.Errorf("-max-turns must be >= 0")
	}
	if cfg.SummarizeAt < 0 {
		return fmt.Errorf("-summarize-at must be >= 0")
	}
	if cfg.SummarizeTokens < 0 {
		return fmt.Errorf("-summarize-tokens must be >= 0")
	}
	if cfg.SummaryKeep < 1 {
		return fmt.Errorf("-summary-keep must be >= 1")
	}
	switch cfg.UI {
	case "", "terminal", "tui":
	default:
		return fmt.Errorf("unknown -ui %q (want terminal|tui)", cfg.UI)
	}

	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	env, err := livetest.Load(wd)
	if err != nil {
		return fmt.Errorf("load .env: %w", err)
	}
	if env.APIKey == "" {
		return fmt.Errorf("API_KEY must be set in .env or environment")
	}

	pc, err := resolveProvider(env, cfg.ProviderName, cfg.ModelID)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var mcp *mcpBundle
	if !cfg.MCP.empty() {
		mcp, err = connectMCP(ctx, cfg.MCP)
		if err != nil {
			return err
		}
		defer mcp.Close()
	}

	var a2a *a2aBundle
	if len(cfg.A2A.URLs) > 0 {
		a2a, err = connectA2A(ctx, cfg.A2A)
		if err != nil {
			return err
		}
		defer a2a.Close()
	}

	interactive := strings.TrimSpace(cfg.Prompt) == ""
	sid := cfg.SessionID
	if sid == "" && interactive {
		sid = "demo"
	}

	useTUI := interactive && cfg.UI == "tui"
	var logCh chan string
	if useTUI {
		logCh = make(chan string, 256)
		cfg.Logger = newTUILogger(cfg.LogLevel, logCh)
	}

	a, toolNames, err := buildAgent(wd, pc, sid, cfg, mcp, a2a)
	if err != nil {
		return err
	}

	if cfg.A2A.Serve != "" {
		srv, err := serveA2A(ctx, cfg.A2A.Serve, a)
		if err != nil {
			return err
		}
		if a2a == nil {
			a2a = &a2aBundle{}
		}
		a2a.server = srv
	}

	meta := sessionMeta{
		Provider:  pc.name,
		Model:     pc.model,
		SessionID: sid,
		MaxTurns:  cfg.MaxTurns,
		MCP:       mcpStatus(mcp),
	}

	if !useTUI {
		printBanner(pc, sid, mcp, a2a, cfg)
	}

	if interactive {
		if useTUI {
			sessionMsgs := loadSessionMessages(wd, sid)
			return runTUI(ctx, a, toolNames, meta, logCh, sessionMsgs)
		}
		return runInteractive(ctx, a, toolNames)
	}
	return runOnce(ctx, a, cfg.Prompt)
}

type sessionMeta struct {
	Provider  string
	Model     string
	SessionID string
	MaxTurns  int
	MCP       string
}

func mcpStatus(mcp *mcpBundle) string {
	if mcp == nil || len(mcp.clients) == 0 {
		return "off"
	}
	return fmt.Sprintf("ok (%d)", len(mcp.clients))
}

func printBanner(pc providerCase, sid string, mcp *mcpBundle, a2a *a2aBundle, cfg demoConfig) {
	fmt.Printf("provider: %s\n", pc.name)
	fmt.Printf("model:    %s\n", pc.model)
	if sid != "" {
		fmt.Printf("session:  %s\n", sid)
	}
	if mcp != nil {
		fmt.Printf("mcp:      %d server(s)\n", len(mcp.clients))
	}
	if a2a != nil && len(a2a.clients) > 0 {
		fmt.Printf("a2a:      %d remote agent(s)\n", len(a2a.clients))
	}
	if cfg.A2A.Serve != "" {
		fmt.Printf("a2a-serve: %s\n", cfg.A2A.Serve)
	}
	if cfg.MaxTurns == 0 {
		fmt.Printf("max-turns: unlimited\n")
	} else {
		fmt.Printf("max-turns: %d\n", cfg.MaxTurns)
	}
	if cfg.NoSummarize {
		fmt.Printf("summarize: off\n")
	} else {
		parts := []string{}
		if cfg.SummarizeAt == 0 {
			parts = append(parts, "overflow-only")
		} else {
			parts = append(parts, fmt.Sprintf("at>%d messages", cfg.SummarizeAt))
		}
		if cfg.SummarizeTokens > 0 {
			parts = append(parts, fmt.Sprintf("tokens>%d", cfg.SummarizeTokens))
		}
		parts = append(parts, fmt.Sprintf("keep=%d", cfg.SummaryKeep))
		if cfg.SegmentedSummary {
			parts = append(parts, "segmented")
		}
		fmt.Printf("summarize: %s\n", strings.Join(parts, ", "))
	}
	fmt.Println()
}

type providerCase struct {
	name     string
	model    string
	provider model.Provider
}

func resolveProvider(env livetest.Env, name, modelOverride string) (providerCase, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai":
		if env.OpenAIBaseURL == "" {
			return providerCase{}, fmt.Errorf("OPENAI_BASE_URL must be set for -provider=openai")
		}
		modelID := firstNonEmpty(modelOverride, env.OpenAIModel)
		env.OpenAIModel = modelID
		return providerCase{name: "openai", model: modelID, provider: livetest.NewOpenAI(env)}, nil
	case "anthropic":
		if env.AnthropicBaseURL == "" {
			return providerCase{}, fmt.Errorf("ANTHROPIC_BASE_URL must be set for -provider=anthropic")
		}
		modelID := firstNonEmpty(modelOverride, env.AnthropicModel)
		env.AnthropicModel = modelID
		return providerCase{name: "anthropic", model: modelID, provider: livetest.NewAnthropic(env)}, nil
	default:
		return providerCase{}, fmt.Errorf("unknown -provider %q (want openai|anthropic)", name)
	}
}

func buildAgent(wd string, pc providerCase, sessionID string, cfg demoConfig, mcp *mcpBundle, a2a *a2aBundle) (*agent.Agent, []string, error) {
	system := `You are a helpful conversational assistant.
Use tools when they help answer the user. Prefer built-in tools for time and random strings.
Be concise unless the user asks for detail.`

	opts := []agent.Option{
		agent.WithSystem(system),
		agent.WithProvider(pc.provider),
		agent.WithTools(builtinTools()...),
		agent.WithLimits(run.Limits{MaxTurns: cfg.MaxTurns}),
	}
	if cfg.Logger != nil {
		opts = append(opts, agent.WithLogger(cfg.Logger))
	} else if log := demoLogger(cfg.LogLevel); log != nil {
		opts = append(opts, agent.WithLogger(log))
	}
	if !cfg.NoSummarize {
		opts = append(opts, agent.WithContextPolicy(newSummarizePolicy(pc.provider, cfg)))
	}
	if sessionID != "" {
		store := &memory.FileStore{Dir: filepath.Join(wd, ".sessions")}
		opts = append(opts, agent.WithSession(sessionID, store))
	}
	if mcp != nil {
		for _, c := range mcp.clients {
			opts = append(opts, agent.WithToolRegistry(c.Registry()))
		}
	}
	if a2a != nil {
		for _, c := range a2a.clients {
			opts = append(opts, agent.WithToolRegistry(c.Registry()))
		}
	}

	a, err := agent.New(opts...)
	if err != nil {
		return nil, nil, err
	}

	var names []string
	for _, s := range a.ToolsRegistry().Specs() {
		names = append(names, s.Name)
	}
	return a, names, nil
}

// newSummarizePolicy compresses the model window when it grows too large, and
// on provider context-overflow. Transcript / session history stays intact.
func newSummarizePolicy(p model.Provider, cfg demoConfig) memory.ContextPolicy {
	windowMax := cfg.SummarizeAt
	if windowMax <= 0 {
		windowMax = cfg.SummaryKeep * 4
		if windowMax < 20 {
			windowMax = 20
		}
	}
	robust := &memory.RobustSummarizer{Provider: p}
	pol := &memory.Summarize{
		Summarizer:           robust,
		Inner:                &memory.Window{MaxMessages: windowMax},
		KeepRecent:           cfg.SummaryKeep,
		ProactiveMaxMessages: cfg.SummarizeAt,
		ProactiveMaxTokens:   cfg.SummarizeTokens,
		MaxMessageBytes:      32 * 1024,
		Fallback:             &memory.Window{MaxMessages: cfg.SummaryKeep},
	}
	if cfg.SegmentedSummary {
		pol.Segmented = &memory.SegmentedSummarizer{Provider: p, MaxTurns: 20}
	}
	return pol
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func demoLogger(level string) logger.Logger {
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		lv = slog.LevelInfo
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	case "off", "none", "silent":
		return nil
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	return logger.FromSlog(slog.New(h))
}
