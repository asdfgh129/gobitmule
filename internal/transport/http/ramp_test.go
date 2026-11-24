package httptransport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/security"
)

func waitBackpressure(t *testing.T, g *rampGate) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !g.backpressured() {
		if time.Now().After(deadline) {
			t.Fatal("gate never observed backpressure")
		}
		time.Sleep(time.Millisecond)
	}
}

// docs/05 §5: "初始 1 连接，按吞吐逐步增至 maxConnections".
func TestRampGateStartsAtOne(t *testing.T) {
	g := newRampGate(4)
	if got := g.currentLimit(); got != 1 {
		t.Fatalf("initial limit = %d, want 1", got)
	}
	if !g.acquire(context.Background()) {
		t.Fatal("first acquire must succeed")
	}
	got := make(chan bool, 1)
	go func() { got <- g.acquire(context.Background()) }()
	select {
	case <-got:
		t.Fatal("second connection opened before the link was proven")
	case <-time.After(50 * time.Millisecond):
	}
	g.release()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("acquire failed")
		}
	case <-time.After(time.Second):
		t.Fatal("slot never returned after release")
	}
	g.release()
}

// Growth is driven by observed backpressure, never by a timer: an idle gate that
// never queued a request must not open more connections.
func TestRampGateGrowsOnBackpressureOnly(t *testing.T) {
	g := newRampGate(3)
	if !g.acquire(context.Background()) {
		t.Fatal("acquire failed")
	}
	if g.backpressured() {
		t.Fatal("no queue yet, backpressure must be false")
	}
	if g.maybeGrow() {
		t.Fatal("grew without backpressure")
	}
	if got := g.currentLimit(); got != 1 {
		t.Fatalf("limit = %d, want 1", got)
	}

	// A second acquire now has to wait -> the ceiling is the bottleneck.
	got := make(chan bool, 1)
	go func() { got <- g.acquire(context.Background()) }()
	waitBackpressure(t, g)
	if !g.maybeGrow() {
		t.Fatal("must grow once backpressure was observed")
	}
	if got := g.currentLimit(); got != 2 {
		t.Fatalf("limit = %d, want 2", got)
	}
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("queued acquire failed after growth")
		}
	case <-time.After(time.Second):
		t.Fatal("queued acquire never got the new slot")
	}
	// The flag is consumed: one grow per observed bottleneck.
	if g.maybeGrow() {
		t.Fatal("a single backpressure event must not grow twice")
	}
	g.release()
	g.release()
}

func TestRampGateNeverExceedsMax(t *testing.T) {
	g := newRampGate(2)
	g.mu.Lock()
	g.held, g.limit, g.blocked = 0, 2, true
	g.mu.Unlock()
	if g.maybeGrow() {
		t.Fatal("grow past max must return false")
	}
	if got := g.currentLimit(); got != 2 {
		t.Fatalf("limit = %d, want 2", got)
	}
}

// Under sustained backpressure the ceiling must climb all the way to max and
// stop there — this is the deterministic half of "按吞吐逐步增至 maxConnections"
// (the end-to-end peak depends on how many chunks the last wave still has).
func TestRampGateReachesMaxUnderSustainedBackpressure(t *testing.T) {
	const max = 4
	g := newRampGate(max)
	ctx := context.Background()
	held := 0
	if !g.acquire(ctx) {
		t.Fatal("acquire failed")
	}
	held++
	for g.currentLimit() < max {
		queued := make(chan bool, 1)
		go func() { queued <- g.acquire(ctx) }()
		waitBackpressure(t, g)
		if !g.maybeGrow() {
			t.Fatalf("must grow while limit = %d < %d", g.currentLimit(), max)
		}
		select {
		case ok := <-queued:
			if !ok {
				t.Fatal("queued acquire failed")
			}
		case <-time.After(time.Second):
			t.Fatal("new slot never reached the queued acquire")
		}
		held++
	}
	if got := g.currentLimit(); got != max {
		t.Fatalf("limit = %d, want %d", got, max)
	}
	g.mu.Lock()
	g.blocked = true
	g.mu.Unlock()
	if g.maybeGrow() {
		t.Fatal("must not grow past maxConnections")
	}
	for i := 0; i < held; i++ {
		g.release()
	}
}

func TestRampGateClampsToAtLeastOne(t *testing.T) {
	g := newRampGate(0)
	if got := g.currentLimit(); got != 1 {
		t.Fatalf("limit = %d, want 1", got)
	}
	if !g.acquire(context.Background()) {
		t.Fatal("acquire failed")
	}
	got := make(chan bool, 1)
	go func() { got <- g.acquire(context.Background()) }()
	waitBackpressure(t, g)
	if g.maybeGrow() {
		t.Fatal("single-connection gate must never grow")
	}
	g.release()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("released slot never reached the waiter")
	}
	g.release()
}

func TestRampGateAcquireHonoursContext(t *testing.T) {
	g := newRampGate(1)
	if !g.acquire(context.Background()) {
		t.Fatal("acquire failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if g.acquire(ctx) {
		t.Fatal("acquire succeeded despite a cancelled context")
	}
	g.release()
	// Tokens must be conserved: the slot is still usable afterwards.
	if !g.acquire(context.Background()) {
		t.Fatal("slot lost after a cancelled acquire")
	}
	g.release()
}

// concurrencyRecorder tracks how many requests the fixture serves at once.
type concurrencyRecorder struct {
	mu      sync.Mutex
	now     int
	first   int
	peak    int
	samples []int
}

func (c *concurrencyRecorder) enter() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now++
	if c.first == 0 {
		c.first = c.now
	}
	if c.now > c.peak {
		c.peak = c.now
	}
	c.samples = append(c.samples, c.now)
}

func (c *concurrencyRecorder) leave() {
	c.mu.Lock()
	c.now--
	c.mu.Unlock()
}

// The gate is wired into Run: the first request must be alone, and the peak must
// reach maxConnections (otherwise "ramp up" would be a no-op).
func TestRunSlowStartRampsConnections(t *testing.T) {
	data := make([]byte, 32<<10)
	for i := range data {
		data[i] = byte((i*17 + 3) % 256)
	}
	rec := &concurrencyRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(data))
			return
		}
		rec.enter()
		defer rec.leave()
		time.Sleep(2 * time.Millisecond) // let concurrency build up
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()

	tr := New(security.NewDevGuard())
	r := &core.Resource{ID: "r", Kind: core.KindSingle,
		Files:   []core.ResourceFile{{Index: 0, Path: "f.bin", Size: int64(len(data)), Selected: true}},
		Mirrors: []core.Mirror{{URL: srv.URL + "/f.bin", Weight: 1}},
		Limits:  core.TaskLimits{MaxConnections: 4}}
	w := newMemWriter(int64(len(data)), 4<<10, 3) // 8 chunks
	if err := tr.Run(context.Background(), r, w, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !bytes.Equal(w.bytes(), data) {
		t.Fatal("slow-start download produced wrong bytes")
	}
	if rec.first != 1 {
		t.Fatalf("first request saw %d concurrent downloads, want 1 (slow start)", rec.first)
	}
	// The exact peak is workload-dependent (the last wave may not need every
	// slot), so assert the two properties that are deterministic: the ramp does
	// open additional connections under load, and it never exceeds the cap.
	// Reaching the cap itself is pinned by
	// TestRampGateReachesMaxUnderSustainedBackpressure.
	if rec.peak < 2 {
		t.Fatalf("peak concurrency = %d, want >= 2 (ramp must grow under load)", rec.peak)
	}
	for _, n := range rec.samples {
		if n > 4 {
			t.Fatalf("observed %d concurrent requests, max is 4", n)
		}
	}
}
