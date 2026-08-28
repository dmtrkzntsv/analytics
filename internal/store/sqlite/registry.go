// Package sqlite provides SQLite backend for the store interface.
// Registry row access (managed-config spec §3). Every write bumps
// meta.config_version and inserts its audit row in the same transaction,
// which is what lets other processes notice changes by polling one row.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/store"
	"github.com/google/uuid"
)

func auditAndBump(ctx context.Context, tx *sql.Tx, a store.AuditEntry) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, subject, detail) VALUES (?,?,?,?)`,
		a.Actor, a.Action, a.Subject, a.Detail); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)
		 WHERE key = 'config_version'`)
	return err
}

func (d *DB) ConfigVersion(ctx context.Context) (int64, error) {
	var v int64
	err := d.db.QueryRowContext(ctx,
		`SELECT CAST(value AS INTEGER) FROM meta WHERE key='config_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

func (d *DB) LoadRegistry(ctx context.Context) ([]store.RegistryProject, []store.RegistryKey, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT alias, name, identity,
		allowed_origins, COALESCE(retention,''), COALESCE(product_aggregation,''),
		archived_at IS NOT NULL FROM projects ORDER BY alias`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ps []store.RegistryProject
	for rows.Next() {
		var p store.RegistryProject
		if err := rows.Scan(&p.Alias, &p.Name, &p.Identity,
			&p.AllowedOrigins, &p.Retention, &p.Aggregation, &p.Archived); err != nil {
			return nil, nil, err
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	krows, err := d.db.QueryContext(ctx, `SELECT key, project, label,
		disabled_at IS NOT NULL FROM ingest_keys ORDER BY project, label`)
	if err != nil {
		return nil, nil, err
	}
	defer krows.Close()
	var ks []store.RegistryKey
	for krows.Next() {
		var k store.RegistryKey
		if err := krows.Scan(&k.Key, &k.Project, &k.Label, &k.Disabled); err != nil {
			return nil, nil, err
		}
		ks = append(ks, k)
	}
	return ks2(ps, ks, krows.Err())
}

// ks2 keeps the happy-path return on one line above.
func ks2(ps []store.RegistryProject, ks []store.RegistryKey, err error) ([]store.RegistryProject, []store.RegistryKey, error) {
	if err != nil {
		return nil, nil, err
	}
	return ps, ks, nil
}

func (d *DB) CreateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects
			(id, alias, name, identity, allowed_origins, retention, product_aggregation)
			VALUES (?,?,?,?,?,NULLIF(?,''),NULLIF(?,''))`,
			id.String(), p.Alias, p.Name, p.Identity, p.AllowedOrigins,
			p.Retention, p.Aggregation); err != nil {
			return fmt.Errorf("create project %q: %w", p.Alias, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) UpdateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE projects SET name=?, identity=?,
			allowed_origins=?, retention=NULLIF(?,''), product_aggregation=NULLIF(?,'')
			WHERE alias=?`,
			p.Name, p.Identity, p.AllowedOrigins, p.Retention, p.Aggregation, p.Alias)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("update project: unknown alias %q", p.Alias)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) SetProjectArchived(ctx context.Context, alias string, archived bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE projects SET archived_at=datetime('now') WHERE alias=? AND archived_at IS NULL`
		if !archived {
			q = `UPDATE projects SET archived_at=NULL WHERE alias=?`
		}
		res, err := tx.ExecContext(ctx, q, alias)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 && archived {
			// restore of a non-archived project is a no-op, archive of an
			// unknown alias is an error; check existence to distinguish.
			var c int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM projects WHERE alias=?`, alias).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return fmt.Errorf("archive: unknown alias %q", alias)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) InsertIngestKey(ctx context.Context, k store.RegistryKey, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var c int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM ingest_keys WHERE project=? AND label=?`,
			k.Project, k.Label).Scan(&c); err != nil {
			return err
		}
		if c > 0 {
			return fmt.Errorf("key label %q already exists for project %q", k.Label, k.Project)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ingest_keys (key, project, label) VALUES (?,?,?)`,
			k.Key, k.Project, k.Label); err != nil {
			return fmt.Errorf("issue key for %q: %w", k.Project, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) SetIngestKeyDisabled(ctx context.Context, project, label string, disabled bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE ingest_keys SET disabled_at=datetime('now') WHERE project=? AND label=?`
		if !disabled {
			q = `UPDATE ingest_keys SET disabled_at=NULL WHERE project=? AND label=?`
		}
		res, err := tx.ExecContext(ctx, q, project, label)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("key %s/%s not found", project, label)
		}
		return auditAndBump(ctx, tx, a)
	})
}
