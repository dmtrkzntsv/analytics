package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

// seedDB migrates a fresh database and returns its path. Every
// mcpserver test builds on this.
func seedDB(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/read.db"
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(context.Background(), store.RegistryProject{
		Alias: "blog", Name: "My blog", Identity: "identified", AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return path
}

func TestOpenReadDBIsReadOnly(t *testing.T) {
	db, err := OpenReadDB(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('x','y')`); err == nil {
		t.Fatal("write through the read connection succeeded")
	}
}

func TestQueryRowsCapsAndTruncates(t *testing.T) {
	db, err := OpenReadDB(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cols, rows, truncated, err := queryRows(context.Background(), db,
		time.Second, 2, `WITH n(i) AS (VALUES (1),(2),(3),(4)) SELECT i FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0] != "i" {
		t.Fatalf("cols = %v", cols)
	}
	if len(rows) != 2 || !truncated {
		t.Fatalf("rows = %d truncated = %v", len(rows), truncated)
	}
}

func TestQueryRowsTimeout(t *testing.T) {
	db, err := OpenReadDB(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// a recursive CTE that never finishes without the deadline
	_, _, _, err = queryRows(context.Background(), db, 50*time.Millisecond, 10,
		`WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r) SELECT COUNT(*) FROM r`)
	if err == nil {
		t.Fatal("runaway query did not time out")
	}
}
