package dashboards

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStampSeesTheWAL(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "a.db")
	if err := os.WriteFile(db, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := stamp(db)
	if err := os.WriteFile(db+"-wal", []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := stamp(db); after == before {
		t.Error("stamp ignored -wal; writes between checkpoints would be invisible")
	}
}

func TestStampSeesAGrowingDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "a.db")
	os.WriteFile(db, []byte("x"), 0o644)
	before := stamp(db)
	os.WriteFile(db, []byte("xx"), 0o644)
	if stamp(db) == before {
		t.Error("stamp ignored a size change")
	}
}

func TestStampOfAMissingDatabaseIsStable(t *testing.T) {
	a, b := stamp("/nonexistent/x.db"), stamp("/nonexistent/x.db")
	if a != b {
		t.Errorf("stamp is not stable for a missing file: %q vs %q", a, b)
	}
}

func TestRunBuildsAndServes(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	cfg := b.cfg
	cfg.Addr = freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, b.log) }()

	if !eventually(t, func() bool {
		resp, err := http.Get("http://" + cfg.Addr + "/")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatal("dashboards never served a build")
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestTickSkipsAnUnchangedDatabase(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	// Interval 0 would allow a rebuild on every tick; only the unchanged
	// database can prevent one here.
	b.cfg.Interval = 0
	r := &rebuilder{b: b}

	if !r.tick(context.Background()) {
		t.Fatal("the first tick must build")
	}
	if r.tick(context.Background()) {
		t.Error("rebuilt an unchanged database")
	}
}

func TestTickRebuildsAfterTheDatabaseChanges(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	b.cfg.Interval = 0
	r := &rebuilder{b: b}
	r.tick(context.Background())

	touch(t, b.cfg.DBPath)
	if !r.tick(context.Background()) {
		t.Error("a changed database did not trigger a rebuild")
	}
}

func TestTickHonoursTheInterval(t *testing.T) {
	stubNPM(t, false)
	b := newTestBuilder(t)
	b.cfg.Interval = time.Hour
	r := &rebuilder{b: b}
	r.tick(context.Background())

	touch(t, b.cfg.DBPath)
	if r.tick(context.Background()) {
		t.Error("rebuilt inside DASHBOARDS_INTERVAL")
	}
}

func TestTickReportsAFailedBuild(t *testing.T) {
	stubNPM(t, true)
	b := newTestBuilder(t)
	r := &rebuilder{b: b}
	if r.tick(context.Background()) {
		t.Error("a failing npm must not count as a build")
	}
	if r.built != "" {
		t.Error("a failed build must not record a fingerprint, or the retry never happens")
	}
}

// touch makes the database look modified without depending on filesystem
// timestamp resolution.
func touch(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
}

// freeAddr reserves a port and releases it: Run takes an address, not a
// listener, so the test has to name one that is free.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func eventually(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
