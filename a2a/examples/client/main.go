package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	eza2a "github.com/sven-victor/ez-agent/a2a"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/tool"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "Agent Card base URL")
	prompt := flag.String("prompt", "hello", "prompt to send")
	flag.Parse()

	ctx := context.Background()
	c, err := eza2a.Dial(ctx, *url)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	specs := c.Specs()
	if len(specs) == 0 {
		log.Fatal("remote agent advertised no skills")
	}
	fmt.Fprintf(os.Stderr, "skills:")
	for _, s := range specs {
		fmt.Fprintf(os.Stderr, " %s", s.Name)
	}
	fmt.Fprintln(os.Stderr)

	h, ok := c.Lookup(specs[0].Name)
	if !ok {
		log.Fatalf("lookup %s", specs[0].Name)
	}
	res, err := h(ctx, tool.Call{
		ID:    "cli",
		Name:  specs[0].Name,
		Input: []byte(fmt.Sprintf(`{"prompt":%q}`, *prompt)),
	})
	if err != nil {
		log.Fatal(err)
	}
	if !res.OK {
		log.Fatalf("remote error: %s", message.TextContent(message.Message{Parts: res.Content}))
	}
	fmt.Println(message.TextContent(message.Message{Parts: res.Content}))
}
