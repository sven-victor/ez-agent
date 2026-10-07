# ez-agent/a2a

Optional [A2A](https://a2a-protocol.org/) adapter for [ez-agent](https://github.com/sven-victor/ez-agent). Protocol types and JSON-RPC transport come from [`github.com/a2aproject/a2a-go/v2`](https://github.com/a2aproject/a2a-go).

This is a **separate Go module**. Importing `github.com/sven-victor/ez-agent` does not pull in a2a-go.

```bash
go get github.com/sven-victor/ez-agent/a2a
```

```go
import eza2a "github.com/sven-victor/ez-agent/a2a"
```

## Server

Wrap an `*agent.Agent` as a JSON-RPC A2A server (`/invoke` + `/.well-known/agent-card.json`):

```go
card := eza2a.NewJSONRPCCard("echo", "An echo agent", "http://127.0.0.1:8080/invoke")
log.Fatal(eza2a.ListenAndServe(":8080", ag, card))
```

`NewExecutor` implements `a2asrv.AgentExecutor` if you want to plug into a2a-go yourself.

## Client

Resolve an Agent Card and expose skills as `tool.Registry` (same pattern as MCP):

```go
c, err := eza2a.Dial(ctx, "http://127.0.0.1:8080")
parent, err := agent.New(
    agent.WithProvider(p),
    agent.WithToolRegistry(c.Registry()),
)
```

Each advertised skill becomes a tool with `{ "prompt": string }` input. The handler calls `SendMessage`.

## Examples (no API key)

From the **repository root** (uses `go.work`):

```bash
go run ./a2a/examples/server -addr :8080
go run ./a2a/examples/client -url http://127.0.0.1:8080 -prompt hello
```

Official CLI (optional): `a2a discover http://127.0.0.1:8080` / `a2a send http://127.0.0.1:8080 "hello"`.

## v1 scope

Implemented: JSON-RPC, text parts, in-memory task store, Agent Card, `SendMessage`.

Not implemented: gRPC, SSE streaming, HITL `input-required`, push notifications, file/data parts (non-text is flattened to a placeholder string).

Local multi-agent inside one process should use `agent.AsTool`, not A2A.
