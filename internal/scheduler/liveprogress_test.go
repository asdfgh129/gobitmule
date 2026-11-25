package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// The eD2k engine downloads into its own piece manager before the reader
// adapter can copy chunks, so without a progress sink the REST task would sit
// at bytesDone=0 for the whole transfer. ReportProgress carries those engine
// bytes into the scheduler's live layer ONLY: bytes_done in SQLite must stay
// the count of committed chunks (single source of truth).
func TestChunkWriterReportProgressIsLiveOnly(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var live int64
	w.onEngineProgress = func(done int64) { live = done }

	w.ReportProgress(6)
	if live != 6 {
		t.Fatalf("live progress = %d, want 6", live)
	}
	// Engine bytes can overshoot the declared size (piece padding); the writer
	// must clamp so the UI never reports more than 100%.
	w.ReportProgress(999)
	if live != 8 {
		t.Fatalf("live progress must clamp to total, got %d", live)
	}
	// Negative counters (no data yet) collapse to zero instead of underflowing.
	w.ReportProgress(-5)
	if live != 0 {
		t.Fatalf("negative progress must clamp to 0, got %d", live)
	}
	if task, err := st.GetTask(ctx, id); err != nil || task.BytesDone != 0 {
		t.Fatalf("engine bytes leaked into bytes_done: %d (%v)", task.BytesDone, err)
	}
}

// A progress event is what refreshes the task list without polling; it must be
// published, throttled to the 500ms window, and carry the task-level totals.
func TestChunkWriterReportProgressEmitsThrottledEvent(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	bus := core.NewEventBus(16)
	events, cancel := bus.Subscribe()
	defer cancel()
	w, err := newChunkWriter(st, id, res.ID, part, 8, bus, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Multi-file shape: this writer owns file 2 of a 100-byte task and 40 bytes
	// were already downloaded by file 1.
	w.progressBase = 40
	w.taskTotal = 100

	w.ReportProgress(10)
	select {
	case e := <-events:
		if e.Type != core.EventTaskProgress || e.TaskID != id {
			t.Fatalf("unexpected event: %+v", e)
		}
		done, _ := e.Payload["bytesDone"].(int64)
		total, _ := e.Payload["bytesTotal"].(int64)
		// 10 is clamped to this file's own 8 bytes, so 40+8 = 48 of the task's 100.
		if done != 48 || total != 100 {
			t.Fatalf("payload = %d/%d, want 48/100", done, total)
		}
	case <-time.After(time.Second):
		t.Fatal("no progress event published")
	}

	// Second call inside the throttle window must not publish again.
	w.ReportProgress(20)
	select {
	case e := <-events:
		t.Fatalf("progress event was not throttled: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// applyLiveProgress overlays the engine counter on the SQLite row returned by
// Get/List: it can only move bytesDone forward, never past bytesTotal, and it
// leaves bytesDone alone when the DB is already ahead (committed chunks win).
func TestApplyLiveProgressOverlay(t *testing.T) {
	s := &Scheduler{liveProgress: make(map[core.TaskID]int64)}

	task := &core.Task{ID: "t1", BytesDone: 100, BytesTotal: 1000}
	s.applyLiveProgress(task)
	if task.BytesDone != 100 {
		t.Fatalf("task without live progress changed: %d", task.BytesDone)
	}

	s.setLiveProgress("t1", 400)
	s.applyLiveProgress(task)
	if task.BytesDone != 400 {
		t.Fatalf("live progress not applied: %d", task.BytesDone)
	}

	// A committed-chunk count above the engine counter must not be rolled back.
	task.BytesDone = 600
	s.applyLiveProgress(task)
	if task.BytesDone != 600 {
		t.Fatalf("live progress rolled back committed bytes: %d", task.BytesDone)
	}

	// The engine cannot report more than the task total.
	s.setLiveProgress("t1", 5000)
	task.BytesDone = 100
	s.applyLiveProgress(task)
	if task.BytesDone != 1000 {
		t.Fatalf("live progress must clamp to bytesTotal, got %d", task.BytesDone)
	}

	// Cleaning up on terminal state must drop the overlay.
	s.clearLiveProgress("t1")
	task.BytesDone = 100
	s.applyLiveProgress(task)
	if task.BytesDone != 100 {
		t.Fatalf("cleared live progress still applied: %d", task.BytesDone)
	}

	// Negative reports collapse to zero rather than underflowing.
	s.setLiveProgress("t1", -1)
	if got := s.liveProgress["t1"]; got != 0 {
		t.Fatalf("negative live progress stored as %d", got)
	}
}
