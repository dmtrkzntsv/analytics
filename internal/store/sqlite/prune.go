package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

// webAggTables and productAggTables must list every agg_* table in the schema;
// TestPruneAggregatesCoversAllAggTables fails if a migration adds one that is
// missing here, since such a table would never honour retention.
var webAggTables = []string{
	"agg_web_daily", "agg_web_pages", "agg_web_referrers", "agg_web_countries",
	"agg_web_devices", "agg_web_browsers", "agg_web_os", "agg_web_utm",
}

var productAggTables = []string{"agg_product_daily", "agg_product_totals", "agg_product_attrs"}

// PruneAggregates drops aggregate rows older than the per-family retention
// cutoffs for one project. Web and product retention are configured
// separately, hence the two cutoffs. All deletes share a transaction so
// retention is applied atomically across tables.
func (d *DB) PruneAggregates(ctx context.Context, project string, webBefore, productBefore civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		del := func(tables []string, before civil.Date) error {
			for _, tbl := range tables {
				if _, err := tx.ExecContext(ctx,
					fmt.Sprintf(`DELETE FROM %s WHERE project=? AND day < ?`, tbl),
					project, before.String()); err != nil {
					return fmt.Errorf("prune %s: %w", tbl, err)
				}
			}
			return nil
		}
		if err := del(webAggTables, webBefore); err != nil {
			return err
		}
		return del(productAggTables, productBefore)
	})
}

// IncrementalVacuum reclaims a bounded number of free pages. It is bounded on
// purpose: a full VACUUM would rewrite the whole database, which is not
// affordable on the Raspberry Pi deployment. Relies on the database having
// been created with auto_vacuum=INCREMENTAL (see Open).
func (d *DB) IncrementalVacuum(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `PRAGMA incremental_vacuum(1000)`); err != nil {
		return fmt.Errorf("incremental vacuum: %w", err)
	}
	return nil
}
