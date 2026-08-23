package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionSubcommand(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "analytics ") {
		t.Fatalf("output = %q, want prefix 'analytics '", out.String())
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"bogus"}, &out); code == 0 {
		t.Fatal("unknown subcommand must return non-zero")
	}
}

func TestNoSubcommandPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if code := run(nil, &out); code == 0 {
		t.Fatal("missing subcommand must return non-zero")
	}
	if !strings.Contains(out.String(), "usage") {
		t.Fatalf("output %q must contain usage", out.String())
	}
}
