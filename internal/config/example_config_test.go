package config

import "testing"

// The shipped example must stay loadable; a typo here breaks every fresh
// install, since install.sh copies it to /etc/analytics/config.json.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load("../../deploy/config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Alias != "myapp" {
		t.Errorf("projects = %+v", cfg.Projects)
	}
	if cfg.Sync.Interval.Duration.Minutes() != 5 {
		t.Errorf("sync interval = %v", cfg.Sync.Interval.Duration)
	}
	if cfg.Retention.Product.RawDays != 30 || cfg.Retention.Web.RawDays != 7 {
		t.Errorf("retention = %+v", cfg.Retention)
	}
}
