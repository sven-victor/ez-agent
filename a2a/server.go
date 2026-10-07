package a2a

import (
	"net/http"

	protocol "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/sven-victor/ez-agent/agent"
)

// Handler returns an http.Handler serving JSON-RPC at /invoke and the Agent Card
// at /.well-known/agent-card.json.
func Handler(a *agent.Agent, card *protocol.AgentCard) http.Handler {
	if card == nil {
		card = NewJSONRPCCard("ez-agent", "ez-agent A2A adapter", "/invoke")
	}
	mux := http.NewServeMux()
	req := a2asrv.NewHandler(NewExecutor(a))
	mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(req))
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	return mux
}

// ListenAndServe starts an HTTP server on addr with Handler(a, card).
func ListenAndServe(addr string, a *agent.Agent, card *protocol.AgentCard) error {
	return http.ListenAndServe(addr, Handler(a, card))
}

// NewJSONRPCCard builds a minimal Agent Card for a JSON-RPC interface at invokeURL
// (absolute URL in production, e.g. http://127.0.0.1:8080/invoke).
func NewJSONRPCCard(name, description, invokeURL string, skills ...protocol.AgentSkill) *protocol.AgentCard {
	if name == "" {
		name = "ez-agent"
	}
	if invokeURL == "" {
		invokeURL = "/invoke"
	}
	if len(skills) == 0 {
		skills = []protocol.AgentSkill{{
			ID:          "chat",
			Name:        "chat",
			Description: "Send a natural-language prompt to the agent.",
			Tags:        []string{"chat"},
		}}
	}
	return &protocol.AgentCard{
		Name:        name,
		Description: description,
		Version:     "0.1.0",
		SupportedInterfaces: []*protocol.AgentInterface{
			protocol.NewAgentInterface(invokeURL, protocol.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills:             skills,
	}
}
