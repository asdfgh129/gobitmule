package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// Use the production 500ms window with a long settle timer so overlapping
// submissions deterministically join one batch even on a loaded CI runner.
func batchingStore(t *testing.T) *Store {
	t.Helper()
	st := newTestStore(t)
	st.meta.settle = 250 * time.Millisecond
	st.meta.window = 500 * time.Millisecond
	return st
}

// The metadata path must actually coalesce: eight writes in one window produce
// ONE transaction, not eight. This is the substance of docs/06 §10's last P0
// deviation being retired.
func TestMetaBatchCoalescesConcurrentWrites(t *testing.T) {
	st := batchingStore(t)
	ctx := context.Background()
	ids := []core.TaskID{mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/a"),
		mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/b")}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = st.SetOption(ctx, ids[i%2], "k"+string(rune('a'+i)), "v")
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	stats := st.MetaBatchStats()
	if stats.Batches != 1 || stats.Ops != 8 || stats.MaxBatch != 8 {
		t.Fatalf("stats = %+v, want 1 batch of 8 ops", stats)
	}
}

// An isolated PATCH must not wait for the full window: the settle timer commits
// it in milliseconds. Batching is for bursts, not a fixed 500ms tax.
func TestMetaBatchIsolatedWriteIsFast(t *testing.T) {
	st := newTestStore(t)
	id := mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/a")
	start := time.Now()
	if err := st.SaveOptions(context.Background(), id, core.TaskOptions{Dir: "/dl"}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("isolated metadata write took %v", d)
	}
	if s := st.MetaBatchStats(); s.Batches != 1 || s.Ops != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

// Two rewrites of the same task within a window must collapse to the newer
// value; both callers still see a successful durable COMMIT.
func TestMetaBatchLastWriterWins(t *testing.T) {
	st := batchingStore(t)
	id := mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/a")
	ctx := context.Background()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = st.SaveOptions(ctx, id, core.TaskOptions{Dir: "/old"}) }()
	time.Sleep(10 * time.Millisecond)
	go func() { defer wg.Done(); errs[1] = st.SaveOptions(ctx, id, core.TaskOptions{Dir: "/new"}) }()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got, err := st.GetTask(ctx, id)
	if err != nil || got.Options.Dir != "/new" {
		t.Fatalf("task options = %+v, %v; want /new", got, err)
	}
	if s := st.MetaBatchStats(); s.Batches != 1 || s.Ops != 1 || s.Calls != 2 {
		t.Fatalf("stats = %+v, want one batch, one row-write, two calls", s)
	}
}

// A failed COMMIT is reported to every caller in the batch, including the ones
// whose own mutation was replaced by a later one.
func TestMetaBatchErrorReachesAllCallers(t *testing.T) {
	st := batchingStore(t)
	boom := errors.New("boom")
	fn := func(context.Context, *sql.Tx) error { return boom }
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = st.meta.submit(context.Background(), "same-key", fn) }()
	time.Sleep(10 * time.Millisecond)
	go func() { defer wg.Done(); errs[1] = st.meta.submit(context.Background(), "same-key", fn) }()
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("caller %d got %v, want boom", i, err)
		}
	}
}

// Cancelling one waiter must not drop the other callers' writes: the shared
// transaction uses a context detached from any individual request.
func TestMetaBatchDetachesSharedCommitFromCallerCancel(t *testing.T) {
	st := batchingStore(t)
	taskID := mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/c")
	ctx, cancel := context.WithCancel(context.Background())
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = st.meta.submit(ctx, "first", func(context.Context, *sql.Tx) error { return nil })
	}()
	time.Sleep(10 * time.Millisecond)
	go func() { defer wg.Done(); errs[1] = st.SetOption(context.Background(), taskID, "k", "v") }()
	cancel()
	wg.Wait()
	if errs[1] != nil {
		t.Fatalf("other caller failed: %v", errs[1])
	}
	v, err := st.Option(context.Background(), taskID, "k")
	if err != nil || v != "v" {
		t.Fatalf("option = %q, %v; want v", v, err)
	}
}

// The size limit is a second, deterministic flush trigger: a long burst must
// not wait a full window when the batch is already large.
func TestMetaBatchLimitForcesFlush(t *testing.T) {
	st := newTestStore(t)
	taskID := mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/d")
	st.meta.settle = time.Hour
	st.meta.limit = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = st.SetOption(context.Background(), taskID, string(rune('a'+i)), "v")
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if s := st.MetaBatchStats(); s.Batches != 1 || s.Ops != 4 {
		t.Fatalf("stats = %+v, want one batch of four", s)
	}
}

// Closing the store must drain a still-buffered write instead of losing it.
func TestMetaBatchCloseDrainsPendingWrite(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.db"
	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.meta.settle = time.Hour
	taskID := mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/e")
	done := make(chan error, 1)
	go func() {
		done <- st.SetOption(context.Background(), taskID, "k", "durable")
	}()
	time.Sleep(20 * time.Millisecond)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("pending write failed: %v", err)
	}
	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if v, err := reopened.Option(context.Background(), taskID, "k"); err != nil || v != "durable" {
		t.Fatalf("after reopen option = %q, %v", v, err)
	}
}

// Batching must not weaken the docs/06 §6 redaction boundary: the batched
// SaveResource still drops credential headers before they reach SQLite.
func TestBatchedSaveResourceStripsSensitiveHeaders(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id := mustInsertTask(t, st, core.StatePending, core.InputURL, "https://x/a")
	res := &core.Resource{ID: "res-batch", Kind: core.KindSingle, Engine: core.EngineGo,
		Headers: map[string]string{"Referer": "https://x/", "Authorization": "Bearer SECRET", "Cookie": "sid=SECRET"}}
	if err := st.SaveResource(ctx, id, res); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.DB().QueryRowContext(ctx, `SELECT headers FROM resources WHERE id=?`, res.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "SECRET") || strings.Contains(raw, "Authorization") || strings.Contains(raw, "Cookie") {
		t.Fatalf("sensitive header reached DB: %s", raw)
	}
	got, err := st.GetResource(ctx, res.ID)
	if err != nil || got.Headers["Referer"] != "https://x/" || len(got.Headers) != 1 {
		t.Fatalf("resource headers = %+v, %v", got, err)
	}
}
