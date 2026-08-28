package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

func init() { commands["config"] = cmdConfig }

func cmdConfig(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, "usage: analytics config <import FILE|export>")
		return 2
	}
	ops, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	switch rest[0] {
	case "export":
		if err := ops.Export(ctx, stdout); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		return 0
	case "import":
		if len(rest) != 2 {
			fmt.Fprintln(stdout, "usage: analytics config import FILE")
			return 2
		}
		f, err := os.Open(rest[1])
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		defer f.Close()
		res, err := ops.Import(ctx, "cli", f)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "%d created, %d updated, %d keys added\n",
			res.Created, res.Updated, res.KeysAdded)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\nusage: analytics config <import FILE|export>\n", rest[0])
		return 2
	}
}
