package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/setup"

	// Adapters
	_ "github.com/xolo-gateway/xolo/internal/adapter/memory"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conf, err := config.Parse()
	if err != nil {
		slog.ErrorContext(ctx, "could not parse config", slog.Any("error", errors.WithStack(err)))
		os.Exit(1)
	}

	logger := slog.New(slogx.ContextHandler{
		Handler: slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level:     slog.Level(conf.Logger.Level),
			AddSource: true,
		}),
	})

	slog.SetDefault(logger)

	slog.DebugContext(ctx, "using configuration", slog.Any("config", conf))

	sig := make(chan os.Signal, 1)
	// Docker and systemd stop the process with SIGTERM: without it here the
	// process is killed at the end of the grace period with every in-flight
	// request cut short.
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.InfoContext(ctx, "use ctrl+c to interrupt")
		received := <-sig
		slog.InfoContext(ctx, "signal received, shutting down", slog.String("signal", received.String()))
		cancel()
	}()

	httpServer, err := setup.NewHTTPServerFromConfig(ctx, conf)
	if err != nil {
		slog.ErrorContext(ctx, "could not setup http server", slog.Any("error", errors.WithStack(err)))
		os.Exit(1)
	}

	servers := []namedServer{
		{name: "http", address: conf.HTTP.Address, run: httpServer.Run},
	}

	// Nil when the Provisionning API is disabled: the public server then runs alone.
	provisionningAPIServer, err := setup.NewProvisionningAPIServerFromConfig(ctx, conf)
	if err != nil {
		slog.ErrorContext(ctx, "could not setup provisionning api server", slog.Any("error", errors.WithStack(err)))
		os.Exit(1)
	}
	if provisionningAPIServer != nil {
		servers = append(servers, namedServer{name: "provisionning-api", address: conf.ProvisionningAPI.Address, run: provisionningAPIServer.Run})
	}

	if err := run(ctx, cancel, servers); err != nil {
		os.Exit(1)
	}
}

type namedServer struct {
	name    string
	address string
	run     func(ctx context.Context) error
}

// run starts every server on the shared root context and waits for all of them
// to return. The first failure cancels the context so the others shut down too:
// no fatal error is ever silently ignored.
func run(ctx context.Context, cancel context.CancelFunc, servers []namedServer) error {
	type result struct {
		name string
		err  error
	}

	results := make(chan result, len(servers))

	for _, server := range servers {
		slog.InfoContext(ctx, "starting server", slog.String("server", server.name), slog.String("address", server.address))

		go func() {
			results <- result{name: server.name, err: server.run(ctx)}
		}()
	}

	var failure error

	for range servers {
		res := <-results
		if res.err == nil {
			continue
		}

		slog.ErrorContext(ctx, "server stopped with an error",
			slog.String("server", res.name), slog.Any("error", errors.WithStack(res.err)))

		failure = res.err

		cancel()
	}

	return failure
}
