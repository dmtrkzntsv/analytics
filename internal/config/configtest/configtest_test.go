package configtest

import "testing"

func TestLoad(t *testing.T) {
	cfg := Load(t, map[string]string{"LISTEN_ADDR": "127.0.0.1:1"},
		`[{"alias": "a", "name": "A", "ingest_keys": [{"key": "ak_a", "label": "web"}], "allowed_origins": ["https://a.com"]}]`)
	if cfg.Listen != "127.0.0.1:1" || cfg.Project("a") == nil {
		t.Errorf("cfg = %+v", cfg)
	}
}
