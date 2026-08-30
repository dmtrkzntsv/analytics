package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
)

func init() {
	commands["serve"] = cmdServe
}

// resolveSurfaces maps the -api/-mcp flags to the surfaces to run. Bare
// `serve` runs both, leniently: a surface that cannot start (MCP without
// auth config) is skipped with a warning rather than failing the process.
// Naming a flag is an explicit request, so misconfiguration of a
// requested surface stays a hard error.
func resolveSurfaces(api, mcp bool) (runAPI, runMCP, lenient bool) {
	if !api && !mcp {
		return true, true, true
	}
	return api, mcp, false
}

func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	api := fs.Bool("api", false, "run only the ingestion API")
	mcpFlag := fs.Bool("mcp", false, "run only the MCP endpoint")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	runAPI, runMCP, lenient := resolveSurfaces(*api, *mcpFlag)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	logger := app.NewLogger(cfg.Log)
	if runMCP {
		if err := cfg.ValidateMCP(); err != nil {
			if !lenient {
				fmt.Fprintln(stdout, err)
				return 1
			}
			logger.Warn("MCP endpoint disabled", "reason", err.Error())
			runMCP = false
		}
	}
	// Per operator request: a surface without its own port is worth a
	// warning, never a failure. MCP_ADDR unset means MCP rides the
	// ingestion listener — a supported topology, flagged so a shared
	// port is never a surprise.
	if runAPI && runMCP && cfg.MCP.Addr == cfg.Listen {
		logger.Warn("MCP_ADDR not set; MCP endpoint shares the ingestion listener", "addr", cfg.Listen)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, cfg, logger, runAPI, runMCP); err != nil {
		logger.Error("serve failed", "error", err)
		return 1
	}
	return 0
}
