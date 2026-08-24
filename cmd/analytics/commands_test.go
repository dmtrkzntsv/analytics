package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setEnv points the process environment at a scratch database and a valid
// projects file; commands read config from the environment only.
func setEnv(t *testing.T, dbPath string) {
	t.Helper()
	projects := filepath.Join(t.TempDir(), "projects.json")
	body := `[{"alias": "app", "name": "App", "allowed_origins": ["https://app.com"]}]`
	if err := os.WriteFile(projects, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "sqlite://"+dbPath)
	t.Setenv("PROJECTS_FILE", projects)
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
// with defaults.
func TestSubcommandsRejectBadConfig(t *testing.T) {
	for _, cmd := range []string{"serve", "migrate"} {
		t.Run(cmd+" missing DATABASE_URL", func(t *testing.T) {
			t.Setenv("DATABASE_URL", "")
			var out bytes.Buffer
			if code := run([]string{cmd}, &out); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if out.Len() == 0 {
				t.Error("want an error message on stdout")
			}
		})
		t.Run(cmd+" missing projects file", func(t *testing.T) {
			t.Setenv("DATABASE_URL", "sqlite://"+filepath.Join(t.TempDir(), "a.db"))
			t.Setenv("PROJECTS_FILE", "/nonexistent/projects.json")
			var out bytes.Buffer
			if code := run([]string{cmd}, &out); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
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
