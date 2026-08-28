// Package mcpserver implements the MCP endpoint (endpoint spec): an
// authenticated, read-only query surface plus the management tools.
// It never logs request bodies or bearer tokens; submitted SQL is
// logged at debug only.
package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// OpenReadDB opens the database file read-only with its own pool,
// independent of the single-writer store connection (endpoint spec §6.1).
// query_only is belt over the mode=ro braces: even a bug that finds a
// writable path is refused by the connection itself.
func OpenReadDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?mode=ro" +
		"&_pragma=query_only(1)" +
		"&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	return db, nil
}

func queryRows(ctx context.Context, db *sql.DB, timeout time.Duration, max int, q string, args ...any) ([]string, [][]string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	var out [][]string
	truncated := false
	for rows.Next() {
		if len(out) == max {
			truncated = true
			break
		}
		vals := make([]any, len(cols))
		for i := range vals {
			vals[i] = new(sql.NullString)
		}
		if err := rows.Scan(vals...); err != nil {
			return nil, nil, false, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			ns := v.(*sql.NullString)
			if ns.Valid {
				row[i] = ns.String
			}
		}
		out = append(out, row)
	}
	return cols, out, truncated, rows.Err()
}
