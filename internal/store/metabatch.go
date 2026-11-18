package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// docs/06 §7 / §10: metadata writes (task_options, resources.headers) are
// aggregated into one transaction instead of one transaction per write. The
// window is capped at metaBatchWindow; an isolated write settles in
// metaBatchSettle so a single PATCH /tasks/{id} does not pay 500ms.
//
// Unlike a fire-and-forget queue, every submitter waits for the COMMIT result.
// That keeps the API honest: a PATCH only returns 200 after its options are
// durable, and a failed transaction is returned to every caller whose write
// was in the batch. Cancelling one caller does not cancel the shared commit.
const (
	metaBatchWindow = 500 * time.Millisecond
	metaBatchSettle = 2 * time.Millisecond
	metaBatchLimit  = 64
)

type metaRequest struct {
	key   string // dedupe key: the last write to one row wins
	apply func(context.Context, *sql.Tx) error
	dones []chan error
}

// MetaBatchStats is the observable proof that writes were actually coalesced
// (used by tests; not a public API surface).
type MetaBatchStats struct {
	Batches  uint64 // number of COMMITs
	Calls    uint64 // number of submit calls (deduped calls count too)
	Ops      uint64 // number of row-writes actually applied
	MaxBatch uint64
}

type metaBatcher struct {
	store   *Store
	mu      sync.Mutex
	flushMu sync.Mutex // serialises flush callers so Close cannot outrun a timer commit
	pending []*metaRequest
	byKey   map[string]*metaRequest
	timer   *time.Timer
	window  time.Duration
	settle  time.Duration
	limit   int
	first   time.Time
	closed  bool
	stats   MetaBatchStats // written under mu
}

func newMetaBatcher(s *Store) *metaBatcher {
	return &metaBatcher{store: s, byKey: map[string]*metaRequest{},
		window: metaBatchWindow, settle: metaBatchSettle, limit: metaBatchLimit}
}

// submit queues one metadata mutation. A second mutation for the same key
// replaces the first (last writer wins) but BOTH callers receive the outcome
// of the committed write, so nobody is left waiting on a channel forever.
func (b *metaBatcher) submit(ctx context.Context, key string, fn func(context.Context, *sql.Tx) error) error {
	done := make(chan error, 1)
	b.mu.Lock()
	b.stats.Calls++
	if b.closed {
		b.mu.Unlock()
		return errors.New("store: metadata batcher is closed")
	}
	if prev := b.byKey[key]; prev != nil {
		prev.apply = fn
		prev.dones = append(prev.dones, done)
		b.armLocked(b.remainingLocked())
		b.mu.Unlock()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req := &metaRequest{key: key, apply: fn, dones: []chan error{done}}
	b.pending = append(b.pending, req)
	b.byKey[key] = req
	if len(b.pending) == 1 {
		b.first = time.Now()
		b.armLocked(b.settle)
	} else {
		b.armLocked(b.remainingLocked())
	}
	lead := len(b.pending) >= b.limit
	b.mu.Unlock()

	if lead {
		b.flush()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The shared commit still runs: only this waiter goes away.
		return ctx.Err()
	}
}

// remainingLocked never exceeds the 500ms window measured from the first
// request in the current batch.
func (b *metaBatcher) remainingLocked() time.Duration {
	if b.first.IsZero() {
		return b.settle
	}
	d := b.window - time.Since(b.first)
	if d < time.Millisecond {
		return time.Millisecond
	}
	return d
}

func (b *metaBatcher) armLocked(d time.Duration) {
	if b.timer == nil {
		b.timer = time.AfterFunc(d, b.flush)
		return
	}
	b.timer.Stop()
	b.timer.Reset(d)
}

// flush commits the whole batch in one transaction. A transient BUSY replay
// inside withTx is safe because every apply function is a self-contained
// upsert (see tasks.go / resources.go).
func (b *metaBatcher) flush() {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()
	b.flushOne()
}

func (b *metaBatcher) flushOne() {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.pending
	b.pending = nil
	b.byKey = map[string]*metaRequest{}
	b.first = time.Time{}
	b.stats.Batches++
	b.stats.Ops += uint64(len(batch))
	if uint64(len(batch)) > b.stats.MaxBatch {
		b.stats.MaxBatch = uint64(len(batch))
	}
	b.mu.Unlock()

	// One caller's cancellation must not discard the other callers' writes.
	dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := b.store.withTx(dbCtx, func(tx *sql.Tx) error {
		for _, req := range batch {
			if err := req.apply(dbCtx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	for _, req := range batch {
		for _, ch := range req.dones {
			ch <- err
			close(ch)
		}
	}
}

// close rejects new submissions and drains what is still buffered. Called by
// Store.Close before the database handle goes away.
func (b *metaBatcher) close() {
	b.mu.Lock()
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
	b.flush()
}

func (s *Store) MetaBatchStats() MetaBatchStats {
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	return s.meta.stats
}
