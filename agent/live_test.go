package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/internal/livetest"
	"github.com/sven-victor/ez-agent/memory"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/run"
	"github.com/sven-victor/ez-agent/tool"
)

type liveProviderCase struct {
	name string
	newP func(t *testing.T) model.Provider
}

func liveProviders(t *testing.T) []liveProviderCase {
	t.Helper()
	var out []liveProviderCase
	if e, err := livetest.LoadFromWD(); err == nil {
		if e.APIKey != "" && e.OpenAIBaseURL != "" {
			env := e
			out = append(out, liveProviderCase{
				name: "openai",
				newP: func(t *testing.T) model.Provider {
					t.Helper()
					return livetest.NewOpenAI(env)
				},
			})
		}
		if e.APIKey != "" && e.AnthropicBaseURL != "" {
			env := e
			out = append(out, liveProviderCase{
				name: "anthropic",
				newP: func(t *testing.T) model.Provider {
					t.Helper()
					return livetest.NewAnthropic(env)
				},
			})
		}
	}
	if len(out) == 0 {
		t.Skip("live test skipped: configure API_KEY with OPENAI_BASE_URL and/or ANTHROPIC_BASE_URL")
	}
	return out
}

func newLiveAgent(t *testing.T, p model.Provider, opts ...agent.Option) *agent.Agent {
	t.Helper()
	all := append([]agent.Option{agent.WithProvider(p)}, opts...)
	a, err := agent.New(all...)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func assertPaired(t *testing.T, msgs []message.Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch v := p.(type) {
			case message.ToolUse:
				pending[v.ID] = true
			case message.ToolResult:
				delete(pending, v.ToolUseID)
			}
		}
	}
	if len(pending) > 0 {
		t.Fatalf("dangling tool uses: %v", pending)
	}
}

func TestLiveAgentToolRoundTrip(t *testing.T) {
	for _, pc := range liveProviders(t) {
		t.Run(pc.name, func(t *testing.T) {
			var toolCalled bool
			a := newLiveAgent(t, pc.newP(t),
				agent.WithSystem("You must use the secret_word tool to answer questions about the secret word."),
				agent.WithTools(tool.Registration{
					Spec: tool.Spec{
						Name:        "secret_word",
						Description: "Returns the secret word. Always call this when asked about the secret word.",
						InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
					},
					Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
						toolCalled = true
						return tool.Result{
							ToolUseID: call.ID,
							OK:        true,
							Content:   []message.Part{message.Text{Text: "zanzibar"}},
						}, nil
					},
				}),
				agent.WithLimits(run.Limits{MaxTurns: 3}),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			res, err := a.Run(ctx, run.Input{Text: "What is the secret word? Use the tool."})
			if err != nil {
				t.Fatal(err)
			}
			if res.StopReason == run.StopToolUse {
				t.Fatal("final stop reason must not be tool_use")
			}
			if !toolCalled {
				t.Fatal("expected the model to call the secret_word tool")
			}
			assertPaired(t, res.Messages)

			final := res.Messages[len(res.Messages)-1]
			t.Logf("final answer: %q stop=%s usage=%+v",
				message.TextContent(final), res.StopReason, res.Usage)
		})
	}
}

func TestLiveAgentMaxTurns(t *testing.T) {
	for _, pc := range liveProviders(t) {
		t.Run(pc.name, func(t *testing.T) {
			calls := 0
			a := newLiveAgent(t, pc.newP(t),
				agent.WithSystem("Always call the dig_deeper tool. Never answer without calling it."),
				agent.WithTools(tool.Registration{
					Spec: tool.Spec{
						Name:        "dig_deeper",
						Description: "Call this to continue investigating. Always call it.",
						InputSchema: json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"}},"required":["note"]}`),
					},
					Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
						calls++
						return tool.Result{
							ToolUseID: call.ID,
							OK:        true,
							Content:   []message.Part{message.Text{Text: "keep digging"}},
						}, nil
					},
				}),
				agent.WithLimits(run.Limits{MaxTurns: 1}),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			res, err := a.Run(ctx, run.Input{Text: "Investigate topic X. Always use dig_deeper."})
			if err != nil {
				t.Fatal(err)
			}
			if res.StopReason != run.StopMaxTurns {
				t.Fatalf("expected max_turns, got %s (tool_calls=%d)", res.StopReason, calls)
			}
			assertPaired(t, res.Messages)
			t.Logf("max_turns stop after %d tool calls, usage=%+v", calls, res.Usage)
		})
	}
}

func TestLiveAgentStreamAndCalculator(t *testing.T) {
	for _, pc := range liveProviders(t) {
		t.Run(pc.name, func(t *testing.T) {
			a := newLiveAgent(t, pc.newP(t),
				agent.WithSystem("You are concise. Use the calculator tool for arithmetic."),
				agent.WithTools(tool.Registration{
					Spec: tool.Spec{
						Name:        "calculator",
						Description: "Add two numbers.",
						InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`),
					},
					Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
						var in struct {
							A float64 `json:"a"`
							B float64 `json:"b"`
						}
						if err := json.Unmarshal(call.Input, &in); err != nil {
							return tool.Result{ToolUseID: call.ID, OK: false, Content: []message.Part{message.Text{Text: err.Error()}}}, nil
						}
						sum := in.A + in.B
						return tool.Result{
							ToolUseID: call.ID,
							OK:        true,
							Content:   []message.Part{message.Text{Text: fmt.Sprintf("%g", sum)}},
						}, nil
					},
				}),
				agent.WithLimits(run.Limits{MaxTurns: 4}),
				agent.WithContextPolicy(&memory.Window{MaxMessages: 20}),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			var (
				deltas     int
				toolCalled bool
				result     *run.Result
			)
			for ev := range a.RunStream(ctx, run.Input{Text: "What is 19 + 23? Use the calculator tool."}) {
				switch e := ev.(type) {
				case event.ModelDelta:
					deltas++
				case event.ToolFinished:
					toolCalled = true
				case event.RunFinished:
					result = e.Result
				case event.RunError:
					t.Fatal(e.Err)
				}
			}
			if result == nil {
				t.Fatal("no result")
			}
			if !toolCalled {
				t.Fatal("expected calculator tool call")
			}
			assertPaired(t, result.Messages)
			final := message.TextContent(result.Messages[len(result.Messages)-1])
			if !strings.Contains(final, "42") {
				t.Logf("answer may not contain 42 (model wording): %q", final)
			}
			t.Logf("deltas=%d stop=%s answer=%q usage=%+v", deltas, result.StopReason, final, result.Usage)
		})
	}
}

func TestLiveAgentPlainText(t *testing.T) {
	for _, pc := range liveProviders(t) {
		t.Run(pc.name, func(t *testing.T) {
			a := newLiveAgent(t, pc.newP(t),
				agent.WithSystem("Reply with exactly one short sentence."),
				agent.WithLimits(run.Limits{MaxTurns: 1}),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := a.Run(ctx, run.Input{Text: "Say hello."})
			if err != nil {
				t.Fatal(err)
			}
			if res.StopReason != run.StopEndTurn {
				t.Fatalf("stop=%s", res.StopReason)
			}
			if len(res.Messages) == 0 {
				t.Fatal("no messages")
			}
			text := message.TextContent(res.Messages[len(res.Messages)-1])
			if text == "" {
				t.Fatal("empty assistant text")
			}
			t.Logf("answer=%q usage=%+v", text, res.Usage)
		})
	}
}

func TestLiveAgentParallelTools(t *testing.T) {
	for _, pc := range liveProviders(t) {
		t.Run(pc.name, func(t *testing.T) {
			var sawA, sawB bool
			a := newLiveAgent(t, pc.newP(t),
				agent.WithSystem("You have two tools. Call BOTH get_alpha and get_beta in the same response before answering."),
				agent.WithTools(
					tool.Registration{
						Spec: tool.Spec{
							Name:        "get_alpha",
							Description: "Returns the alpha code.",
							InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
						},
						Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
							sawA = true
							return tool.Result{ToolUseID: call.ID, OK: true, Content: []message.Part{message.Text{Text: "A1"}}}, nil
						},
					},
					tool.Registration{
						Spec: tool.Spec{
							Name:        "get_beta",
							Description: "Returns the beta code.",
							InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
						},
						Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
							sawB = true
							return tool.Result{ToolUseID: call.ID, OK: true, Content: []message.Part{message.Text{Text: "B2"}}}, nil
						},
					},
				),
				agent.WithLimits(run.Limits{MaxTurns: 4}),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			res, err := a.Run(ctx, run.Input{Text: "Fetch both alpha and beta codes using the tools, then report them."})
			if err != nil {
				t.Fatal(err)
			}
			assertPaired(t, res.Messages)
			if !sawA || !sawB {
				t.Logf("model may have called only one tool (A=%v B=%v); stop=%s", sawA, sawB, res.StopReason)
			}
			t.Logf("A=%v B=%v stop=%s usage=%+v", sawA, sawB, res.StopReason, res.Usage)
		})
	}
}

func TestLiveAgentSessionPersist(t *testing.T) {
	for _, pc := range liveProviders(t) {
		t.Run(pc.name, func(t *testing.T) {
			store := memory.NewMemStore()
			sid := "live-session-" + pc.name

			a1 := newLiveAgent(t, pc.newP(t),
				agent.WithSystem("Remember facts the user tells you. Be brief."),
				agent.WithSession(sid, store),
				agent.WithLimits(run.Limits{MaxTurns: 1}),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			_, err := a1.Run(ctx, run.Input{Text: "My favorite color is teal. Acknowledge in one short sentence."})
			if err != nil {
				t.Fatal(err)
			}

			a2 := newLiveAgent(t, pc.newP(t),
				agent.WithSystem("Use conversation history. Be brief."),
				agent.WithSession(sid, store),
				agent.WithLimits(run.Limits{MaxTurns: 1}),
			)
			res, err := a2.Run(ctx, run.Input{Text: "What is my favorite color?"})
			if err != nil {
				t.Fatal(err)
			}
			final := message.TextContent(res.Messages[len(res.Messages)-1])
			if !strings.Contains(strings.ToLower(final), "teal") {
				t.Logf("session recall may have failed (model wording): %q", final)
			}
			t.Logf("recall=%q messages=%d", final, len(res.Messages))
		})
	}
}
