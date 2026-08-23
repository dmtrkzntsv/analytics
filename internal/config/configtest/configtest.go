// Package configtest builds config.Config values for tests without touching
// the process environment: env overrides come from a map, projects from an
// inline JSON array written to a temp file.
package configtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dmitry/analytics/internal/config"
)

// Load returns a validated config. vars may override any documented env
// variable; DATABASE_URL defaults to an unused sqlite DSN so callers that
// never open the store can omit it.
func Load(t testing.TB, vars map[string]string, projectsJSON string) *config.Config {
	t.Helper()
	m := map[string]string{"DATABASE_URL": "sqlite:///unused"}
	for k, v := range vars {
		m[k] = v
	}
	if _, ok := m["PROJECTS_FILE"]; !ok {
		path := filepath.Join(t.TempDir(), "projects.json")
		if err := os.WriteFile(path, []byte(projectsJSON), 0o644); err != nil {
			t.Fatal(err)
		}
		m["PROJECTS_FILE"] = path
	}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
