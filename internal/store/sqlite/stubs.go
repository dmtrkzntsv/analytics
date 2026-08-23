package sqlite

import (
	"context"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

// Stub implementations for store.Store methods not yet implemented in this
// task; later sqlite tasks replace each stub with a real implementation.

func (d *DB) WebDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return nil, fmt.Errorf("sqlite: not implemented")
}

func (d *DB) ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return nil, fmt.Errorf("sqlite: not implemented")
}

func (d *DB) AggregateWebDay(ctx context.Context, project string, day civil.Date) error {
	return fmt.Errorf("sqlite: not implemented")
}

func (d *DB) AggregateProductDay(ctx context.Context, project string, day civil.Date, agg store.ProductAggSettings) error {
	return fmt.Errorf("sqlite: not implemented")
}

func (d *DB) PruneAggregates(ctx context.Context, project string, webBefore, productBefore civil.Date) error {
	return fmt.Errorf("sqlite: not implemented")
}

func (d *DB) IncrementalVacuum(ctx context.Context) error {
	return fmt.Errorf("sqlite: not implemented")
}

func (d *DB) KnownAttributeKeys(ctx context.Context) ([]string, error) {
	return nil, fmt.Errorf("sqlite: not implemented")
}

func (d *DB) RebuildFlatView(ctx context.Context, keys []string) error {
	return fmt.Errorf("sqlite: not implemented")
}
