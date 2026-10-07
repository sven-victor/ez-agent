package a2a

import (
	"context"
	"iter"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/run"
)

// NewExecutor adapts an ez-agent Agent to a2asrv.AgentExecutor.
// User text is forwarded via Agent.Run; the assistant text is returned as an A2A Message.
func NewExecutor(a *agent.Agent) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[protocol.Event, error] {
		return func(yield func(protocol.Event, error) bool) {
			if a == nil {
				yield(nil, errNilAgent)
				return
			}
			prompt := textFromProtocolMessage(execCtx.Message)
			if prompt == "" {
				prompt = "(empty message)"
			}
			res, err := a.Run(ctx, run.Input{Text: prompt})
			if err != nil {
				yield(nil, err)
				return
			}
			out := lastAssistantText(res)
			if out == "" {
				out = "(empty sub-agent response)"
			}
			yield(protocol.NewMessage(protocol.MessageRoleAgent, protocol.NewTextPart(out)), nil)
		}
	})
}

var errNilAgent = errString("a2a: agent is nil")

type errString string

func (e errString) Error() string { return string(e) }
