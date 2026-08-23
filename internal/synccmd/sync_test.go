package synccmd

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

// makeValidDB creates a real migrated sqlite file to act as the "restored"
// artifact litestream would produce.
func makeValidDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return path
}

// stubLitestream replaces the restore exec with a copy of fixture to the -o
// path, or a failing command. It returns a counter of invocations.
func stubLitestream(t *testing.T, fixture string, fail bool) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	old := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls.Add(1)
		if name != "litestream" {
			t.Errorf("exec %q, want litestream", name)
		}
		out := ""
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				out = args[i+1]
			}
		}
		if fail {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "cp", fixture, out)
	}
	t.Cleanup(func() { execCommand = old })
	return &calls
}

func TestRunOnceHappyPath(t *testing.T) {
	fixture := makeValidDB(t)
	dir := t.TempDir()
	replica := filepath.Join(dir, "replica.db")
	stubLitestream(t, fixture, false)
	cfg := config.SyncConfig{ReplicaPath: replica, LitestreamConfig: "/etc/litestream.yml"}
	if err := RunOnce(context.Background(), cfg, "sqlite:///var/lib/analytics/analytics.db", slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(replica); err != nil {
		t.Fatal("replica missing")
	}
	if _, err := os.Stat(filepath.Join(dir, ".last_sync")); err != nil {
		t.Fatal("marker missing")
	}
	if _, err := os.Stat(replica + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp must not linger")
	}
	// The swapped-in replica must be a usable database, not just a file.
	st, err := store.Open("sqlite://" + replica)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.ProjectAliases(context.Background()); err != nil {
		t.Errorf("replica not queryable: %v", err)
	}
}

func TestRunOnceRestoreFailureKeepsReplica(t *testing.T) {
	fixture := makeValidDB(t)
	dir := t.TempDir()
	replica := filepath.Join(dir, "replica.db")
	if err := os.WriteFile(replica, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubLitestream(t, fixture, true)
	cfg := config.SyncConfig{ReplicaPath: replica}
	if err := RunOnce(context.Background(), cfg, "sqlite:///x.db", slog.Default()); err == nil {
		t.Fatal("restore failure must error")
	}
	data, err := os.ReadFile(replica)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatal("failed cycle must not touch existing replica")
	}
	if _, err := os.Stat(replica + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp must be cleaned up after a failed restore")
	}
}

func TestRunOnceCorruptRestoreRejected(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	replica := filepath.Join(dir, "replica.db")
	stubLitestream(t, garbage, false)
	cfg := config.SyncConfig{ReplicaPath: replica}
	if err := RunOnce(context.Background(), cfg, "sqlite:///x.db", slog.Default()); err == nil {
		t.Fatal("quick_check must reject a corrupt restore")
	}
	if _, err := os.Stat(replica); !os.IsNotExist(err) {
		t.Fatal("corrupt file must not become the replica")
	}
	if _, err := os.Stat(replica + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp must be cleaned up after a rejected restore")
	}
}

// A corrupt restore must not clobber a good existing replica either.
func TestRunOnceCorruptRestoreKeepsGoodReplica(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	replica := filepath.Join(dir, "replica.db")
	if err := os.WriteFile(replica, []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubLitestream(t, garbage, false)
	if err := RunOnce(context.Background(), config.SyncConfig{ReplicaPath: replica},
		"sqlite:///x.db", slog.Default()); err == nil {
		t.Fatal("want error")
	}
	data, err := os.ReadFile(replica)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "good" {
		t.Fatal("rejected restore overwrote the previous replica")
	}
}

func TestSourcePath(t *testing.T) {
	for dsn, want := range map[string]string{
		"sqlite:///var/lib/analytics/a.db": "/var/lib/analytics/a.db",
		"sqlite://relative.db":             "relative.db",
		"/already/a/path.db":               "/already/a/path.db",
	} {
		if got := sourcePath(dsn); got != want {
			t.Errorf("sourcePath(%q) = %q, want %q", dsn, got, want)
		}
	}
}

// Run must do a cycle immediately rather than waiting out the first
// interval, and must return promptly when cancelled.
func TestRunCyclesImmediatelyAndStops(t *testing.T) {
	fixture := makeValidDB(t)
	dir := t.TempDir()
	replica := filepath.Join(dir, "replica.db")
	calls := stubLitestream(t, fixture, false)
	cfg := config.SyncConfig{ReplicaPath: replica}
	cfg.Interval = time.Hour // long enough that only the first cycle runs

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "sqlite:///x.db", slog.Default()) }()

	deadline := time.After(5 * time.Second)
	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("Run did not perform an immediate cycle")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// A failing cycle must be logged and retried, not fatal to the loop.
func TestRunSurvivesFailingCycle(t *testing.T) {
	dir := t.TempDir()
	calls := stubLitestream(t, "", true)
	cfg := config.SyncConfig{ReplicaPath: filepath.Join(dir, "replica.db")}
	cfg.Interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "sqlite:///x.db", slog.Default()) }()

	deadline := time.After(5 * time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("loop stopped after %d failing cycles", calls.Load())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// A zero interval must not panic the ticker; it falls back to a default.
func TestRunZeroIntervalDoesNotPanic(t *testing.T) {
	fixture := makeValidDB(t)
	dir := t.TempDir()
	stubLitestream(t, fixture, false)
	cfg := config.SyncConfig{ReplicaPath: filepath.Join(dir, "replica.db")} // Interval zero

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "sqlite:///x.db", slog.Default()) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}
