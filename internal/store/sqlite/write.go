package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const tsFormat = "2006-01-02T15:04:05Z"

func (d *DB) WriteWebHits(ctx context.Context, hits []store.WebHit) error {
	if len(hits) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		// INSERT OR IGNORE: with client-supplied UUIDv7 ids, a batch
		// retried after a timeout that actually succeeded is a no-op.
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO web_hits
			(id, project, ts, received_at, actor_id, user_id, group_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 country, device, browser, os)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range hits {
			if _, err := stmt.ExecContext(ctx, h.ID, h.Project,
				h.TS.UTC().Format(tsFormat), h.ReceivedAt.UTC().Format(tsFormat),
				h.ActorID, h.UserID, h.GroupID,
				h.Host, h.Path, h.ReferrerSource, h.UTMSource, h.UTMMedium, h.UTMCampaign,
				h.Country, h.Device, h.Browser, h.OS); err != nil {
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
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO product_events
			(id, project, event_name, ts, received_at, actor_id, user_id, group_id,
			 platform, app_version, attributes)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
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
				e.TS.UTC().Format(tsFormat), e.ReceivedAt.UTC().Format(tsFormat),
				e.ActorID, e.UserID, e.GroupID, e.Platform, e.AppVersion,
				string(blob)); err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) WriteAppViews(ctx context.Context, views []store.AppView) error {
	if len(views) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO app_views
			(id, project, ts, received_at, actor_id, user_id, group_id, session_id,
			 screen, platform, app_version, os_version, device_model, locale, country)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, v := range views {
			if _, err := stmt.ExecContext(ctx, v.ID, v.Project,
				v.TS.UTC().Format(tsFormat), v.ReceivedAt.UTC().Format(tsFormat),
				v.ActorID, v.UserID, v.GroupID, v.SessionID,
				v.Screen, v.Platform, v.AppVersion, v.OSVersion,
				v.DeviceModel, v.Locale, v.Country); err != nil {
				return fmt.Errorf("app view %s: %w", v.ID, err)
			}
		}
		return nil
	})
}

// UpsertIdentities records display names, latest write wins.
// identities.last_seen_day is maintained by the daily pass, not here.
func (d *DB) UpsertIdentities(ctx context.Context, ids []store.Identity) error {
	if len(ids) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO identities
			(project, kind, id, name, updated_at)
			VALUES (?,?,?,?,datetime('now'))
			ON CONFLICT(project, kind, id) DO UPDATE SET
			  name=excluded.name, updated_at=excluded.updated_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, i := range ids {
			if i.ID == "" || i.Name == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, i.Project, i.Kind, i.ID, i.Name); err != nil {
				return fmt.Errorf("identity %s/%s: %w", i.Kind, i.ID, err)
			}
		}
		return nil
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
