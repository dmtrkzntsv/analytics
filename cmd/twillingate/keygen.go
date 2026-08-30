package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

func init() {
	commands["keygen"] = runKeygen
}

// runKeygen prints ingest keys plus a ready-to-paste snippet.
//
// Keys are public by design — they ship inside app binaries and page source
// — so their job is revocation and project identification, not secrecy. 128
// bits of entropy makes guessing infeasible regardless, which is also why
// returning 401 for a bad key leaks nothing useful.
func runKeygen(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(stdout)
	n := fs.Int("n", 1, "number of keys to generate")
	mcp := fs.Bool("mcp", false, "mint the MCP access token instead")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *mcp {
		tok, err := manage.MintMCPToken()
		if err != nil {
			fmt.Fprintf(stdout, "keygen: entropy: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Add to analytics.env:")
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "  MCP_TOKEN=%s\n", tok)
		return 0
	}

	if *n < 1 {
		fmt.Fprintln(stdout, "keygen: -n must be at least 1")
		return 2
	}

	keys := make([]string, *n)
	for i := range keys {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			fmt.Fprintf(stdout, "keygen: entropy: %v\n", err)
			return 1
		}
		keys[i] = "ak_" + hex.EncodeToString(buf)
	}

	fmt.Fprintln(stdout, "note: prefer 'twillingate key issue', which registers the key as it mints it")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Add to projects.json:")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, `  "ingest_keys": [`)
	for i, k := range keys {
		comma := ","
		if i == len(keys)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    { \"key\": %q, \"label\": \"client-%d\" }%s\n", k, i+1, comma)
	}
	fmt.Fprintln(stdout, "  ]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Web snippet:")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, `  <script defer src="https://analytics.example.com/js/script.js"`)
	fmt.Fprintf(stdout, "          data-key=%q\n", keys[0])
	fmt.Fprintln(stdout, `          data-identity="anonymous"></script>`)
	return 0
}
