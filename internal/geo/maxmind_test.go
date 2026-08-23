package geo

import (
	"context"
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
	downloadDB = func(ctx context.Context, url, dest string) error {
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
	downloadDB = func(ctx context.Context, url, dest string) error { called = true; return nil }
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

// TestDownloadDBRedactsLicenseKey exercises the real downloadDB (not a test
// stub) against an address nothing listens on, so it fails at the
// transport level. http.Get / *url.Error normally embed the full request
// URL, including the license_key query param, in their error string; the
// license key must never leak into a returned error (which may be logged).
func TestDownloadDBRedactsLicenseKey(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.mmdb")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := downloadDB(ctx, "http://127.0.0.1:1/x?license_key=SECRET123", dest)
	if err == nil {
		t.Fatal("expected a transport error connecting to a closed port")
	}
	if strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("error must not leak the license key, got %q", err.Error())
	}
}
