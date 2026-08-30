// Package dashboards renders the Evidence site: it snapshots the database,
// runs the Evidence build, and serves the result. Evidence is a static site
// generator — it bakes query results in at build time — so "serving
// dashboards" is a build loop, not a request path.
package dashboards

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// snapshot writes a consistent copy of dbPath to dest.
//
// Evidence's node process then owns a file nobody else writes to. Without
// this it would read the live database — which is checkpointed underneath it
// while traffic arrives — or a replica that a restore job can replace by
// rename halfway through a build.
func snapshot(ctx context.Context, dbPath, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// VACUUM INTO refuses to overwrite, so the previous snapshot goes first.
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Checked rather than left to the driver: opening a missing file creates
	// an empty database, and Evidence would happily render a site with no
	// data in it. Opening read-only instead is not an option — a WAL
	// database with no other connection has no -shm, and a read-only
	// connection cannot create one.
	if _, err := os.Stat(dbPath); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return err
	}
	return nil
}

// sourceFilename is the value Evidence's sqlite datasource needs in
// EVIDENCE_SOURCE__twillingate__filename.
//
// The plugin resolves it with path.join(<source dir>, filename): an absolute
// path gets rewritten into the source directory, and a hand-written ../../..
// silently breaks whenever the project directory changes depth. Computing it
// keeps the knob out of the deployment files entirely.
func sourceFilename(projectDir, snapshotPath string) (string, error) {
	return filepath.Rel(filepath.Join(projectDir, "sources", "twillingate"), snapshotPath)
}
