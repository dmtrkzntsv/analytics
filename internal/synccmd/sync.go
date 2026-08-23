// Package synccmd maintains the backoffice read replica (spec §10, split
// topology): restore from object storage, verify, swap atomically.
package synccmd

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmitry/analytics/internal/config"
	_ "modernc.org/sqlite"
)

// execCommand is a seam so tests can stand in for the litestream binary.
var execCommand = exec.CommandContext

// defaultInterval applies when a caller builds a SyncConfig directly rather
// than through config.Parse, which supplies its own default.
const defaultInterval = 5 * time.Minute

func sourcePath(dbDSN string) string {
	return strings.TrimPrefix(dbDSN, "sqlite://")
}

// RunOnce performs one replica refresh: restore to a temporary file, verify
// it, then swap it into place.
//
// The restore never writes to ReplicaPath directly. Anything that goes wrong
// — a failed restore, a truncated or corrupt download — leaves the previous
// replica exactly as it was, so the backoffice keeps serving stale-but-valid
// data instead of losing its dataset.
func RunOnce(ctx context.Context, cfg config.SyncConfig, dbDSN string, logger *slog.Logger) error {
	tmp := cfg.ReplicaPath + ".tmp"
	defer os.Remove(tmp)

	args := []string{"restore"}
	if cfg.LitestreamConfig != "" {
		args = append(args, "-config", cfg.LitestreamConfig)
	}
	args = append(args, "-o", tmp, sourcePath(dbDSN))
	if out, err := execCommand(ctx, "litestream", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("sync: litestream restore: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	if err := quickCheck(ctx, tmp); err != nil {
		return fmt.Errorf("sync: restored file failed verification: %w", err)
	}
	if err := os.Rename(tmp, cfg.ReplicaPath); err != nil {
		return fmt.Errorf("sync: swap replica: %w", err)
	}
	marker := filepath.Join(filepath.Dir(cfg.ReplicaPath), ".last_sync")
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		logger.Warn("sync: marker write failed", "error", err)
	}
	logger.Info("sync: replica updated", "path", cfg.ReplicaPath)
	return nil
}

func quickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check: %s", result)
	}
	return nil
}

// Run refreshes the replica immediately, then every cfg.Interval until ctx
// is cancelled. A failing cycle is logged and retried on the next tick — a
// transient object-storage outage must not kill the loop.
func Run(ctx context.Context, cfg config.SyncConfig, dbDSN string, logger *slog.Logger) error {
	if err := RunOnce(ctx, cfg, dbDSN, logger); err != nil {
		logger.Error("sync cycle failed", "error", err)
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := RunOnce(ctx, cfg, dbDSN, logger); err != nil {
				logger.Error("sync cycle failed", "error", err)
			}
		}
	}
}
