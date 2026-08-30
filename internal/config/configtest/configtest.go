// Package configtest builds config.Config values for tests without touching
// the process environment: env overrides come from a map. Config is
// infra-only (spec §4/managed-config); a test that needs projects seeds
// them into a registry separately (see internal/manage, or each package's
// own newTestRegistry helper).
package configtest

import (
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// Load returns a validated config. vars may override any documented env
// variable; DATABASE_DSN defaults to an unused sqlite DSN so callers that
// never open the store can omit it.
func Load(t testing.TB, vars map[string]string) *config.Config {
	t.Helper()
	m := map[string]string{"DATABASE_DSN": "sqlite:///unused"}
	for k, v := range vars {
		m[k] = v
	}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
