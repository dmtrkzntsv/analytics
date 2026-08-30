// Package jobs schedules the daily maintenance work (spec §9): salt
// rotation at 00:00 UTC, aggregation+prune at 03:00 UTC, with a catch-up
// pass on boot so downtime never skips days.
package jobs

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Rotator is the slice of identity.Salter the scheduler needs.
type Rotator interface {
	Rotate(ctx context.Context) error
	Current(ctx context.Context) (string, error)
}

type Runner struct {
	store  store.Store
	cfg    *config.Config
	reg    *manage.Registry
	salt   Rotator
	logger *slog.Logger
	now    func() time.Time

	// Last UTC day on which each job fired, so a job runs at most once per
	// day however often the ticker lands inside its hour.
	lastSaltDay string
	lastAggDay  string
}

func New(st store.Store, cfg *config.Config, reg *manage.Registry, salt Rotator, logger *slog.Logger, now func() time.Time) *Runner {
	return &Runner{store: st, cfg: cfg, reg: reg, salt: salt, logger: logger, now: now}
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
	// The registry is the sole source of project config now, so
	// store.ProjectAliases (all rows, including archived) is the complete
	// list -- no config-side union needed. An archived project keeps its
	// retention overrides (it is still a registry row), where the old
	// config-absence fallback silently reverted it to global defaults.
	snap := r.reg.Snapshot(ctx)
	for _, id := range ids {
		ret := snap.RetentionFor(id)

		// Cohorts, actors and identity rollups read raw rows across all
		// three classes and never delete them, so they run over every day
		// still present -- not just the aged-out ones. Two reasons: a
		// web-only project has no app_views to drive them from, and
		// restricting them to aged-out days would leave the users, groups
		// and retention pages a whole raw window stale. Every write is
		// keyed and recomputed, so re-running a day is safe.
		identityDays, err := r.allRawDays(ctx, id, today.AddDays(1))
		if err != nil {
			return err
		}
		// Retention is undefined for anonymous projects: actor_id rotates at
		// midnight, so first_seen_day always equals the day itself and every
		// cohort would hold nothing but offset 0.
		p := snap.Project(id)
		identified := p != nil && p.Identity == config.IdentityIdentified
		for _, day := range identityDays {
			if identified {
				if err := r.store.UpsertActors(ctx, id, day); err != nil {
					r.logger.Error("upsert actors failed", "project", id, "day", day.String(), "error", err)
				}
				if err := r.store.AggregateRetentionDay(ctx, id, day); err != nil {
					r.logger.Error("aggregate retention failed", "project", id, "day", day.String(), "error", err)
				}
			}
			if err := r.store.AggregateIdentityDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate identity failed", "project", id, "day", day.String(), "error", err)
			}
		}

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
		settings := snap.AggregationFor(id)
		for _, day := range prodDays {
			if err := r.store.AggregateProductDay(ctx, id, day, settings); err != nil {
				r.logger.Error("aggregate product failed", "project", id, "day", day.String(), "error", err)
			}
		}

		appDays, err := r.store.AppDaysBefore(ctx, id, today.AddDays(-ret.App.RawDays))
		if err != nil {
			return err
		}
		for _, day := range appDays {
			if err := r.store.AggregateAppDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate app failed", "project", id, "day", day.String(), "error", err)
			}
		}

		if err := r.store.PruneAggregates(ctx, id,
			today.AddDays(-ret.Web.AggregateDays),
			today.AddDays(-ret.Product.AggregateDays),
			today.AddDays(-ret.App.AggregateDays)); err != nil {
			r.logger.Error("prune failed", "project", id, "error", err)
		}
		if err := r.store.PruneActors(ctx, id, today.AddDays(-ret.App.AggregateDays)); err != nil {
			r.logger.Error("prune actors failed", "project", id, "error", err)
		}
		if err := r.store.PruneIdentities(ctx, id, today.AddDays(-ret.App.AggregateDays)); err != nil {
			r.logger.Error("prune identities failed", "project", id, "error", err)
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

// allRawDays merges the days present in all three raw tables, deduplicated
// and sorted, so a project is covered whatever mix of surfaces it uses.
func (r *Runner) allRawDays(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	seen := map[string]bool{}
	var out []civil.Date
	for _, fn := range []func(context.Context, string, civil.Date) ([]civil.Date, error){
		r.store.WebDaysBefore, r.store.ProductDaysBefore, r.store.AppDaysBefore,
	} {
		days, err := fn(ctx, project, before)
		if err != nil {
			return nil, err
		}
		for _, d := range days {
			if !seen[d.String()] {
				seen[d.String()] = true
				out = append(out, d)
			}
		}
	}
	// Chronological order matters: AggregateRetentionDay reads first_seen_day
	// from actors, so an earlier day must be recorded before a later one
	// references it.
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
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
