package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/store"
)

func janitorTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func insertEventTS(t *testing.T, st *store.Store, ts int64) {
	t.Helper()
	if err := st.InsertEvent(context.Background(),
		core.Event{Type: core.EventTaskCreated, Ts: ts}); err != nil {
		t.Fatalf("insert event ts=%d: %v", ts, err)
	}
}

func eventsOf(t *testing.T, st *store.Store) []core.Event {
	t.Helper()
	evs, err := st.Events(context.Background(), 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return evs
}

// The sweep must apply the configured age window, using the injected clock so
// the assertion does not depend on wall time.
func TestEventJanitorSweepEnforcesAgeRetention(t *testing.T) {
	st := janitorTestStore(t)
	defer st.Close()

	now := time.UnixMilli(1_700_000_000_000)
	insertEventTS(t, st, now.Add(-30*24*time.Hour).UnixMilli())
	insertEventTS(t, st, now.Add(-8*24*time.Hour).UnixMilli())
	insertEventTS(t, st, now.Add(-1*time.Hour).UnixMilli())

	j := newEventJanitor(st, config.EventsConfig{RetentionDays: 7, MaxRows: 100}, quietLogger())
	j.now = func() time.Time { return now }

	res, err := j.sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.DeletedByAge != 2 || res.Remaining != 1 {
		t.Fatalf("result = %+v, want DeletedByAge=2 Remaining=1", res)
	}
	if evs := eventsOf(t, st); len(evs) != 1 || evs[0].Ts != now.Add(-1*time.Hour).UnixMilli() {
		t.Fatalf("remaining events = %+v, want only the 1h-old one", evs)
	}
}

// run() must sweep immediately (not one interval later) and must return once
// its context is cancelled — otherwise daemon shutdown would leak it.
func TestEventJanitorRunSweepsThenStops(t *testing.T) {
	st := janitorTestStore(t)
	defer st.Close()

	old := time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	insertEventTS(t, st, old)

	j := newEventJanitor(st, config.EventsConfig{RetentionDays: 7, MaxRows: 100}, quietLogger())
	j.interval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		j.run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(eventsOf(t, st)) == 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("janitor did not prune the stale event")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor did not stop after context cancellation")
	}
}

// A store-level failure must be reported, never fatal: retention is best-effort
// and the next tick retries.
func TestEventJanitorSweepFailureIsReported(t *testing.T) {
	st := janitorTestStore(t)
	j := newEventJanitor(st, config.EventsConfig{RetentionDays: 7, MaxRows: 100}, quietLogger())
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := j.sweep(context.Background()); err == nil {
		t.Fatal("expected an error when the store is closed")
	}
}
