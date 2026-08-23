// Package sqlite implements store.Store on modernc.org/sqlite (spec §7.2).
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/dmitry/analytics/internal/store"
	_ "modernc.org/sqlite"
)

func init() { store.Register("sqlite", open) }

type DB struct{ db *sql.DB }

func open(dsn string) (store.Store, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	// sqlite:///var/lib/analytics/a.db -> /var/lib/analytics/a.db
	// sqlite://relative.db            -> relative.db (host part)
	path := u.Path
	if u.Host != "" {
		path = u.Host + u.Path
	}
	path = strings.TrimPrefix(path, "//")
	return openAt(path)
}

func openAt(path string) (*DB, error) {
	// _pragma values applied on every new connection by the driver. Note:
	// journal_mode is deliberately NOT set here — unlike the other pragmas
	// below, switching journal mode writes the database header (page 1),
	// which would make the file non-empty before we get a chance to set
	// auto_vacuum (that pragma only takes effect on an empty database, i.e.
	// before any page has been written). So journal_mode is set explicitly
	// after auto_vacuum, below.
	dsn := "file:" + path + "?" + strings.Join([]string{
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=cache_size(-16000)",
		"_pragma=temp_store(2)",
	}, "&")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // single-writer pipeline (spec §7.2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	// auto_vacuum must be set before the first table is created, and
	// before journal_mode=WAL (which itself writes page 1).
	if _, err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: auto_vacuum: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: journal_mode: %w", err)
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }
