package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dbPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	body := fmt.Sprintf(`{
		"database": "sqlite://%s",
		"projects": [{"alias": "app", "name": "App", "allowed_origins": ["https://app.com"]}]
	}`, dbPath)
	if !json.Valid([]byte(body)) {
		t.Fatal("test fixture is not valid JSON")
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigrateSubcommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	var out bytes.Buffer
	if code := run([]string{"migrate", "-config", writeConfig(t, dbPath)}, &out); code != 0 {
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
	if code := run([]string{"migrate", "-config", writeConfig(t, dbPath)}, &out); code != 0 {
		t.Fatalf("second migrate exit = %d (%s)", code, out.String())
	}
}

// A missing or unusable config must fail loudly rather than starting with
// defaults.
func TestSubcommandsRejectBadConfig(t *testing.T) {
	for _, cmd := range []string{"serve", "migrate"} {
		t.Run(cmd+" missing config", func(t *testing.T) {
			var out bytes.Buffer
			if code := run([]string{cmd, "-config", "/nonexistent/config.json"}, &out); code != 1 {
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

func TestMigrateRejectsBadDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
		"database": "bogus://nope",
		"projects": [{"alias": "app", "name": "App", "allowed_origins": ["https://app.com"]}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"migrate", "-config", path}, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1 (%s)", code, out.String())
	}
}
