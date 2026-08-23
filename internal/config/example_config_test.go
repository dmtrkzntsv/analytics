package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// The shipped examples must stay loadable; a typo here breaks every fresh
// install, since install.sh copies them to /etc/analytics/. The env example
// is parsed the way systemd's EnvironmentFile= reads it: KEY=VALUE lines,
// with commented lines documenting defaults — those are uncommented here so
// every documented key round-trips through FromEnv.
func TestExamplesLoad(t *testing.T) {
	f, err := os.Open("../../deploy/analytics.example.env")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	vars := map[string]string{"PROJECTS_FILE": "../../deploy/projects.example.json"}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "#")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(k, " \t") || k == "PROJECTS_FILE" {
			continue // prose comment, not a documented default
		}
		vars[k] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"DATABASE_URL", "LISTEN_ADDR", "BUFFER_FLUSH_INTERVAL", "SYNC_INTERVAL"} {
		if _, ok := vars[key]; !ok {
			t.Fatalf("example env must document %s", key)
		}
	}
	cfg, err := FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Alias != "myapp" {
		t.Errorf("projects = %+v", cfg.Projects)
	}
	if cfg.Sync.Interval.Minutes() != 5 {
		t.Errorf("sync interval = %v", cfg.Sync.Interval)
	}
	if cfg.Retention.Product.RawDays != 30 || cfg.Retention.Web.RawDays != 7 {
		t.Errorf("retention = %+v", cfg.Retention)
	}
}
