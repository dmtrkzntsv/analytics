package geo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a concurrency-safe io.Writer, needed because refreshLoop
// logs from a background goroutine while the test reads the buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// buildTarGz packs name=content into a tar.gz byte slice, mirroring the
// archive shape MaxMind ships (a single .mmdb member inside a tarball).
func buildTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if name != "" {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// --- sanitizeTransportErr ---

func TestSanitizeTransportErrPassesThroughOtherErrors(t *testing.T) {
	plain := errors.New("some non-url error")
	if got := sanitizeTransportErr(plain); got != plain {
		t.Errorf("sanitizeTransportErr(%v) = %v, want the same error unchanged", plain, got)
	}
}

// --- downloadDB, exercised against a real HTTP server (not the test stub) ---

func TestDownloadDBSuccessExtractsMmdb(t *testing.T) {
	mmdbBytes, err := os.ReadFile("testdata/GeoIP2-Country-Test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	archive := buildTarGz(t, "GeoLite2-Country.mmdb", mmdbBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mmdb")
	if err := downloadDB(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("downloadDB = %v, want nil", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, mmdbBytes) {
		t.Error("extracted file content does not match the archived .mmdb member")
	}
}

func TestDownloadDBFailsOnNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mmdb")
	err := downloadDB(context.Background(), srv.URL, dest)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want mention of HTTP 500", err)
	}
}

func TestDownloadDBFailsOnBadGzip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not a gzip stream"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mmdb")
	if err := downloadDB(context.Background(), srv.URL, dest); err == nil {
		t.Error("want error reading a non-gzip body as gzip")
	}
}

func TestDownloadDBFailsWhenArchiveHasNoMmdb(t *testing.T) {
	archive := buildTarGz(t, "README.txt", []byte("nothing to see here"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mmdb")
	err := downloadDB(context.Background(), srv.URL, dest)
	if err == nil || !strings.Contains(err.Error(), "no .mmdb in archive") {
		t.Errorf("err = %v, want mention of no .mmdb in archive", err)
	}
}

func TestDownloadDBFailsOnMalformedURL(t *testing.T) {
	// A raw control byte in the URL makes http.NewRequestWithContext itself
	// fail, before any network I/O happens.
	dest := filepath.Join(t.TempDir(), "out.mmdb")
	err := downloadDB(context.Background(), "http://\x7f/x", dest)
	if err == nil || !strings.Contains(err.Error(), "download request failed") {
		t.Errorf("err = %v, want mention of download request failed", err)
	}
}

func TestDownloadDBFailsWhenDestDirMissing(t *testing.T) {
	mmdbBytes, err := os.ReadFile("testdata/GeoIP2-Country-Test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	archive := buildTarGz(t, "GeoLite2-Country.mmdb", mmdbBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "no-such-dir", "out.mmdb")
	if err := downloadDB(context.Background(), srv.URL, dest); err == nil {
		t.Error("want error creating the temp file in a missing directory")
	}
}

// --- newMaxmind / ensureFresh ---

func TestNewMaxmindRequiresLicenseKey(t *testing.T) {
	if _, err := New("maxmind://", t.TempDir(), slog.Default()); err == nil {
		t.Fatal("empty license key must error")
	}
}

func TestNewMaxmindFailsWhenInitialDownloadFails(t *testing.T) {
	dir := t.TempDir()
	old := downloadDB
	downloadDB = func(ctx context.Context, url, dest string) error { return errors.New("network down") }
	defer func() { downloadDB = old }()

	_, err := New("maxmind://KEY", dir, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "initial GeoLite2 download failed") {
		t.Errorf("err = %v, want mention of initial GeoLite2 download failed", err)
	}
}

func TestNewMaxmindFailsWhenDownloadedFileIsNotAnMmdb(t *testing.T) {
	dir := t.TempDir()
	old := downloadDB
	downloadDB = func(ctx context.Context, url, dest string) error {
		return os.WriteFile(dest, []byte("not an mmdb file"), 0o644)
	}
	defer func() { downloadDB = old }()

	_, err := New("maxmind://KEY", dir, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "open mmdb") {
		t.Errorf("err = %v, want mention of open mmdb", err)
	}
}

func TestEnsureFreshFallsBackToStaleDBWhenRefreshFails(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	path := filepath.Join(dir, "GeoLite2-Country.mmdb")
	stale := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	old := downloadDB
	downloadDB = func(ctx context.Context, url, dest string) error { return errors.New("refresh unreachable") }
	defer func() { downloadDB = old }()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	p, err := New("maxmind://k", dir, logger)
	if err != nil {
		t.Fatalf("New = %v, want nil (stale db must still be usable)", err)
	}
	defer p.Close()
	if !strings.Contains(buf.String(), "refresh failed, keeping stale db") {
		t.Errorf("log output = %q, want mention of keeping stale db", buf.String())
	}
	// The stale db must still serve lookups.
	r := httptest.NewRequest("POST", "/api/hit", nil)
	if got := p.Country(r, "2.125.160.216"); got != "GB" {
		t.Errorf("lookup on stale db = %q, want GB", got)
	}
}

// --- refreshLoop, driven by shrinking the package-level ticker interval ---

// TestRefreshLoopLogsFailureAndKeepsServing drives refreshLoop's own
// "weekly refresh failed" branch. That branch only fires when ensureFresh
// returns a genuine (non-nil) error, which per ensureFresh only happens
// when the db file is entirely missing at check time AND the download
// fails too — a stale-but-present file makes ensureFresh swallow the
// download error and log its own "keeping stale db" message instead
// (covered by TestEnsureFreshFallsBackToStaleDBWhenRefreshFails). So this
// test starts from a working provider, then deletes the on-disk file out
// from under it before the next tick, forcing that fatal path.
func TestRefreshLoopLogsFailureAndKeepsServing(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	path := filepath.Join(dir, "GeoLite2-Country.mmdb")

	oldInterval := refreshInterval
	refreshInterval = 5 * time.Millisecond
	defer func() { refreshInterval = oldInterval }()

	oldDL := downloadDB
	downloadDB = func(ctx context.Context, url, dest string) error {
		return errors.New("refresh unreachable")
	}
	defer func() { downloadDB = oldDL }()

	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	p, err := New("maxmind://k", dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	mm := p.(*maxmind)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for !strings.Contains(buf.String(), "weekly refresh failed") {
		select {
		case <-deadline:
			t.Fatalf("log output = %q, want mention of weekly refresh failed", buf.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	// The provider must keep serving lookups from the reader it already
	// had in memory: a failed refresh must not tear that down.
	r := httptest.NewRequest("POST", "/api/hit", nil)
	if got := p.Country(r, "2.125.160.216"); got != "GB" {
		t.Errorf("lookup after a failed refresh = %q, want GB", got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	// Wait for the background goroutine to actually stop reading the
	// downloadDB/refreshInterval package vars before this test's defers
	// restore them out from under it.
	select {
	case <-mm.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshLoop did not stop after Close")
	}
}

// TestRefreshLoopReloadsReaderOnSuccess drives the reopen-and-swap branch
// that runs after every successful ensureFresh, including the common case
// where the file is already fresh so ensureFresh is a same-file no-op: the
// loop still reopens and swaps the reader on each tick.
func TestRefreshLoopReloadsReaderOnSuccess(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir) // fresh mtime: ensureFresh will not call downloadDB

	oldInterval := refreshInterval
	refreshInterval = 5 * time.Millisecond
	defer func() { refreshInterval = oldInterval }()

	p, err := New("maxmind://k", dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Let several ticks elapse so the reopen-and-swap branch runs a few
	// times, then confirm lookups still work off the reloaded reader.
	time.Sleep(50 * time.Millisecond)
	r := httptest.NewRequest("POST", "/api/hit", nil)
	if got := p.Country(r, "2.125.160.216"); got != "GB" {
		t.Errorf("lookup after reload ticks = %q, want GB", got)
	}
}

// Close on a maxmind whose reader was never assigned (reachable in-package
// even though newMaxmind never returns such a value to callers, since a
// construction failure returns a nil Provider instead) must not panic and
// must report success.
func TestMaxmindCloseWithNilReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &maxmind{ctx: ctx, cancel: cancel, stopped: make(chan struct{})}
	close(m.stopped)
	if err := m.Close(); err != nil {
		t.Errorf("Close() with no reader = %v, want nil", err)
	}
}
