# ez-agent

Go SDK for building AI agents with a typed turn loop, tools, streaming events, hooks, and context-window policies.

Inspired by [Strands Agents](https://strandsagents.com/) **behavior** (loop, tool pairing, hooks), with a Go-idiomatic API: small interfaces, `context.Context`, and `New(opts ...Option)`.

## Features

| Area | What you get |
|------|----------------|
| Agent loop | Model → optional tools → write results → repeat until stop |
| Provider | OpenAI-compatible, Anthropic Messages, Gemini (`model/openai`, `model/anthropic`, `model/gemini`); `Stream` + `Complete` |
| Tools | Explicit `Spec` + `Handler`; concurrent or sequential; unknown tools become error results |
| MCP | `tool/mcp` (stdio / HTTP / SSE / in-process); live `WithToolRegistry` + `RefreshTools` |
| Messages | Text, Image, Audio, Video, Document, AudioOut, Reasoning, Opaque, ToolUse, ToolResult |
| Structured output | Native JSON Schema via `WithOutputSchema` (OpenAI `response_format`, Anthropic `output_config`, Gemini `responseSchema`) |
| Streaming | `RunStream` yields typed `event.Event` values |
| Hooks / Plugins | Before/After Run, Model, Tool, Summary; plugins register tools + hooks; `contrib/memoryplugin` |
| Logging | Optional `WithLogger` (`Debug`/`Info`/`Warn`/`Error`); `logger.FromSlog` for `log/slog` |
| Limits | `MaxTurns`, `MaxTotalTokens`, `MaxOutputTokens`; cancel via `context` |
| Memory | `Noop`, `Window`, `Summarize` (+ `RobustSummarizer`, `SegmentedSummarizer`), `Offload`, `DegradeMedia`; `SessionStore`; `LongTerm` |
| Checkpoint | `WithCheckpoint` mid-run snapshots; `LoadCheckpoint` / `SaveCheckpoint` / `Continue` |
| HITL | `WithInterrupter` pause before tools; `Resume` / `ResumeStream` / `StateFromResult` |
| Realtime | `realtime.Session` (outside Turn loop); `Pipe` + `realtime/openai` WebSocket |
| Retries | `WithRetryPolicy` exponential backoff on `ErrThrottled` |

## Install

```bash
go get github.com/sven-victor/ez-agent
```

Requires Go 1.25+ (see `go.mod`).

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/model/openai"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

func main() {
	p := openai.New(
		openai.WithAPIKey(os.Getenv("API_KEY")),
		openai.WithBaseURL(os.Getenv("OPENAI_BASE_URL")), // e.g. https://api.openai.com/v1
		openai.WithModel("gpt-4o-mini"),
	)

	a, err := agent.New(
		agent.WithProvider(p),
		agent.WithSystem("You are a helpful assistant."),
		agent.WithTools(tool.Registration{
			Spec: tool.Spec{
				Name:        "echo",
				Description: "Echo the input text",
				InputSchema: []byte(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{
					ToolUseID: call.ID,
					OK:        true,
					Content:   nil, // optional message.Part list
				}, nil
			},
		}),
		agent.WithLimits(run.Limits{MaxTurns: 10}),
	)
	if err != nil {
		panic(err)
	}

	res, err := a.Run(context.Background(), run.Input{Text: "Hello"})
	if err != nil {
		panic(err)
	}
	fmt.Println(res.StopReason)
}
```

Runnable demo (loads `.env` automatically; see `.env.example`):

```bash
cp .env.example .env   # then fill in API_KEY

go run ./cmd/demo                                          # interactive REPL (terminal)
go run ./cmd/demo -ui tui                                  # interactive multi-pane TUI
go run ./cmd/demo -prompt "What time is it?"               # one-shot, then exit
go run ./cmd/demo -max-turns 10                            # per-message loop cap (default 20, 0=unlimited)
go run ./cmd/demo -summarize-at 40 -summary-keep 10         # auto-summarize model window (default)
go run ./cmd/demo -summarize-tokens 80000                   # also trigger on coarse token estimate
go run ./cmd/demo -segmented-summary                        # tool-driven segmented fallback on overflow
go run ./cmd/demo -no-summarize                             # disable summarization
go run ./cmd/demo -provider anthropic -session chat1
go run ./cmd/demo -mcp-stdio "npx -y @modelcontextprotocol/server-everything"
go run ./cmd/demo -mcp-http http://localhost:8080/mcp
go run ./cmd/demo -mcp-config mcp.json   # mcpServers JSON (HTTP headers, stdio env, …)
```

Example `mcp.json` (FastMCP / Cursor canonical `mcpServers` format):

```json
{
  "mcpServers": {
    "devops": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer ..."
      },
      "proxy": "socks5://127.0.0.1:1080"
    },
    "local": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-everything"]
    }
  }
}
```

Config-file servers prefix tool names with `<server>_` (e.g. `devops_list_pods`).
Remote HTTP/SSE entries may set `proxy` to an `http`/`https`/`socks5`/`socks5h` URL.

Built-in tools: `current_time`, `format_time`, `random_string`. Slash commands in the REPL: `/help`, `/tools`, `/quit`. Interactive input uses readline (↑/↓ history, ←/→ cursor, CJK-aware backspace); history is stored under `.sessions/demo_history`.

Context: by default the demo enables `memory.Summarize` with `RobustSummarizer` (retry / map-reduce / truncate), proactive when the model window exceeds `-summarize-at` messages (optional `-summarize-tokens`), and on provider overflow. Use `-segmented-summary` for tool-driven segmented fallback. Session `Transcript` is unchanged — only the model window is compressed.

## Architecture

```text
agent.Agent          # New / Run / RunStream / Resume / Continue / Checkpoint
    └── loop         # turn state machine, limits, cancel, overflow + HITL
          ├── model.Provider (+ openai / anthropic / gemini)
          ├── tool.Executor / Registry (Multi / MapRegistry)
          ├── hook.Registry
          ├── logger.Logger (optional diagnostics)
          └── memory.ContextPolicy
domain: message · event · run
cross-cutting: plugin · realtime (not in Turn loop) · contrib/
```

Dependency direction: `agent` → `loop` → (`model`, `tool`) → (`message`, `event`, `run`). Vendor wire formats stay inside provider adapters; the loop never imports a vendor SDK.

## Agent API

### Construction options

```go
a, err := agent.New(
    agent.WithProvider(p),                 // required
    agent.WithSystem("..."),               // system prompt text
    agent.WithSystemConfig(model.System{Text: "…", Parts: …}), // multipart system (needs Caps.SystemParts)
    agent.WithTools(reg1, reg2),           // local MapRegistry tools
    agent.WithToolRegistry(mcpClient),     // live extra Registry (e.g. MCP); not snapshotted
    agent.WithToolChoice(tool.Choice{Type: "required"}), // auto|none|required|named
    agent.WithHooks(hooks),
    agent.WithPlugins(plugin1),
    agent.WithLimits(run.Limits{MaxTurns: 20, MaxTotalTokens: 100_000, MaxOutputTokens: 2048}),
    agent.WithContextPolicy(&memory.Window{MaxMessages: 40}),
    agent.WithSession("chat-1", &memory.FileStore{Dir: ".sessions"}),
    agent.WithOutputSchema(schemaJSON, "answer"),
    agent.WithExecutor(&tool.Sequential{}), // default: Concurrent
    agent.WithRetryPolicy(model.ExponentialBackoff{MaxAttempts: 5}),
    agent.WithInterrupter(loop.FuncInterrupter(…)),
    agent.WithCheckpoint("run-1", memory.NewMemCheckpointStore()),
    agent.WithPreferComplete(true),        // use Provider.Complete instead of Stream
    agent.WithLogger(logger.FromSlog(slog.Default())), // optional framework diagnostics
)
```

`New` fails if the provider is missing or a plugin `Install` returns an error (no half-installed agent).

Accessors:

| Method | Returns |
|--------|---------|
| `Hooks()` | `*hook.Registry` |
| `Tools()` | local `*tool.MapRegistry` (plugins / `WithTools`) |
| `ToolsRegistry()` | live composed registry (`Tools` + `WithToolRegistry` extras) |

### Logging

Pass an optional `logger.Logger` via `WithLogger` for framework diagnostics (`Debug` / `Info` / `Warn` / `Error`). When omitted, logging is a no-op. Key-value args follow `log/slog` conventions.

```go
import (
    "log/slog"
    "github.com/sven-victor/ez-agent/logger"
)

a, err := agent.New(
    agent.WithProvider(p),
    agent.WithLogger(logger.FromSlog(slog.Default())),
)
```

Emissions cover agent construction, run start/finish, model calls, overflow/retries, tool start/finish, session/checkpoint I/O, cancel, fatal errors, and context policies (summarize / window / offload / media degrade). `agent.WithLogger` also fills nil `Logger` fields on known `memory.ContextPolicy` / summarizer trees via `memory.SetLogger`.

Hooks remain for control/mutation; the logger is observability only.

### Run vs RunStream

```go
res, err := a.Run(ctx, run.Input{Text: "Hi"})

for ev := range a.RunStream(ctx, run.Input{Text: "Hi"}) {
    switch e := ev.(type) {
    case event.RunStarted:
    case event.ModelDelta:
        fmt.Print(e.TextDelta) // also AudioChunk / MIME when AudioOut streams
    case event.ModelFinished:
        // per-cycle assistant message
    case event.ToolStarted:
        fmt.Println("tool", e.Call.Name)
    case event.ToolFinished:
        fmt.Println("done", e.Result.OK)
    case event.Interrupted:
        // HITL: e.Pending holds tool calls awaiting Resume
    case event.RunFinished:
        res = e.Result
    case event.RunError:
        return e.Err
    case event.Cancelled:
        // ctx canceled
    }
}
```

### Input

```go
run.Input{
    Text:     "optional sugar → one user text message",
    Messages: []message.Message{ /* optional extra history */ },
}
```

Both fields may be set; `Text` is prepended as a user message, then `Messages` are appended.

### Result and stop reasons

```go
type Result struct {
    Messages   []message.Message
    StopReason StopReason
    Usage      Usage
    Structured []byte           // set when WithOutputSchema validation succeeds
    Pending    *PendingTools    // set when interrupted (HITL)
}
```

| StopReason | Meaning |
|------------|---------|
| `end_turn` | Model finished without pending tools |
| `max_turns` | Hit `Limits.MaxTurns` (checked at the start of each cycle) |
| `max_tokens` | Hit `Limits.MaxTotalTokens`, or provider reported length / truncated generation |
| `canceled` | `ctx` canceled |
| `interrupted` | HITL pause before tools; use `Resume` |
| `tool_use` | **Intermediate only** — never a final `Result.StopReason` |
| `error` | Reserved terminal reason (prefer returning `error` for fatal failures) |

Invariants:

- Every `ToolUse` gets a matching `ToolResult` before the next model call (success, failure, deny, cancel, or incomplete sanitization).
- Incomplete / truncated tool JSON is **not** executed; on `max_tokens` with tool calls, all tool uses in that message are stripped and annotated.

### Resume, Continue, Checkpoint

```go
import "github.com/sven-victor/ez-agent/loop"

a, _ := agent.New(
    agent.WithProvider(p),
    agent.WithInterrupter(loop.FuncInterrupter(
        func(ctx context.Context, st *run.State, calls []tool.Call) (bool, error) {
            return true, nil // pause every tool batch
        },
    )),
    agent.WithCheckpoint("run-1", &memory.FileCheckpointStore{Dir: ".checkpoints"}),
)

res, _ := a.Run(ctx, run.Input{Text: "…" })
if res.StopReason == run.StopInterrupted {
    st := run.StateFromResult(res)
    res, _ = a.Resume(ctx, st)           // or a.ResumeStream(ctx, st)
}

// Crash recovery / continue without new user input:
st, _ := a.LoadCheckpoint(ctx)
res, _ = a.Continue(ctx, st)
_ = a.SaveCheckpoint(ctx, st)            // manual snapshot
```

`WithCheckpoint` also auto-saves after tool rounds and at end of run.

## Agent loop (behavior)

Each run roughly follows:

1. `BeforeRun` hook; append user input (unless `Continue` / `Resume`)
2. If `Pending` tools exist (Resume): execute them, clear Pending, `Turn++`
3. Cycle top: check limits / `ctx`
4. `ContextPolicy.Prepare` → `BeforeModel` → validate capabilities → `Provider.Stream` (or `Complete` when `WithPreferComplete`)
5. On context overflow → `ContextPolicy.Reduce` (must shrink) → retry (default cap 3)
6. After every model attempt: `AfterModel` (success or failure; on failure `Err` is set). On success: sanitize incomplete tool calls, append assistant message; hooks may `Retry` / `ReplaceMessage` (ignored when `Err != nil`)
7. If stop is not `tool_use` → finish
8. Else optional HITL interrupt → else execute tools (`BeforeTool` / `AfterTool`), append results, `Turn++`, continue
9. `AfterRun` → return `Result`

Cancel: in-flight tools exit ASAP; unpaired tool uses get synthetic error results so history stays consistent.

Provider retries (`WithRetryPolicy`) apply to transient errors such as `model.ErrThrottled` (overflow uses `Reduce` instead).

## Tools

Register tools explicitly (no decorators / reflection):

```go
tool.Registration{
    Spec: tool.Spec{
        Name:        "search",
        Description: "Search docs",
        InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
    },
    Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
        // Business failure: OK=false, continue the loop
        // Framework error (returned error): also normalized to OK=false so pairing holds
        return tool.Result{
            ToolUseID: call.ID,
            OK:        true,
            Content:   []message.Part{message.Text{Text: "hits..."}},
        }, nil
    },
}
```

Behavior:

- Same-batch tools run **concurrently** by default (`WithExecutor(&tool.Sequential{})` for ordered)
- Unknown tool name → `OK=false` result, other tools still run
- `BeforeTool` may `Deny` / `Skip` / `ReplaceInput`; deny/skip still produce a `ToolResult`
- `AfterTool` runs whenever `BeforeTool` does **not** return an error (including handler failure, unknown tool, cancel, deny/skip); it may `ReplaceResult`. If `BeforeTool` itself fails, `AfterTool` is skipped.

### Live Multi vs snapshot Compose

| Helper | Behavior |
|--------|----------|
| `tool.NewMulti(regs...)` | **Live** delegation; `Specs`/`Lookup` always query backends (used by `WithToolRegistry`) |
| `tool.Compose(regs...)` | **Snapshot** into a `*MapRegistry` (later names win at compose time) |

```go
agent.WithToolChoice(tool.Choice{Type: "named", Name: "search"})
```

## Messages and media

```go
message.NewUserText("hello")
message.NewUserParts(
    message.Text{Text: "describe this"},
    message.Image{Blob: message.Blob{MIME: "image/png", Bytes: png}, Detail: "high"},
    message.Audio{Blob: message.Blob{MIME: "audio/wav", Bytes: wav}, Duration: 2 * time.Second},
)
message.NewAssistantText("hi")
message.NewToolResultMessage(message.ToolResult{ToolUseID: id, OK: true, Parts: …})
```

Part kinds: `Text`, `Image`, `Audio`, `Video`, `Document`, `AudioOut`, `Reasoning` (text + optional signature), `Opaque` (vendor passthrough; needs `Caps.Opaque`), `ToolUse`, `ToolResult`.

`Blob` must set exactly one of `Bytes`, `URL`, or `Path` (optional `Filename`, `Size`). Screenshots are `Image` with `image/png` or `image/jpeg`.

Unsupported parts for the current provider return `model.ErrUnsupportedPart` (never silently dropped), unless you wrap a `memory.DegradeMedia` policy.

## Provider (OpenAI / Anthropic / Gemini)

```go
p := openai.New(
    openai.WithAPIKey(key),
    openai.WithBaseURL("https://api.openai.com/v1"),
    openai.WithModel("gpt-4o-mini"),
    openai.WithOrganization("org-…"),           // optional OpenAI-Organization header
    openai.WithHeader("OpenAI-Project", "proj_…"), // optional custom headers
    openai.WithHTTPClient(http.DefaultClient),
    openai.WithCaps(…), // optional override
)

p := anthropic.New(
    anthropic.WithAPIKey(key),
    anthropic.WithBaseURL("https://api.anthropic.com"), // or a gateway
    anthropic.WithModel("claude-sonnet-4-20250514"),
    anthropic.WithHeader("anthropic-beta", "…"), // optional custom headers
)

p := gemini.New(
    gemini.WithAPIKey(key),
    gemini.WithModel("gemini-2.0-flash"),
    gemini.WithHeader("X-Trace-Id", "…"), // optional custom headers
)
```

Default capabilities (override with `WithCaps`):

| Cap | OpenAI | Anthropic | Gemini |
|-----|--------|-----------|--------|
| Text / Image / Document | ✓ | ✓ | ✓ |
| Audio / Video in | ✓ | ✗ (API has no native blocks) | ✓ |
| AudioOut | ✓ | ✗ | ✓ |
| Tools / Streaming | ✓ | ✓ | ✓ |
| JSONSchema | ✓ `response_format` | ✓ `output_config.format` | ✓ `responseSchema` |
| SystemParts | ✓ | ✓ | ✓ |
| Opaque | ✓ | ✓ (unknown content blocks) | ✓ |
| Reasoning | ✓ `reasoning_content` | ✓ `thinking` blocks | text fallback on encode |

`model.Validate` rejects unsupported Parts / System.Parts / OutputSchema when Caps disallow them. Cross-adapter fixtures live in `model/conform`.

`model.Result` includes wire capture for each invocation:

| Field | Meaning |
|-------|---------|
| `RawRequest` | JSON body sent to the provider |
| `RawResponse` | Full JSON body for `Complete`; for `Stream` (SSE), newline-joined payloads after each `data:` line (including `[DONE]` when present) |

Available on each `Stream`/`Complete` return value and via `AfterModel.Result`.

Sentinel errors: `model.ErrUnsupportedPart`, `model.ErrOverflow`, `model.ErrThrottled`.

## Hooks and plugins

```go
h := hook.NewRegistry()
h.OnBeforeModel(func(ctx context.Context, e *hook.BeforeModel) error { return nil })
h.OnAfterModel(func(ctx context.Context, e *hook.AfterModel) error {
    // Always fired after each provider attempt (success or failure).
    // On failure: e.Err != nil; e.Retry / e.ReplaceMessage are ignored.
    // On success:
    // e.Retry = true
    // e.ReplaceMessage = &msg
    return nil
})
h.OnBeforeTool(func(ctx context.Context, e *hook.BeforeTool) error {
    // e.Deny / e.Skip / e.ReplaceInput
    // Returning an error skips AfterTool for that call.
    return nil
})
h.OnAfterTool(func(ctx context.Context, e *hook.AfterTool) error {
    // Fired unless BeforeTool returned an error (covers OK=false / cancel / deny).
    // e.ReplaceResult = &result
    return nil
})
h.OnBeforeSummary(func(ctx context.Context, e *hook.BeforeSummary) error {
    // e.Skip = true  // abort this summarization attempt (AfterSummary still runs with Err)
    // e.Input = ...  // replace summarizer input
    return nil
})
h.OnAfterSummary(func(ctx context.Context, e *hook.AfterSummary) error {
    // Always fired after each summarization attempt (success, skip, or failure).
    // e.Err != nil on skip/failure; e.ReplaceSummary is ignored then.
    // On success: e.Usage is added to e.State.Usage; e.ReplaceSummary = &msg
    return nil
})

agent.New(agent.WithProvider(p), agent.WithHooks(h))
```

- **Before\***: registration order  
- **After\***: reverse registration order (for symmetric cleanup)

Events: `BeforeRun` / `AfterRun`, `BeforeModel` / `AfterModel`, `BeforeTool` / `AfterTool`, `BeforeSummary` / `AfterSummary` (summary hooks are fired by `memory.Summarize` on proactive Prepare / reactive Reduce).

| After hook | When it runs | Mutation notes |
|------------|--------------|----------------|
| `AfterModel` | After every provider call attempt, success or failure | `Retry` / `ReplaceMessage` only when `Err == nil` |
| `AfterTool` | After tool path when `BeforeTool` did not fail | `ReplaceResult` always applicable when the hook runs |
| `AfterSummary` | After every summarization attempt (incl. skip / summarizer error) | `ReplaceSummary` only when `Err == nil` |

Plugins package tools + hooks:

```go
type Plugin interface {
    Name() string
    Install(h *plugin.Host) error
}
```

`Host` exposes `RegisterTool` and the same `OnBefore*` / `OnAfter*` helpers.

### Long-term memory plugin

```go
import "github.com/sven-victor/ez-agent/contrib/memoryplugin"

lt := memory.NewMemLongTerm()
agent.WithPlugins(&memoryplugin.Plugin{
    Store:  lt,
    Inject: true, // inject search hits into model window (not Transcript)
    TopK:   5,
})
```

Registers tools `remember` and `search_memory`.

## Context / memory

`State` keeps an in-run `Transcript` (returned in `Result.Messages`) and a model `Messages` window. Policies only mutate `Messages`. With `WithSession`, the store persists the **effective window** (`Messages`): the loop `Append`s new turns and `ReplaceAll`s after summarize/trim. Each `message.Message` may carry an optional stable `ID` (UUID; assigned on Append when empty) so SQL adapters can store one row per message—summaries get a new ID, recent messages keep theirs across `ReplaceAll`.

```go
agent.WithContextPolicy(memory.Noop{})

agent.WithContextPolicy(&memory.Window{MaxMessages: 40})

agent.WithContextPolicy(&memory.Summarize{
    Summarizer: &memory.RobustSummarizer{Provider: p}, // retry + map-reduce + truncate
    Segmented:  &memory.SegmentedSummarizer{Provider: p}, // optional tool-driven fallback
    Inner:      &memory.Window{MaxMessages: 40},
    Fallback:   &memory.Window{MaxMessages: 10}, // if summarization fails, drop oldest
    Ratio:                0.3,
    KeepRecent:           10,
    ProactiveMaxMessages: 80,
    ProactiveMaxTokens:   0,          // optional coarse token threshold
    MaxMessageBytes:      32 * 1024, // truncate oversized parts before summarize
})

// Simple one-shot summarizer (no map-reduce / truncate):
// Summarizer: &memory.ProviderSummarizer{Provider: p},

agent.WithContextPolicy(&memory.Offload{
    Store:    memory.NewMemBlobStore(), // or &memory.FileBlobStore{Dir: ".blobs"}
    MaxBytes: 32 * 1024,
    Inner:    &memory.Window{MaxMessages: 40},
})

agent.WithContextPolicy(&memory.DegradeMedia{
    Caps:  p.Caps(),
    Mode:  memory.DegradePlaceholder, // or DegradeFail (Validate errors)
    Inner: &memory.Window{MaxMessages: 40},
})

agent.WithSession("id", &memory.FileStore{Dir: ".sessions"})
// SessionStore.Append / ReplaceAll keep the effective window mid-run;
// Save at end also writes Messages (not full Transcript).
// or memory.NewMemStore() for tests

agent.WithCheckpoint("run-1", memory.NewMemCheckpointStore())
// or &memory.FileCheckpointStore{Dir: ".checkpoints"}
```

On provider overflow, the loop calls `Reduce` and retries (capped).

## Realtime (outside Turn loop)

```go
// In-process mock for tests
s := realtime.OpenPipe(32)

// OpenAI Realtime WebSocket
import rtopenai "github.com/sven-victor/ez-agent/realtime/openai"
s, err := rtopenai.Dial(ctx,
    rtopenai.WithAPIKey(os.Getenv("API_KEY")),
    rtopenai.WithModel("gpt-4o-realtime-preview"),
    rtopenai.WithBaseURL("wss://api.openai.com/v1/realtime"), // optional
)

_ = s.SendText(ctx, "hello")
_ = s.SendAudio(ctx, "audio/pcm", pcmBytes)
for ev := range s.Events() {
    switch e := ev.(type) {
    case realtime.TranscriptDelta:
        fmt.Println(e.Role, e.Text)
    case realtime.AudioDelta:
        // play e.Data — SDK has no speaker
    case realtime.ErrorEvent:
        log.Println(e.Err)
    case realtime.Closed:
        return
    }
}
```

Do not mix bidi realtime into `agent.Run`.

## Structured output

```go
schema := []byte(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`)
a, _ := agent.New(
    agent.WithProvider(p),
    agent.WithOutputSchema(schema, "answer"), // name used by OpenAI; ignored by Anthropic GA shape
)
res, _ := a.Run(ctx, run.Input{Text: "..."})
// res.Structured is the JSON object bytes when validation succeeds
```

Invalid JSON from the model triggers a capped AfterModel-style retry inside the loop.

## MCP

```go
import ezmcp "github.com/sven-victor/ez-agent/tool/mcp"

client, err := ezmcp.DialStdio(ctx, "npx", "-y", "@modelcontextprotocol/server-everything")
// or ezmcp.DialHTTP(ctx, "http://localhost:8080/mcp", ezmcp.WithHeaders(map[string]string{"Authorization": "Bearer …"}))
// or ezmcp.DialSSE(ctx, "http://localhost:8080/sse")
// or ezmcp.DialInProcess(ctx, mcpGoClient, "prefix_") // tests / embedded
// or cfg, _ := ezmcp.LoadConfigFile("mcp.json"); clients, _ := ezmcp.DialConfig(ctx, cfg)
defer client.Close()

a, err := agent.New(
    agent.WithProvider(p),
    agent.WithToolRegistry(client.Registry()), // live: RefreshTools visible on next Run
)
_ = client.RefreshTools(ctx)
```

`tool.Compose(regA, regB)` merges into a snapshot `MapRegistry` when you need a single static map.

## Package layout

```text
ez-agent/
├── agent/           # New, Run, RunStream, Resume, Continue, Options
├── loop/            # Turn state machine, HITL Interrupter
├── model/           # Provider, Capabilities, Validate, RetryPolicy
│   ├── openai/      # Chat Completions SSE
│   ├── anthropic/   # Messages API SSE
│   ├── gemini/      # generateContent SSE
│   └── conform/     # Cross-provider fixture tests
├── tool/            # Spec, MapRegistry, Multi, Concurrent, Sequential, Compose
│   └── mcp/         # MCP client (stdio / HTTP / SSE / in-process)
├── message/         # Message, Part, Blob
├── event/           # Run stream events
├── hook/            # Lifecycle hooks
├── plugin/          # Plugin host
├── contrib/         # Example plugins (memoryplugin)
├── realtime/        # Full-duplex Session (not agent.Run)
│   └── openai/      # OpenAI Realtime WebSocket adapter
├── run/             # Input, State, Result, Limits, Usage
├── memory/          # ContextPolicy, Session, Offload, Checkpoint, LongTerm
├── cmd/demo/          # Interactive chat demo (+ MCP, built-in tools)
├── internal/envfile   # Minimal .env loader
└── internal/livetest  # Shared live credentials for demos/tests
```

## Configuration (.env for demos / live tests)

```env
API_KEY=sk-...
OPENAI_BASE_URL=https://api.openai.com/v1
ANTHROPIC_BASE_URL=https://api.anthropic.com

# Optional model overrides
# LIVE_MODEL=gpt-4o-mini
# LIVE_OPENAI_MODEL=gpt-4o-mini
# LIVE_ANTHROPIC_MODEL=claude-sonnet-4-20250514
```

| Variable | Used by |
|----------|---------|
| `API_KEY` | Demo + all live tests (OpenAI / Anthropic share this key name) |
| `OPENAI_BASE_URL` | Chat Completions adapter (`model/openai`, `-provider openai`) |
| `ANTHROPIC_BASE_URL` | Anthropic Messages adapter (`model/anthropic`, `-provider anthropic`) |
| `LIVE_MODEL` | Default model id for live tests/demo |
| `LIVE_OPENAI_MODEL` / `LIVE_ANTHROPIC_MODEL` | Per-provider override |

Copy `.env.example` to get started. Live tests **Skip** when the required keys are missing. Any OpenAI-compatible base URL works with `model/openai` (ChatGPT is the documented default).

## Testing

```bash
# Unit tests (mocks; no network)
go test ./...

# Live provider + agent tests (need .env)
go test ./model/openai/ ./model/anthropic/ ./agent/ -run Live -v

# Demo
go run ./cmd/demo -prompt "What time is it?"
```

Live coverage (per configured provider): streaming text, tool calls, multi-turn tool-result round-trips, max_tokens, cancel, agent tool loop, MaxTurns, parallel tools, session recall. OpenAI live also exercises JSON Schema when the gateway supports it.

Unit coverage includes: plain text loop, tool round-trips, parallel tools, unknown tool pairing, incomplete tool sanitization, `MaxTurns` / `MaxTotalTokens` / `MaxOutputTokens`, multi-turn tool chains, window prepare/pair safety, overflow reduce+retry, cancel, hooks (lifecycle + deny), stream events, plugins, live tool-registry refresh, HITL interrupt/resume, checkpoints, and provider conform fixtures.

## Design principles (short)

- Prefer small interfaces and Options over large mutable configs
- Results are separate from stream deltas (`Result` return value, not “last event”)
- No public middleware / intervention / callback_handler layers — use Hooks or `RunStream`
- No Bedrock-shaped internal types; adapters own vendor wire formats
- English-only identifiers and comments in the codebase

## License

See repository license when published.
