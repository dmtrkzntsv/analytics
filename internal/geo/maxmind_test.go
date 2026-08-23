package geo

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, dataDir string) {
	t.Helper()
	src, err := os.ReadFile("testdata/GeoIP2-Country-Test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "GeoLite2-Country.mmdb"), src, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMaxmindLookup(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	p, err := New("maxmind://testkey", dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r := httptest.NewRequest("POST", "/api/hit", nil)
	if got := p.Country(r, "2.125.160.216"); got != "GB" {
		t.Errorf("GB ip => %q", got)
	}
	if got := p.Country(r, "999.invalid"); got != "" {
		t.Errorf("invalid ip must degrade to empty, got %q", got)
	}
}

func TestMaxmindDownloadsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	called := ""
	old := downloadDB
	downloadDB = func(url, dest string) error {
		called = url
		src, _ := os.ReadFile("testdata/GeoIP2-Country-Test.mmdb")
		return os.WriteFile(dest, src, 0o644)
	}
	defer func() { downloadDB = old }()
	p, err := New("maxmind://KEY123", dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if called == "" || !contains(called, "license_key=KEY123") {
		t.Fatalf("download URL = %q, must contain license key", called)
	}
}

func TestMaxmindStaleTriggersDownload(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	path := filepath.Join(dir, "GeoLite2-Country.mmdb")
	stale := time.Now().Add(-31 * 24 * time.Hour)
	os.Chtimes(path, stale, stale)
	called := false
	old := downloadDB
	downloadDB = func(url, dest string) error { called = true; return nil }
	defer func() { downloadDB = old }()
	p, err := New("maxmind://k", dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !called {
		t.Fatal("stale DB must trigger re-download")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
