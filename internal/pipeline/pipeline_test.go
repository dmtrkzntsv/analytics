package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

type fakeSink struct {
	mu     sync.Mutex
	hits   []store.WebHit
	events []store.ProductEvent
	views  []store.AppView
	fail   int // fail this many calls before succeeding
	calls  int
}

func (f *fakeSink) WriteWebHits(_ context.Context, h []store.WebHit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail > 0 {
		f.fail--
		return errors.New("boom")
	}
	f.hits = append(f.hits, h...)
	return nil
}

func (f *fakeSink) WriteProductEvents(_ context.Context, e []store.ProductEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e...)
	return nil
}

func (f *fakeSink) WriteAppViews(_ context.Context, v []store.AppView) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views = append(f.views, v...)
	return nil
}

func (f *fakeSink) viewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.views)
}

func cfg(max int, interval time.Duration, cap int) config.BufferConfig {
	return config.BufferConfig{FlushMaxEvents: max, FlushInterval: interval, Capacity: cap}
}

func TestFlushBySize(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(3, time.Hour, 100), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	for i := 0; i < 3; i++ {
		b.EnqueueHit(store.WebHit{ID: "h"})
	}
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.hits) == 3 })
	cancel()
	<-done
}

func TestFlushByInterval(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(1000, 30*time.Millisecond, 100), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	b.EnqueueEvent(store.ProductEvent{ID: "e"})
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.events) == 1 })
	cancel()
	<-done
}

func TestShutdownFlushesRemaining(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(1000, time.Hour, 100), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	b.EnqueueHit(store.WebHit{ID: "h"})
	time.Sleep(10 * time.Millisecond) // let worker pick it up
	cancel()
	<-done
	if len(sink.hits) != 1 {
		t.Fatalf("shutdown must flush; got %d hits", len(sink.hits))
	}
}

func TestOverflowDropsOldest(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(1000, time.Hour, 2), sink, slog.Default())
	// No worker running: fill beyond capacity.
	b.EnqueueHit(store.WebHit{ID: "1"})
	b.EnqueueHit(store.WebHit{ID: "2"})
	b.EnqueueHit(store.WebHit{ID: "3"}) // drops "1"
	if b.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", b.Dropped())
	}
}

func TestFlushRetriesThenDrops(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { retryDelays = old }()
	sink := &fakeSink{fail: 2} // fails twice, succeeds on 3rd
	b := New(cfg(1, time.Hour, 10), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	b.EnqueueHit(store.WebHit{ID: "h"})
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.hits) == 1 })
	cancel()
	<-done
}

func TestFlushExhaustedRetriesDropsAndWorkerContinues(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { retryDelays = old }()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	sink := &fakeSink{fail: 10} // more failures than attempts: always fails
	b := New(cfg(1, time.Hour, 10), sink, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	b.EnqueueHit(store.WebHit{ID: "dropped"})
	// Wait for all 4 attempts (1 + 3 retries) to have been made, i.e. the
	// batch has been given up on.
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return sink.calls == 4 })
	// The batch must never land, even after the retries are exhausted, and
	// must not be requeued/resurrected by a later successful flush.
	sink.mu.Lock()
	if len(sink.hits) != 0 {
		t.Fatalf("dropped batch must not be written; got %d hits", len(sink.hits))
	}
	sink.fail = 0 // clear failures so the next flush succeeds
	sink.mu.Unlock()

	// Worker must still be running and able to flush a subsequent hit.
	b.EnqueueHit(store.WebHit{ID: "ok"})
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.hits) == 1 })
	sink.mu.Lock()
	if sink.hits[0].ID != "ok" {
		t.Fatalf("expected surviving hit to be %q, got %q", "ok", sink.hits[0].ID)
	}
	sink.mu.Unlock()

	cancel()
	<-done

	if !strings.Contains(buf.String(), "batch dropped") {
		t.Fatalf("expected error log to contain %q, got: %s", "batch dropped", buf.String())
	}
}

func TestShutdownDuringRetriesDoesNotStall(t *testing.T) {
	old := retryDelays
	// Large delays: if the backoff sleep weren't cancellation-aware, a
	// cancel during the first sleep would stall shutdown for ~30s.
	retryDelays = []time.Duration{10 * time.Second, 10 * time.Second, 10 * time.Second}
	defer func() { retryDelays = old }()

	sink := &fakeSink{fail: 10} // always fails, forcing the worker into backoff
	b := New(cfg(1, time.Hour, 10), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	b.EnqueueHit(store.WebHit{ID: "h"})
	// Let the worker pick up the item and enter its first (long) backoff
	// sleep before we cancel.
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return sink.calls >= 1 })

	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation; backoff sleep is not cancellation-aware")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %s, want well under configured backoff", elapsed)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

func TestFlushesAppViewsBySize(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(2, time.Hour, 100), sink, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	b.EnqueueAppView(store.AppView{ID: "1", Project: "p", Screen: "/a"})
	b.EnqueueAppView(store.AppView{ID: "2", Project: "p", Screen: "/b"})

	deadline := time.Now().Add(2 * time.Second)
	for sink.viewCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := sink.viewCount(); got != 2 {
		t.Errorf("app views written = %d, want 2", got)
	}
	cancel()
	<-done
}

func TestShutdownDrainsAppViews(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(1000, time.Hour, 100), sink, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	b.EnqueueAppView(store.AppView{ID: "1", Project: "p", Screen: "/a"})
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if got := sink.viewCount(); got != 1 {
		t.Errorf("app views written on shutdown = %d, want 1", got)
	}
}

// The size trigger counts all three kinds together: a buffer holding one of
// each must flush at FlushMaxEvents=3, not wait for three of any one kind.
func TestFlushSizeCountsAllKinds(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(3, time.Hour, 100), sink, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	b.EnqueueHit(store.WebHit{ID: "h", Project: "p"})
	b.EnqueueEvent(store.ProductEvent{ID: "e", Project: "p", EventName: "n"})
	b.EnqueueAppView(store.AppView{ID: "v", Project: "p", Screen: "/a"})

	deadline := time.Now().Add(2 * time.Second)
	for sink.viewCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	sink.mu.Lock()
	h, e, v := len(sink.hits), len(sink.events), len(sink.views)
	sink.mu.Unlock()
	if h != 1 || e != 1 || v != 1 {
		t.Errorf("flushed hits=%d events=%d views=%d; want 1 1 1", h, e, v)
	}
	cancel()
	<-done
}
