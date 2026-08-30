package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

func init() { commands["project"] = cmdProject }

// envFileLookup overlays KEY=VALUE lines from path under the real
// environment: real env wins, matching how EnvironmentFile= behaves.
func envFileLookup(path string) (func(string) (string, bool), error) {
	fromFile := map[string]string{}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				fromFile[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return v, true
		}
		v, ok := fromFile[key]
		return v, ok
	}, nil
}

// openOps opens the store named by DATABASE_DSN (optionally via
// -env-file), migrates, and returns the management frontend. The CLI
// talks to the database directly — break-glass by design (spec §7.1).
func openOps(stdout io.Writer, envFile string) (*manage.Ops, *config.Config, func(), int) {
	lookup, err := envFileLookup(envFile)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	cfg, err := config.FromEnv(lookup)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	reg := manage.New(st, cfg.Retention, app.NewLogger(cfg.Log))
	if err := reg.Reload(ctx); err != nil {
		st.Close()
		fmt.Fprintln(stdout, err)
		return nil, nil, nil, 1
	}
	return manage.NewOps(reg, st), cfg, func() { st.Close() }, 0
}

func cmdProject(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, "usage: twillingate project <create|update|list|archive|restore|delete> [flags]")
		return 2
	}
	ops, _, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "create", "update":
		sf := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
		sf.SetOutput(stdout)
		alias := sf.String("alias", "", "project alias (required)")
		name := sf.String("name", "", "display name (defaults to alias)")
		identity := sf.String("identity", "anonymous", "anonymous|identified")
		var origins multiFlag
		sf.Var(&origins, "origin", "allowed origin, `*` wildcards accepted (repeatable)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}

		spec := manage.ProjectSpec{Alias: *alias}

		if sub == "create" {
			// For create, use the provided flags or defaults
			spec.Name = *name
			spec.Identity = *identity
			spec.AllowedOrigins = origins
		} else {
			// For update, start from current values and overlay only explicitly-set flags
			snap := ops.Reg.Snapshot(ctx)
			current := snap.Project(*alias)
			if current == nil {
				fmt.Fprintf(stdout, "project %q not found\n", *alias)
				return 1
			}
			// Start from current values
			spec.Name = current.Name
			spec.Identity = current.Identity
			spec.AllowedOrigins = current.AllowedOrigins
			spec.Retention = current.Retention
			spec.Aggregation = current.Aggregation

			// Overlay explicitly-set flags using sf.Visit
			sf.Visit(func(f *flag.Flag) {
				switch f.Name {
				case "name":
					spec.Name = *name
				case "identity":
					spec.Identity = *identity
				case "origin":
					// If -origin was passed at all, replace the whole list
					spec.AllowedOrigins = origins
				}
			})
		}

		var p *manage.Project
		var err error
		if sub == "create" {
			p, err = ops.CreateProject(ctx, "cli", spec)
		} else {
			p, err = ops.UpdateProject(ctx, "cli", spec)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %q %sd\n", p.Alias, sub)
		fmt.Fprintln(stdout, "next: twillingate key issue -project", p.Alias, "-label web")
		return 0
	case "list":
		s := ops.Reg.Snapshot(ctx)
		for _, p := range s.Projects() {
			state := ""
			if p.Archived {
				state = "  (archived)"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s%s\n", p.Alias, p.Identity, p.Name, state)
		}
		return 0
	case "archive", "restore":
		sf := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
		sf.SetOutput(stdout)
		alias := sf.String("alias", "", "project alias (required)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		var err error
		if sub == "archive" {
			err = ops.ArchiveProject(ctx, "cli", *alias)
		} else {
			err = ops.RestoreProject(ctx, "cli", *alias)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %q %sd\n", *alias, sub)
		return 0
	case "delete":
		sf := flag.NewFlagSet("project delete", flag.ContinueOnError)
		sf.SetOutput(stdout)
		alias := sf.String("alias", "", "project alias (required)")
		force := sf.Bool("force", false, "skip confirmation")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if !*force {
			fmt.Fprintf(stdout, "This permanently deletes project %q and ALL its data.\n", *alias)
			fmt.Fprintln(stdout, "Re-run with -force to confirm.")
			return 1
		}
		if err := ops.DeleteProject(ctx, "cli", *alias); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %q deleted\n", *alias)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\nusage: twillingate project <create|update|list|archive|restore|delete> [flags]\n", sub)
		return 2
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
