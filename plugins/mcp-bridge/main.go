package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
)

func main() {
	p := &Plugin{}

	// The plugin keeps one MCP session per conversation open between requests,
	// so a server it talked to is left holding a stream until it is closed:
	// shutting down has to close them, not just exit.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		p.Close()
		os.Exit(0)
	}()

	pluginsdk.ServeWithUI(p, "mcp-bridge", newUIHandler(p.oauthClient()))
}
