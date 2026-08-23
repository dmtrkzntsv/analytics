package sqlite

import (
	"context"
	"fmt"
)

// Stub implementations for store.Store methods not yet implemented in this
// task; later sqlite tasks replace each stub with a real implementation.

func (d *DB) KnownAttributeKeys(ctx context.Context) ([]string, error) {
	return nil, fmt.Errorf("sqlite: not implemented")
}

func (d *DB) RebuildFlatView(ctx context.Context, keys []string) error {
	return fmt.Errorf("sqlite: not implemented")
}
