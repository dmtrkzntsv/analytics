package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// The shipped .env.example must stay loadable; a typo here breaks every
// fresh install, since install.sh copies it to /etc/analytics/. It is
// parsed the way systemd's EnvironmentFile= reads it: KEY=VALUE lines, with
// commented lines documenting defaults — those are uncommented here so
// every documented key round-trips through FromEnv.
func TestExamplesLoad(t *testing.T) {
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "#")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(k, " \t") {
			continue // prose comment, not a documented default
		}
		vars[k] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"DATABASE_URL", "LISTEN_ADDR", "BUFFER_FLUSH_INTERVAL", "DASHBOARDS_INTERVAL"} {
		if _, ok := vars[key]; !ok {
			t.Fatalf("example env must document %s", key)
		}
	}
	cfg, err := FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboards.Interval.Minutes() != 15 {
		t.Errorf("dashboards interval = %v", cfg.Dashboards.Interval)
	}
	if cfg.Retention.Product.RawDays != 30 || cfg.Retention.Web.RawDays != 7 {
		t.Errorf("retention = %+v", cfg.Retention)
	}
}
