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
	"github.com/dmitry/analytics/internal/dashboards"
)

func init() {
	commands["dashboards"] = cmdDashboards
}

func cmdDashboards(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("dashboards", flag.ContinueOnError)
	fs.SetOutput(stdout)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.LoadDashboards()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	logger := app.NewLogger(cfg.Log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := dashboards.Run(ctx, cfg.Dashboards, logger); err != nil {
		logger.Error("dashboards failed", "error", err)
		return 1
	}
	return 0
}
