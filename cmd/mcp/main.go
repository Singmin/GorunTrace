package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/Singmin/GorunTrace/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8080", "GorunTrace HTTP server")
	flag.Parse()
	server, err := mcpserver.New(*endpoint, os.Getenv("GORUNTRACE_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err = server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
