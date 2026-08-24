// Command analytics is the single binary for the ultra-lite analytics
// system: `serve` (ingestion server), `dashboards` (the Evidence renderer),
// `migrate`, and `version`.
package main

import (
	"fmt"
	"io"
	"os"
)

var version = "dev" // overridden via -ldflags "-X main.version=..."

var commands = map[string]func(args []string, stdout io.Writer) int{}

func init() {
	commands["version"] = func(_ []string, stdout io.Writer) int {
		fmt.Fprintf(stdout, "analytics %s\n", version)
		return 0
	}
}

func run(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, "usage: analytics <serve|dashboards|migrate|version> [flags]")
		return 2
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stdout, "unknown command %q\nusage: analytics <serve|dashboards|migrate|version> [flags]\n", args[0])
		return 2
	}
	return cmd(args[1:], stdout)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
