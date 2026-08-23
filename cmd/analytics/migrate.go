package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func init() {
	commands["migrate"] = cmdMigrate
}

func cmdMigrate(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
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
	st, err := store.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintln(stdout, "migrations applied")
	return 0
}
