package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/store"
)

// ADR-0015 decision ③: progress is flushed in batches, not per chunk. Four
// megabytes of chunk completions or 500ms of wall clock, whichever comes first.
// A crash may therefore lose the completion records of one window; recovery
// resets those `assigned` chunks to `pending`, so the cost is a re-download of
// at most one window — never a corrupt file and never a double-counted
// bytes_done (the batch and the progress delta share one transaction).
var (
	chunkFlushWindow = 500 * time.Millisecond
	chunkFlushBytes  = int64(4 << 20)
)

// chunkWriter is the scheduler-owned implementation of core.ChunkWriter.
// The .part file is the only place bytes go; all state lives in SQLite.
type chunkWriter struct {
	st          *store.Store
	taskID      core.TaskID
	resourceID  string
	tempPath    string
	f           *os.File
	mu          sync.Mutex
	bus         *core.EventBus
	maxAttempts int
	onWrite     func(int64)
	lastEvent   time.Time

	// Batch state (ADR-0015 ③). pending holds chunk completions that the .part
	// file already has but SQLite does not yet know about.
	pending      []store.ChunkDone
	pendingBytes int64
	flushWindow  time.Duration
	flushBytes   int64
	flushErr     error
	stopFlush    chan struct{}
	flushStopped chan struct{}
	closeOnce    sync.Once
	closeErr     error
}

func newChunkWriter(st *store.Store, taskID core.TaskID, resourceID, tempPath string,
	total int64, bus *core.EventBus, maxAttempts int, onWrite func(int64)) (*chunkWriter, error) {
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		return nil, core.Err(core.E_STORAGE_FULL, "create output directory: %v", err)
	}
	f, err := os.OpenFile(tempPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, core.Err(core.E_STORAGE_FULL, "open .part file: %v", err)
	}
	if total > 0 {
		if err := f.Truncate(total); err != nil {
			f.Close()
			return nil, core.Err(core.E_STORAGE_FULL, "preallocate: %v", err)
		}
	}
	w := &chunkWriter{
		st: st, taskID: taskID, resourceID: resourceID, tempPath: tempPath,
		f: f, bus: bus, maxAttempts: maxAttempts, onWrite: onWrite,
		flushWindow: chunkFlushWindow, flushBytes: chunkFlushBytes,
		stopFlush: make(chan struct{}), flushStopped: make(chan struct{}),
	}
	go w.flushLoop()
	return w, nil
}

// flushLoop bounds the time a completion record can stay in memory. Without it a
// slow trickle of small chunks would never reach the byte threshold and the
// progress row would lag indefinitely.
func (w *chunkWriter) flushLoop() {
	defer close(w.flushStopped)
	window := w.flushWindow
	if window <= 0 {
		return
	}
	t := time.NewTicker(window)
	defer t.Stop()
	for {
		select {
		case <-w.stopFlush:
			return
		case <-t.C:
			if err := w.flushPending(context.Background()); err != nil {
				// No caller to return to: remember it so the next WriteChunk or
				// Sync fails the task instead of silently losing completions.
				w.mu.Lock()
				if w.flushErr == nil {
					w.flushErr = err
				}
				w.mu.Unlock()
			}
		}
	}
}

// flushPending commits every buffered completion in one transaction.
func (w *chunkWriter) flushPending(ctx context.Context) error {
	w.mu.Lock()
	if len(w.pending) == 0 {
		err := w.flushErr
		w.mu.Unlock()
		return err
	}
	batch := w.pending
	w.pending = nil
	w.pendingBytes = 0
	err := w.flushErr
	w.mu.Unlock()
	if err != nil {
		// The batch is gone with the error already recorded; do not retry it,
		// because a partially applied transaction is indistinguishable here.
		return err
	}
	// Chunk bookkeeping must survive an intentional cancel, exactly like Release.
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if werr := w.st.WriteChunkDoneBatch(dbCtx, batch); werr != nil {
		w.mu.Lock()
		if w.flushErr == nil {
			w.flushErr = werr
		}
		w.mu.Unlock()
		return werr
	}
	return nil
}

func (w *chunkWriter) Next(ctx context.Context) (core.Chunk, bool, error) {
	return w.st.Next(ctx, w.taskID)
}

func (w *chunkWriter) WriteChunk(ctx context.Context, c core.Chunk, p []byte) error {
	if int64(len(p)) != c.Length {
		return core.Err(core.E_INTERNAL, "chunk %d byte count mismatch: want %d got %d", c.Index, c.Length, len(p))
	}
	w.mu.Lock()
	_, err := w.f.WriteAt(p, c.Start)
	w.mu.Unlock()
	if err != nil {
		return core.Err(core.E_STORAGE_FULL, "write chunk %d: %v", c.Index, err)
	}
	sum := sha256.Sum256(p)

	w.mu.Lock()
	if w.flushErr != nil {
		err := w.flushErr
		w.mu.Unlock()
		return err
	}
	w.pending = append(w.pending, store.ChunkDone{
		Chunk: c, ResourceID: w.resourceID, Digest: hex.EncodeToString(sum[:]), MirrorIndex: c.MirrorIndex,
	})
	w.pendingBytes += c.Length
	due := w.pendingBytes >= w.flushBytes
	w.mu.Unlock()
	if due {
		if ferr := w.flushPending(ctx); ferr != nil {
			return ferr
		}
	}
	if w.onWrite != nil {
		w.onWrite(c.Length)
	}
	w.publishProgress(ctx)
	return nil
}

func (w *chunkWriter) Verified(ctx context.Context, c core.Chunk, _ core.Hash) error {
	// Ordering: verified is a transition on top of done, so the buffered
	// completion for this chunk must reach SQLite before the transition runs.
	if err := w.flushPending(ctx); err != nil {
		return err
	}
	return w.st.Verified(ctx, c)
}

func (w *chunkWriter) Release(ctx context.Context, c core.Chunk, err error) error {
	e := core.AsError(err)
	code := core.E_INTERNAL
	if e != nil {
		code = e.Code
	}
	w.bus.Publish(core.Event{
		Type:   core.EventTaskChunkFailed,
		TaskID: w.taskID,
		Payload: map[string]any{
			"chunk": c.Index, "attempt": c.Attempt, "mirror": c.MirrorIndex, "errorCode": code,
		},
	})
	// Chunk bookkeeping must survive an intentional cancel (pause/kill).
	// A cancellation is never a terminal chunk failure: the bytes simply were
	// not finished and must be retried after resume / crash recovery.
	dbCtx := context.WithoutCancel(ctx)
	cancelled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
	retry := cancelled || (core.IsRetryable(err) && c.Attempt < w.maxAttempts)
	return w.st.Release(dbCtx, c, retry)
}

func (w *chunkWriter) publishProgress(ctx context.Context) {
	w.mu.Lock()
	if time.Since(w.lastEvent) < 500*time.Millisecond {
		w.mu.Unlock()
		return
	}
	w.lastEvent = time.Now()
	w.mu.Unlock()
	done, total := w.currentProgress(ctx)
	w.bus.Publish(core.Event{
		Type:    core.EventTaskProgress,
		TaskID:  w.taskID,
		Payload: map[string]any{"bytesDone": done, "bytesTotal": total},
	})
}

func (w *chunkWriter) currentProgress(ctx context.Context) (int64, int64) {
	t, err := w.st.GetTask(ctx, w.taskID)
	if err != nil {
		return 0, 0
	}
	return t.BytesDone, t.BytesTotal
}

// Sync persists the buffered completion records, then fsyncs the .part file
// before the atomic rename. Database first: if the process dies between the two
// steps the file is still not renamed, so the recorded chunks stay recoverable.
func (w *chunkWriter) Sync() error {
	if err := w.flushPending(context.Background()); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Sync()
}

// Close stops the flush loop, persists anything still buffered, then closes the
// .part file. Idempotent: the scheduler calls it once, recovery paths may call
// it again on the same writer.
func (w *chunkWriter) Close() error {
	w.closeOnce.Do(func() {
		if w.stopFlush != nil {
			close(w.stopFlush)
			<-w.flushStopped
		}
		ferr := w.flushPending(context.Background())
		w.mu.Lock()
		cerr := w.f.Close()
		w.mu.Unlock()
		if ferr != nil {
			w.closeErr = ferr
			return
		}
		w.closeErr = cerr
	})
	return w.closeErr
}
