// Package main shows how to build a CLIProxyAPI binary with Langfuse
// observability enabled.
//
// Every upstream request is forwarded to Langfuse as a generation span.
// When an upstream gateway sets X-Request-Id the span is attached to the
// parent trace; otherwise a fresh trace is created.
//
// Required environment variables:
//
//	LANGFUSE_BASE_URL   e.g. https://cloud.langfuse.com
//	LANGFUSE_PUBLIC_KEY pk-lf-...
//	LANGFUSE_SECRET_KEY sk-lf-...
//
// Build and run:
//
//	go build -o cpa-langfuse ./examples/langfuse-plugin
//	LANGFUSE_BASE_URL=https://cloud.langfuse.com \
//	LANGFUSE_PUBLIC_KEY=pk-lf-... \
//	LANGFUSE_SECRET_KEY=sk-lf-... \
//	./cpa-langfuse -config config.yaml
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/examples/langfuse-plugin/langfuse"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

func main() {
	if err := run(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Register the Langfuse plugin if credentials are present in the environment.
	// No-op when env vars are not set, so the binary is safe to run without them.
	if lf := langfuse.NewFromEnv(); lf != nil {
		coreusage.RegisterPlugin(lf)
		log.Info("langfuse plugin: registered")
	} else {
		log.Info("langfuse plugin: skipped (LANGFUSE_BASE_URL / PUBLIC_KEY / SECRET_KEY not set)")
	}

	svc, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(*configPath).
		Build()
	if err != nil {
		return fmt.Errorf("failed to build service: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err = svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("service error: %w", err)
	}
	return nil
}
