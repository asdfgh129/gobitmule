package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/asdfgh129/ud/internal/core"
)

// withFlush overrides the ADR-0015 ③ flush knobs for one test.
func withFlush(t *testing.T, window time.Duration, bytes int64) {
	t.Helper()
	oldWindow, oldBytes := chunkFlushWindow, chunkFlushBytes
	chunkFlushWindow, chunkFlushBytes = window, bytes
	t.Cleanup(func() { chunkFlushWindow, chunkFlushBytes = oldWindow, oldBytes })
}

// No timer, no byte trigger: the completion stays buffered until Sync. This is
// the behaviour ADR-0015 asks for (batch rather than one transaction per chunk)
// and it proves the writer no longer commits on every chunk.
func TestChunkWriterBatchesUntilSync(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	c, ok, err := w.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("next: ok=%v err=%v", ok, err)
	}
	if err := w.WriteChunk(ctx, c, []byte("AAAA")); err != nil {
		t.Fatal(err)
	}
	if task, _ := st.GetTask(ctx, id); task.BytesDone != 0 {
		t.Fatalf("chunk was committed immediately (bytes_done=%d); batching is not in effect", task.BytesDone)
	}
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	if task, _ := st.GetTask(ctx, id); task.BytesDone != 4 {
		t.Fatalf("sync did not persist the buffered completion: bytes_done=%d", task.BytesDone)
	}
}

// A full flush window of completions must reach SQLite without waiting for Sync,
// otherwise a long download would show no progress for its whole duration.
func TestChunkWriterFlushesOnByteThreshold(t *testing.T) {
	withFlush(t, time.Hour, 4) // one 4-byte chunk is exactly one window
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	c, ok, err := w.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("next: ok=%v err=%v", ok, err)
	}
	if err := w.WriteChunk(ctx, c, []byte("AAAA")); err != nil {
		t.Fatal(err)
	}
	if task, _ := st.GetTask(ctx, id); task.BytesDone != 4 {
		t.Fatalf("byte threshold did not flush: bytes_done=%d", task.BytesDone)
	}
}

// The timer bounds how long a completion may stay in memory when chunks arrive
// slowly and never reach the byte threshold.
func TestChunkWriterTimerFlushesSlowTrickle(t *testing.T) {
	withFlush(t, 25*time.Millisecond, 1<<20)
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	c, ok, err := w.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("next: ok=%v err=%v", ok, err)
	}
	if err := w.WriteChunk(ctx, c, []byte("AAAA")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		task, err := st.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.BytesDone == 4 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timer flush never happened: bytes_done=%d after 3s", task.BytesDone)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Regression: verified is a transition on top of done. With batching enabled the
// completion row may still be buffered when the transport verifies the chunk, so
// Verified must flush first — otherwise the UPDATE ... WHERE state='assigned'
// matches nothing and the run fails with a bogus "not in assigned state".
func TestChunkWriterVerifiedFlushesBufferedCompletionFirst(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	c, ok, err := w.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("next: ok=%v err=%v", ok, err)
	}
	if err := w.WriteChunk(ctx, c, []byte("AAAA")); err != nil {
		t.Fatal(err)
	}
	if err := w.Verified(ctx, c, core.Hash{}); err != nil {
		t.Fatalf("verified must flush the buffered completion first: %v", err)
	}
	chunks, err := st.Chunks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if chunks[0].State != core.ChunkVerified || !chunks[0].HashOK {
		t.Fatalf("state=%s hashOK=%v want verified/true", chunks[0].State, chunks[0].HashOK)
	}
	if task, _ := st.GetTask(ctx, id); task.BytesDone != 4 {
		t.Fatalf("bytes_done=%d want 4", task.BytesDone)
	}
}

// The docs/06 §10 deviation claimed task_options / headers were rewritten per
// chunk. They are not, and this test prevents a future change from quietly
// reintroducing that hot-path write: chunk completions must only touch chunks,
// checksums and tasks.bytes_done, never the metadata batcher.
func TestChunkWriterDoesNotWriteMetadata(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	before := st.MetaBatchStats().Calls
	for i := 0; i < 2; i++ {
		c, ok, err := w.Next(ctx)
		if err != nil || !ok {
			t.Fatalf("next: ok=%v err=%v", ok, err)
		}
		if err := w.WriteChunk(ctx, c, []byte("AAAA")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := st.MetaBatchStats().Calls; got != before {
		t.Fatalf("chunk path issued %d metadata writes (before=%d)", got-before, before)
	}
	if task, _ := st.GetTask(ctx, id); task.BytesDone != 8 {
		t.Fatalf("bytes_done = %d, want 8", task.BytesDone)
	}
}
