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
	"github.com/dmitry/analytics/internal/synccmd"
)

func init() {
	commands["sync"] = cmdSync
}

func cmdSync(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stdout)
	cfgPath := fs.String("config", "/etc/analytics/config.json", "path to config.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	logger := app.NewLogger(cfg.Log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := synccmd.Run(ctx, cfg.Sync, cfg.Database, logger); err != nil {
		logger.Error("sync failed", "error", err)
		return 1
	}
	return 0
}
