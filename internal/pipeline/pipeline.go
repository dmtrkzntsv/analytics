// Package pipeline decouples HTTP ingestion from storage: a bounded
// channel absorbs bursts; a single worker writes batches (spec §5.3).
// Availability over completeness: enqueue never blocks, flush failures
// are retried then dropped.
package pipeline

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

type Sink interface {
	WriteWebHits(ctx context.Context, hits []store.WebHit) error
	WriteProductEvents(ctx context.Context, evs []store.ProductEvent) error
}

// item carries exactly one of hit/event.
type item struct {
	hit   *store.WebHit
	event *store.ProductEvent
}

var retryDelays = []time.Duration{time.Second, 5 * time.Second, 25 * time.Second}

type Buffer struct {
	cfg     config.BufferConfig
	sink    Sink
	logger  *slog.Logger
	ch      chan item
	dropped atomic.Uint64
}

func New(cfg config.BufferConfig, sink Sink, logger *slog.Logger) *Buffer {
	return &Buffer{cfg: cfg, sink: sink, logger: logger, ch: make(chan item, cfg.Capacity)}
}

func (b *Buffer) EnqueueHit(h store.WebHit)         { b.enqueue(item{hit: &h}) }
func (b *Buffer) EnqueueEvent(e store.ProductEvent) { b.enqueue(item{event: &e}) }
func (b *Buffer) Dropped() uint64                   { return b.dropped.Load() }

func (b *Buffer) enqueue(it item) {
	for {
		select {
		case b.ch <- it:
			return
		default:
			// Full: drop the oldest to make room, count it, retry.
			select {
			case <-b.ch:
				b.dropped.Add(1)
			default:
			}
		}
	}
}

func (b *Buffer) Run(ctx context.Context) {
	ticker := time.NewTicker(b.cfg.FlushInterval.Duration)
	defer ticker.Stop()
	var hits []store.WebHit
	var events []store.ProductEvent
	flush := func(ctx context.Context) {
		if len(hits) > 0 {
			b.write(ctx, func(c context.Context) error { return b.sink.WriteWebHits(c, hits) }, len(hits), "web_hits")
			hits = nil
		}
		if len(events) > 0 {
			b.write(ctx, func(c context.Context) error { return b.sink.WriteProductEvents(c, events) }, len(events), "product_events")
			events = nil
		}
	}
	for {
		select {
		case <-ctx.Done():
			// Drain whatever is still queued, then final flush.
			for {
				select {
				case it := <-b.ch:
					if it.hit != nil {
						hits = append(hits, *it.hit)
					} else {
						events = append(events, *it.event)
					}
					continue
				default:
				}
				break
			}
			flush(ctx)
			return
		case it := <-b.ch:
			if it.hit != nil {
				hits = append(hits, *it.hit)
			} else {
				events = append(events, *it.event)
			}
			if len(hits)+len(events) >= b.cfg.FlushMaxEvents {
				flush(ctx)
			}
		case <-ticker.C:
			flush(ctx)
		}
	}
}

// write retries per spec §5.3 (3 attempts, then drop with an error log).
// The sink call itself always uses context.Background(): shutdown must
// still be able to flush. The backoff sleep between attempts, however, is
// cancellation-aware — if ctx is done (e.g. the process is shutting down)
// the sleep is aborted immediately and remaining attempts fire back-to-back
// without delay, so a failing flush can't stall shutdown past the retry
// writes themselves. Normal (non-shutdown) operation keeps the full
// 1s/5s/25s backoff.
func (b *Buffer) write(ctx context.Context, fn func(context.Context) error, n int, kind string) {
	var err error
	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryDelays[attempt-1]):
			case <-ctx.Done():
			}
		}
		if err = fn(context.Background()); err == nil {
			return
		}
	}
	b.logger.Error("pipeline: batch dropped after retries", "kind", kind, "count", n, "error", err)
}
