package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

func openEventsStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedEvents(t *testing.T, st *Store, ts ...int64) {
	t.Helper()
	for _, v := range ts {
		if err := st.InsertEvent(context.Background(), core.Event{
			Type: core.EventTaskCreated, Ts: v,
		}); err != nil {
			t.Fatalf("insert event ts=%d: %v", v, err)
		}
	}
}

// remainingEventTS returns the surviving event timestamps, oldest first.
func remainingEventTS(t *testing.T, st *Store) []int64 {
	t.Helper()
	evs, err := st.Events(context.Background(), 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	out := make([]int64, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Ts)
	}
	return out
}

func TestPruneEventsByAge(t *testing.T) {
	st := openEventsStore(t)
	seedEvents(t, st, 1000, 2000, 3000, 4000)

	res, err := st.PruneEvents(context.Background(), 3000, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.DeletedByAge != 2 || res.DeletedByCount != 0 || res.Remaining != 2 {
		t.Fatalf("result = %+v, want DeletedByAge=2 DeletedByCount=0 Remaining=2", res)
	}
	want := []int64{3000, 4000}
	got := remainingEventTS(t, st)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("remaining ts = %v, want %v (boundary row must survive: ts < cutoff only)", got, want)
	}
}

func TestPruneEventsByRowCap(t *testing.T) {
	st := openEventsStore(t)
	seedEvents(t, st, 1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000, 10000)

	res, err := st.PruneEvents(context.Background(), 0, 4)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.DeletedByAge != 0 || res.DeletedByCount != 6 || res.Remaining != 4 {
		t.Fatalf("result = %+v, want DeletedByAge=0 DeletedByCount=6 Remaining=4", res)
	}
	got := remainingEventTS(t, st)
	want := []int64{7000, 8000, 9000, 10000}
	if len(got) != len(want) {
		t.Fatalf("remaining = %v, want %v (newest 4)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remaining = %v, want %v (newest 4)", got, want)
		}
	}
}

// Both halves must apply in one pass: age first, then the row cap.
func TestPruneEventsAgeThenCap(t *testing.T) {
	st := openEventsStore(t)
	seedEvents(t, st, 1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000, 10000)

	res, err := st.PruneEvents(context.Background(), 5000, 1)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	// age removes 1000..4000 (4), cap then removes 5000..9000 (5).
	if res.DeletedByAge != 4 || res.DeletedByCount != 5 || res.Remaining != 1 {
		t.Fatalf("result = %+v, want 4/5/1", res)
	}
	if got := remainingEventTS(t, st); len(got) != 1 || got[0] != 10000 {
		t.Fatalf("remaining = %v, want [10000]", got)
	}
}

func TestPruneEventsNoOpCases(t *testing.T) {
	st := openEventsStore(t)
	seedEvents(t, st, 1000, 2000, 3000)

	cases := []struct {
		name     string
		beforeTS int64
		maxRows  int
	}{
		{"cap above row count", 0, 10},
		{"both halves disabled", 0, 0},
		{"age cutoff below every row", 1, 0},
	}
	for _, c := range cases {
		res, err := st.PruneEvents(context.Background(), c.beforeTS, c.maxRows)
		if err != nil {
			t.Fatalf("%s: prune: %v", c.name, err)
		}
		if res.DeletedByAge != 0 || res.DeletedByCount != 0 || res.Remaining != 3 {
			t.Fatalf("%s: result = %+v, want no deletions and Remaining=3", c.name, res)
		}
	}
}

// A cancelled context must abort the whole sweep: the two deletes and the
// count share one transaction, so nothing may be committed partially.
func TestPruneEventsCancelledContextKeepsRows(t *testing.T) {
	st := openEventsStore(t)
	seedEvents(t, st, 1000, 2000, 3000, 4000)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.PruneEvents(ctx, 3000, 1); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if got := remainingEventTS(t, st); len(got) != 4 {
		t.Fatalf("remaining = %v, want all 4 rows intact", got)
	}
}
