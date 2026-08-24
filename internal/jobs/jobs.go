// Package jobs schedules the daily maintenance work (spec §9): salt
// rotation at 00:00 UTC, aggregation+prune at 03:00 UTC, with a catch-up
// pass on boot so downtime never skips days.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

// Rotator is the slice of identity.Salter the scheduler needs.
type Rotator interface {
	Rotate(ctx context.Context) error
	Current(ctx context.Context) (string, error)
}

type Runner struct {
	store  store.Store
	cfg    *config.Config
	salt   Rotator
	logger *slog.Logger
	now    func() time.Time

	// Last UTC day on which each job fired, so a job runs at most once per
	// day however often the ticker lands inside its hour.
	lastSaltDay string
	lastAggDay  string
}

func New(st store.Store, cfg *config.Config, salt Rotator, logger *slog.Logger, now func() time.Time) *Runner {
	return &Runner{store: st, cfg: cfg, salt: salt, logger: logger, now: now}
}

func aggSettingsFor(p *config.Project) store.ProductAggSettings {
	if p == nil || p.ProductAggregation == nil {
		return store.ProductAggSettings{}
	}
	return store.ProductAggSettings{
		Enabled:    p.ProductAggregation.Enabled,
		Attributes: p.ProductAggregation.Attributes,
		TopN:       p.ProductAggregation.TopN,
	}
}

// RunDailyPass rolls up every day that has aged out of the raw window,
// prunes aggregates past their retention, refreshes the flat view and
// reclaims free pages.
//
// Per-project failures are logged and skipped rather than returned: one
// broken project must not stop maintenance for the rest. Only failures to
// enumerate work at all are treated as fatal to the pass.
func (r *Runner) RunDailyPass(ctx context.Context) error {
	today := civil.Today(r.now())
	ids, err := r.store.ProjectAliases(ctx)
	if err != nil {
		return err
	}
	// Config projects may not be synced yet on first boot; union them in.
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for _, p := range r.cfg.Projects {
		if !seen[p.Alias] {
			ids = append(ids, p.Alias)
		}
	}
	for _, id := range ids {
		// Archived projects (in the DB, absent from config) fall back to
		// global retention and zero-value aggregation settings, which means
		// their raw rows are dropped without a product rollup.
		ret := r.cfg.RetentionFor(id)

		days, err := r.store.WebDaysBefore(ctx, id, today.AddDays(-ret.Web.RawDays))
		if err != nil {
			return err
		}
		for _, day := range days {
			if err := r.store.AggregateWebDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate web failed", "project", id, "day", day.String(), "error", err)
			}
		}

		prodDays, err := r.store.ProductDaysBefore(ctx, id, today.AddDays(-ret.Product.RawDays))
		if err != nil {
			return err
		}
		settings := aggSettingsFor(r.cfg.Project(id))
		for _, day := range prodDays {
			if err := r.store.AggregateProductDay(ctx, id, day, settings); err != nil {
				r.logger.Error("aggregate product failed", "project", id, "day", day.String(), "error", err)
			}
		}

		if err := r.store.PruneAggregates(ctx, id,
			today.AddDays(-ret.Web.AggregateDays),
			today.AddDays(-ret.Product.AggregateDays),
			today.AddDays(-ret.App.AggregateDays)); err != nil {
			r.logger.Error("prune failed", "project", id, "error", err)
		}
	}

	if keys, err := r.store.KnownAttributeKeys(ctx); err != nil {
		r.logger.Error("attribute key scan failed", "error", err)
	} else if err := r.store.RebuildFlatView(ctx, keys); err != nil {
		r.logger.Error("flat view rebuild failed", "error", err)
	}
	if err := r.store.IncrementalVacuum(ctx); err != nil {
		r.logger.Error("incremental vacuum failed", "error", err)
	}
	return nil
}

// runScheduled fires whichever jobs are due at the current time. Split out
// from Run so the day-boundary guards are testable without a real clock.
func (r *Runner) runScheduled(ctx context.Context) {
	now := r.now().UTC()
	day := civil.Today(now).String()
	if now.Hour() == 0 && r.lastSaltDay != day {
		r.lastSaltDay = day
		if err := r.salt.Rotate(ctx); err != nil {
			r.logger.Error("salt rotation", "error", err)
		}
	}
	if now.Hour() == 3 && r.lastAggDay != day {
		r.lastAggDay = day
		if err := r.RunDailyPass(ctx); err != nil {
			r.logger.Error("daily pass", "error", err)
		}
	}
}

// Run blocks until ctx is cancelled, doing a catch-up pass on entry.
func (r *Runner) Run(ctx context.Context) {
	if _, err := r.salt.Current(ctx); err != nil {
		r.logger.Error("initial salt", "error", err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		r.logger.Error("boot catch-up pass", "error", err)
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.runScheduled(ctx)
		}
	}
}
