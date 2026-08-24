package dashboards

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func seedDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("create table t(x); insert into t values (42)"); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCopiesData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	seedDB(t, src)

	dest := filepath.Join(dir, "work", "snapshot.db")
	if err := snapshot(context.Background(), src, dest); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	out, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var x int
	if err := out.QueryRow("select x from t").Scan(&x); err != nil || x != 42 {
		t.Fatalf("snapshot content: x=%d err=%v", x, err)
	}
}

func TestSnapshotOverwritesPrevious(t *testing.T) {
	// VACUUM INTO refuses an existing target; every cycle after the first
	// would fail if the old snapshot were left in place.
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	seedDB(t, src)
	dest := filepath.Join(dir, "snapshot.db")
	if err := snapshot(context.Background(), src, dest); err != nil {
		t.Fatal(err)
	}
	if err := snapshot(context.Background(), src, dest); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
}

func TestSnapshotOfAMissingDatabaseFails(t *testing.T) {
	dir := t.TempDir()
	err := snapshot(context.Background(), filepath.Join(dir, "nope.db"), filepath.Join(dir, "s.db"))
	if err == nil {
		t.Fatal("want an error snapshotting a database that does not exist")
	}
}

func TestSourceFilenameIsRelativeToTheSourceDir(t *testing.T) {
	got, err := sourceFilename("/opt/evidence", "/var/lib/dashboards/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	want := "../../../../var/lib/dashboards/snapshot.db"
	if got != want {
		t.Errorf("sourceFilename = %q, want %q", got, want)
	}
}

func TestSourceFilenameTracksProjectDepth(t *testing.T) {
	// The old hand-written ../../../ was correct only for a project at /app.
	got, err := sourceFilename("/app", "/data/replica.db")
	if err != nil {
		t.Fatal(err)
	}
	if want := "../../../data/replica.db"; got != want {
		t.Errorf("sourceFilename = %q, want %q", got, want)
	}
}
