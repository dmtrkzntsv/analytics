package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmitry/analytics/internal/app"
	"github.com/dmitry/analytics/internal/config"
)

func init() {
	commands["serve"] = cmdServe
}

func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	api := fs.Bool("api", false, "run the ingestion API")
	mcpFlag := fs.Bool("mcp", false, "run the MCP endpoint")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*api && !*mcpFlag {
		fmt.Fprintln(stdout, "serve: specify at least one surface: -api (ingestion), -mcp (MCP endpoint)")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if *mcpFlag {
		if err := cfg.ValidateMCP(); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
	}
	logger := app.NewLogger(cfg.Log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, cfg, logger, *api, *mcpFlag); err != nil {
		logger.Error("serve failed", "error", err)
		return 1
	}
	return 0
}
