package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dmitry/analytics/internal/store"
	"github.com/google/uuid"
)

const tsFormat = "2006-01-02T15:04:05Z"

func (d *DB) WriteWebHits(ctx context.Context, hits []store.WebHit) error {
	if len(hits) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO web_hits
			(id, project, ts, visitor_hash, path, referrer_source,
			 utm_source, utm_medium, utm_campaign, country, device, browser, os)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range hits {
			if _, err := stmt.ExecContext(ctx, h.ID, h.Project,
				h.TS.UTC().Format(tsFormat), h.VisitorHash, h.Path, h.ReferrerSource,
				h.UTMSource, h.UTMMedium, h.UTMCampaign, h.Country, h.Device, h.Browser, h.OS); err != nil {
				return fmt.Errorf("web hit %s: %w", h.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) WriteProductEvents(ctx context.Context, evs []store.ProductEvent) error {
	if len(evs) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO product_events
			(id, project, event_name, user_id, ts, attributes) VALUES (?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range evs {
			attrs := e.Attributes
			if attrs == nil {
				attrs = map[string]string{}
			}
			blob, err := json.Marshal(attrs)
			if err != nil {
				return fmt.Errorf("event %s attributes: %w", e.ID, err)
			}
			if _, err := stmt.ExecContext(ctx, e.ID, e.Project, e.EventName,
				e.UserID, e.TS.UTC().Format(tsFormat), string(blob)); err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) SyncProjects(ctx context.Context, ps []store.ProjectInfo) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		aliases := make([]string, 0, len(ps))
		for _, p := range ps {
			aliases = append(aliases, p.Alias)
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("sync projects: %w", err)
			}
			// ON CONFLICT(alias) keeps the previously generated id.
			if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, alias, name) VALUES (?,?,?)
				ON CONFLICT(alias) DO UPDATE SET name=excluded.name, archived_at=NULL`,
				id.String(), p.Alias, p.Name); err != nil {
				return err
			}
		}
		q := `UPDATE projects SET archived_at=datetime('now') WHERE archived_at IS NULL`
		args := []any{}
		if len(aliases) > 0 {
			q += ` AND alias NOT IN (?` + strings.Repeat(",?", len(aliases)-1) + `)`
			for _, a := range aliases {
				args = append(args, a)
			}
		}
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	})
}

func (d *DB) ProjectAliases(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT alias FROM projects ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// tx runs fn in a transaction with commit/rollback handling; shared by all
// sqlite write paths.
func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
