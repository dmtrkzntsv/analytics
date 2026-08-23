package pipeline

import (
	"context"
	"errors"
	"log/slog"
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

func cfg(max int, interval time.Duration, cap int) config.BufferConfig {
	return config.BufferConfig{FlushMaxEvents: max, FlushInterval: config.Duration{Duration: interval}, Capacity: cap}
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
