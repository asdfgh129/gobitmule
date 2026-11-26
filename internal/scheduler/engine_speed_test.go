package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// The eD2k engine owns the .part file and reports a running byte total through
// ReportProgress; it never calls WriteChunk while downloading. Speed was
// therefore always 0 for eD2k tasks even though bytes were plainly arriving.
// Only the delta between two reports is traffic — the first report is a
// baseline (it can include bytes resumed from an earlier run) and a regression
// (piece restore) must not be credited as negative traffic.
func TestEngineProgressFeedsSpeedCounter(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	w, err := newChunkWriter(st, id, res.ID, part, 1_000_000, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var credited []int64
	w.onWrite = func(n int64) { credited = append(credited, n) }

	// Only the delta between two reports is traffic. The first report is a
	// baseline (it can include bytes resumed from an earlier run), an unchanged
	// report is not traffic, and a regression (piece restore) resets the
	// baseline instead of turning into negative traffic — the bytes the engine
	// re-fetches afterwards are real network traffic again.
	w.ReportProgress(0) // baseline
	w.ReportProgress(300_000)
	w.ReportProgress(300_000) // unchanged: no traffic
	w.ReportProgress(120_000) // restore: drops the baseline to 120000
	w.ReportProgress(500_000)

	want := []int64{300_000, 380_000}
	if len(credited) != len(want) {
		t.Fatalf("credited = %v, want %v", credited, want)
	}
	for i := range want {
		if credited[i] != want[i] {
			t.Fatalf("credited = %v, want %v", credited, want)
		}
	}
}

// Once the engine has reported progress, the scheduler re-reads the finished
// file through WriteChunk. Those bytes already reached the speed counter as
// network traffic, so the copy must not be credited a second time.
func TestEnginePathDoesNotDoubleCreditChunkWrites(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var total int64
	w.onWrite = func(n int64) { total += n }

	w.ReportProgress(0) // baseline
	w.ReportProgress(8) // 8 network bytes

	c, ok, err := w.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("claim chunk: ok=%t err=%v", ok, err)
	}
	if err := w.WriteChunk(ctx, c, make([]byte, c.Length)); err != nil {
		t.Fatal(err)
	}
	if total != 8 {
		t.Fatalf("speed credit = %d, want 8 (engine delta only)", total)
	}
}

// A plain HTTP transport never reports engine progress, so its chunk writes
// must keep feeding the counter exactly as before.
func TestChunkWritesStillCreditSpeedWithoutEngine(t *testing.T) {
	withFlush(t, time.Hour, 1<<20)
	st, id, res, part := newWriterFixture(t)
	ctx := context.Background()
	w, err := newChunkWriter(st, id, res.ID, part, 8, core.NewEventBus(8), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var total int64
	w.onWrite = func(n int64) { total += n }

	c, ok, err := w.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("claim chunk: ok=%t err=%v", ok, err)
	}
	if err := w.WriteChunk(ctx, c, make([]byte, c.Length)); err != nil {
		t.Fatal(err)
	}
	if total != c.Length {
		t.Fatalf("speed credit = %d, want %d", total, c.Length)
	}
}
