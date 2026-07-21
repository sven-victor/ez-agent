package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/tool"
	ezmcp "github.com/sven-victor/ez-agent/tool/mcp"
)

func TestMCPInProcessEcho(t *testing.T) {
	srv := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(true))
	srv.AddTool(mcp.NewTool("echo",
		mcp.WithDescription("echo text"),
		mcp.WithString("text", mcp.Required(), mcp.Description("text to echo")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text := req.GetString("text", "")
		return mcp.NewToolResultText(text), nil
	})

	inner, err := mcpclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ezmcp.DialInProcess(context.Background(), inner, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	specs := client.Specs()
	if len(specs) != 1 || specs[0].Name != "echo" {
		t.Fatalf("specs=%v", specs)
	}
	h, ok := client.Lookup("echo")
	if !ok {
		t.Fatal("lookup echo")
	}
	res, err := h(context.Background(), tool.Call{
		ID:    "c1",
		Name:  "echo",
		Input: json.RawMessage(`{"text":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("not ok: %#v", res)
	}
	if message.TextContent(message.Message{Parts: res.Content}) != "hi" {
		t.Fatalf("content=%v", res.Content)
	}
}

func TestCompose(t *testing.T) {
	a := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "a", Description: "a", InputSchema: json.RawMessage(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	})
	b := tool.NewRegistry(tool.Registration{
		Spec: tool.Spec{Name: "b", Description: "b", InputSchema: json.RawMessage(`{}`)},
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ToolUseID: call.ID, OK: true}, nil
		},
	})
	merged := tool.Compose(a, b)
	if len(merged.Specs()) != 2 {
		t.Fatalf("specs=%d", len(merged.Specs()))
	}
	if _, ok := merged.Lookup("a"); !ok {
		t.Fatal("missing a")
	}
	if _, ok := merged.Lookup("b"); !ok {
		t.Fatal("missing b")
	}
}
