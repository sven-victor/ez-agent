package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/run"
)

func runOnce(ctx context.Context, a *agent.Agent, prompt string) error {
	fmt.Printf("you> %s\n", prompt)
	return streamTurn(ctx, a, prompt)
}

func runInteractive(ctx context.Context, a *agent.Agent, toolNames []string) error {
	fmt.Println("Interactive chat. Type /help for commands, /quit to exit.")
	if len(toolNames) > 0 {
		fmt.Printf("Tools: %s\n", strings.Join(toolNames, ", "))
	}
	fmt.Println()

	in := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			return nil
		default:
		}

		fmt.Print("you> ")
		line, err := in.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println()
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch {
		case line == "/quit" || line == "/exit" || line == "/q":
			return nil
		case line == "/help" || line == "/?":
			printHelp()
			continue
		case line == "/tools":
			if len(toolNames) == 0 {
				fmt.Println("(no tools registered)")
			} else {
				for _, name := range toolNames {
					fmt.Println(" -", name)
				}
			}
			continue
		case strings.HasPrefix(line, "/"):
			fmt.Printf("unknown command %q — try /help\n", line)
			continue
		}

		if err := streamTurn(ctx, a, line); err != nil {
			if ctx.Err() != nil {
				fmt.Println()
				return nil
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		fmt.Println()
	}
}

func streamTurn(ctx context.Context, a *agent.Agent, prompt string) error {
	var result *run.Result
	var printed bool

	for ev := range a.RunStream(ctx, run.Input{Text: prompt}) {
		switch e := ev.(type) {
		case event.ModelDelta:
			if !printed {
				fmt.Print("assistant> ")
				printed = true
			}
			fmt.Print(e.TextDelta)
		case event.ToolStarted:
			if printed {
				fmt.Println()
				printed = false
			}
			fmt.Printf("  [tool] %s(%s)\n", e.Call.Name, compactJSON(e.Call.Input))
		case event.ToolFinished:
			status := "ok"
			if !e.Result.OK {
				status = "err"
			}
			fmt.Printf("  [done] %s [%s] %s\n", e.Call.Name, status, truncate(partsText(e.Result.Content), 120))
		case event.RunFinished:
			result = e.Result
		case event.RunError:
			if printed {
				fmt.Println()
			}
			return e.Err
		case event.Cancelled:
			if printed {
				fmt.Println()
			}
			return ctx.Err()
		}
	}

	if printed {
		fmt.Println()
	}
	if result == nil {
		return fmt.Errorf("run produced no result")
	}
	return nil
}

func printHelp() {
	fmt.Println(`Commands:
  /help, /?     show this help
  /tools        list available tools
  /quit, /exit  leave the chat

Anything else is sent to the assistant.`)
}

func partsText(parts []message.Part) string {
	return message.TextContent(message.Message{Parts: parts})
}

func compactJSON(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return "{}"
	}
	return truncate(s, 80)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
