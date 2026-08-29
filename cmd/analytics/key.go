package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dmitry/analytics/internal/manage"
)

func init() { commands["key"] = cmdKey }

func cmdKey(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("key", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, "usage: analytics key <issue|list|disable|enable> [flags]")
		return 2
	}
	ops, cfg, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	sub, subArgs := rest[0], rest[1:]
	sf := flag.NewFlagSet("key "+sub, flag.ContinueOnError)
	sf.SetOutput(stdout)
	project := sf.String("project", "", "project alias (required)")
	label := sf.String("label", "", "key label")
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	switch sub {
	case "issue":
		key, err := ops.IssueIngestKey(ctx, "cli", *project, *label)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		p := ops.Reg.Snapshot(ctx).Project(*project)
		if p == nil {
			fmt.Fprintf(stdout, "issued %s (label %q) but project %q vanished before the snippet could be built; run `analytics key list` to confirm\n", key, *label, *project)
			return 1
		}
		fmt.Fprintf(stdout, "issued %s (label %q)\n\nWeb snippet:\n\n%s\n",
			key, *label, manage.Snippet(cfg.PublicURL, key, p.Identity))
		if cfg.PublicURL == "" {
			fmt.Fprintln(stdout, "\nnote: PUBLIC_URL is not set; replace "+manage.SnippetPlaceholderBase+" with your collector URL")
		}
		return 0
	case "list":
		// list reads the raw rows so disabled keys are visible; the
		// snapshot deliberately drops them.
		_, ks, err := ops.St.LoadRegistry(ctx)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		for _, k := range ks {
			if *project != "" && k.Project != *project {
				continue
			}
			state := "active"
			if k.Disabled {
				state = "disabled"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", k.Project, k.Label, k.Key, state)
		}
		return 0
	case "disable", "enable":
		var err error
		if sub == "disable" {
			err = ops.DisableIngestKey(ctx, "cli", *project, *label)
		} else {
			err = ops.EnableIngestKey(ctx, "cli", *project, *label)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "key %s/%s %sd\n", *project, *label, sub)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\nusage: analytics key <issue|list|disable|enable> [flags]\n", sub)
		return 2
	}
}
