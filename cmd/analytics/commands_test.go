package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setEnv points the process environment at a scratch database; commands
// read config from the environment only. The project list lives in the
// registry (that database), not in an env-named file.
func setEnv(t *testing.T, dbPath string) {
	t.Helper()
	t.Setenv("DATABASE_URL", "sqlite://"+dbPath)
}

func TestMigrateSubcommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	setEnv(t, dbPath)
	var out bytes.Buffer
	if code := run([]string{"migrate"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, out.String())
	}
	if !strings.Contains(out.String(), "migrations applied") {
		t.Errorf("output = %q", out.String())
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("database not created: %v", err)
	}
	// Re-running must stay clean.
	out.Reset()
	if code := run([]string{"migrate"}, &out); code != 0 {
		t.Fatalf("second migrate exit = %d (%s)", code, out.String())
	}
}

// A missing or unusable configuration must fail loudly rather than starting
// with defaults. serve needs a surface flag to get past the usage check and
// reach config loading at all.
func TestSubcommandsRejectBadConfig(t *testing.T) {
	argsFor := map[string][]string{"serve": {"serve", "-api"}, "migrate": {"migrate"}}
	for _, cmd := range []string{"serve", "migrate"} {
		t.Run(cmd+" missing DATABASE_URL", func(t *testing.T) {
			t.Setenv("DATABASE_URL", "")
			var out bytes.Buffer
			if code := run(argsFor[cmd], &out); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if out.Len() == 0 {
				t.Error("want an error message on stdout")
			}
		})
		t.Run(cmd+" bad flag", func(t *testing.T) {
			var out bytes.Buffer
			if code := run([]string{cmd, "-nope"}, &out); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
		})
	}
}

func TestBareServeIsUsageError(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"serve"}, &out)
	if code == 0 {
		t.Fatal("bare serve did not fail")
	}
	want := "specify at least one surface: -api (ingestion), -mcp (MCP endpoint)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output %q missing %q", out.String(), want)
	}
}

func TestMigrateRejectsBadDSN(t *testing.T) {
	setEnv(t, "unused.db")
	t.Setenv("DATABASE_URL", "bogus://nope")
	var out bytes.Buffer
	if code := run([]string{"migrate"}, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1 (%s)", code, out.String())
	}
}

// dashboards renders whatever database it is pointed at; unlike serve and
// migrate it must start without a project list.
func TestDashboardsRejectsMissingDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DASHBOARDS_DB_PATH", "")
	var out bytes.Buffer
	if code := run([]string{"dashboards"}, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "DASHBOARDS_DB_PATH") {
		t.Errorf("error = %q, want it to name the missing setting", out.String())
	}
}

func TestKeygenPrintsUsableKeys(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-n", "2"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "ak_") {
		t.Errorf("output has no ak_ prefixed key:\n%s", s)
	}
	if !strings.Contains(s, "data-key") || !strings.Contains(s, "data-identity") {
		t.Errorf("output should include a ready-to-paste snippet:\n%s", s)
	}
	if !strings.Contains(s, "ingest_keys") {
		t.Errorf("output should include the projects.json fragment:\n%s", s)
	}
	if got := strings.Count(s, `"key":`); got != 2 {
		t.Errorf("asked for 2 keys, got %d:\n%s", got, s)
	}
}

func TestKeygenKeysAreUniqueAndWellFormed(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-n", "8"}, &out); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	seen := map[string]bool{}
	for _, f := range strings.Fields(out.String()) {
		f = strings.Trim(f, `",`)
		if !strings.HasPrefix(f, "ak_") {
			continue
		}
		if seen[f] {
			t.Fatalf("duplicate key %q", f)
		}
		// 16 random bytes hex-encoded, plus the "ak_" prefix.
		if len(f) != 3+32 {
			t.Errorf("key %q has length %d, want %d", f, len(f), 3+32)
		}
		seen[f] = true
	}
	if len(seen) != 8 {
		t.Errorf("got %d distinct keys, want 8", len(seen))
	}
}

func TestKeygenRejectsBadCount(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-n", "0"}, &out); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestKeygenAppearsInUsage(t *testing.T) {
	var out bytes.Buffer
	run(nil, &out)
	if !strings.Contains(out.String(), "keygen") {
		t.Errorf("usage does not mention keygen: %s", out.String())
	}
}
