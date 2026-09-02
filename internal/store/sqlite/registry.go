// Package sqlite provides SQLite backend for the store interface.
// Registry row access (managed-config spec §3). Every write bumps
// meta.config_version and inserts its audit row in the same transaction,
// which is what lets other processes notice changes by polling one row.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
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
		allowed_origins, COALESCE(retention,''), attributes,
		archived_at IS NOT NULL FROM projects ORDER BY alias`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ps []store.RegistryProject
	for rows.Next() {
		var p store.RegistryProject
		if err := rows.Scan(&p.Alias, &p.Name, &p.Identity,
			&p.AllowedOrigins, &p.Retention, &p.Attributes, &p.Archived); err != nil {
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
		var taken int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM projects WHERE alias=?`, p.Alias).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("create project: alias %q: %w", p.Alias, store.ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects
			(id, alias, name, identity, allowed_origins, retention, attributes)
			VALUES (?,?,?,?,?,NULLIF(?,''),?)`,
			id.String(), p.Alias, p.Name, p.Identity, p.AllowedOrigins,
			p.Retention, p.Attributes); err != nil {
			return fmt.Errorf("create project %q: %w", p.Alias, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) UpdateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE projects SET name=?, identity=?,
			allowed_origins=?, retention=NULLIF(?,''), attributes=?
			WHERE alias=?`,
			p.Name, p.Identity, p.AllowedOrigins, p.Retention, p.Attributes, p.Alias)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("update project: unknown alias %q: %w", p.Alias, store.ErrNotFound)
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
				return fmt.Errorf("archive: unknown alias %q: %w", alias, store.ErrNotFound)
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
			return fmt.Errorf("key label %q for project %q: %w", k.Label, k.Project, store.ErrConflict)
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
			return fmt.Errorf("key %s/%s: %w", project, label, store.ErrNotFound)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// projectTables is every table carrying a per-project `project` column.
// Kept in one place so a future migration adding a table has one list to
// extend; TestProjectTablesMatchesSchema (registry_test.go) cross-checks
// this list against the live schema (sqlite_master + pragma_table_info) in
// both directions, so a forgotten addition or a stale entry fails loudly
// instead of silently orphaning rows on DeleteProjectData or RenameProject.
var projectTables = []string{
	"web_hits", "product_events", "app_views",
	"agg_web_daily", "agg_web_pages", "agg_web_hosts", "agg_web_referrers", "agg_web_countries",
	"agg_web_devices", "agg_web_browsers", "agg_web_os", "agg_web_utm",
	"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	"agg_app_daily", "agg_app_screens", "agg_app_versions", "agg_app_os",
	"agg_app_devices", "agg_app_countries",
	"actors", "agg_retention", "identities", "agg_identity_daily",
	"ingest_keys",
}

// DeleteProjectData hard-deletes the project and every row keyed by its
// alias, in one transaction (spec §7.3). The audit row is written in the
// same transaction and survives — audit_log has no project column.
// Page reclamation is the caller's job (IncrementalVacuum), because a
// vacuum inside the tx would deadlock the single connection.
func (d *DB) DeleteProjectData(ctx context.Context, alias string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE alias=?`, alias)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("delete: unknown alias %q: %w", alias, store.ErrNotFound)
		}
		for _, table := range projectTables {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE project=?`, alias); err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}

// RenameProject rewrites the alias on the registry row and the `project`
// column on every table in projectTables, in one transaction. ingest_keys
// is one of those tables, so keys follow the rename and deployed clients
// keep working without redeploying — that is what makes this command
// usable rather than a data-loss trap. The already-taken check on the new
// alias runs before any write. The unknown-source check is not a separate
// pre-check: it is the RowsAffected()==0 result of the UPDATE projects
// statement itself, and an error at that point aborts the transaction —
// so either way a rejected rename leaves the source alias and all its rows
// completely untouched, just via rollback rather than avoidance for the
// unknown-source case.
//
// PRAGMA foreign_keys is never enabled in this codebase, so the
// `REFERENCES projects(alias)` clause on ingest_keys does not constrain
// statement order; projects is updated first regardless, to match the
// design doc.
func (d *DB) RenameProject(ctx context.Context, old, new string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if old == new {
			return fmt.Errorf("rename: project %q is already named %q", old, new)
		}
		var taken int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM projects WHERE alias=?`, new).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("rename: alias %q: %w", new, store.ErrConflict)
		}
		res, err := tx.ExecContext(ctx, `UPDATE projects SET alias=? WHERE alias=?`, new, old)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("rename: unknown alias %q: %w", old, store.ErrNotFound)
		}
		for _, table := range projectTables {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+table+` SET project=? WHERE project=?`, new, old); err != nil {
				return fmt.Errorf("rename %s: %w", table, err)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}
