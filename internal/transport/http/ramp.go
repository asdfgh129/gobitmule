package httptransport

import (
	"context"
	"sync"
)

// rampGate is the slow-start (docs/05 §5: "初始 1 连接，按吞吐逐步增至
// maxConnections") concurrency gate of the Range downloader.
//
// Tokens are conserved: the gate owns exactly `max` slot tokens but withholds
// all but one until the link proves it can keep them busy. Growth is driven by
// an observed *backpressure* signal rather than a timer or a throughput
// estimate: the dispatcher only sets the flag when it actually had to wait for a
// free slot, i.e. when the current ceiling was the bottleneck. A later
// completion then hands one withheld slot back. A link that cannot fill one
// connection never opens a second one; a fast link reaches maxConnections after
// a few chunks.
//
// Using backpressure (instead of "was every slot busy at completion time")
// keeps the ramp monotonic: the dispatcher raises the flag before it blocks, so
// a burst of simultaneous completions cannot make the gate miss the saturation
// the way a sampled in-flight counter can.
type rampGate struct {
	sem chan struct{}

	mu       sync.Mutex
	held     int // slots withheld from the pool (== max - limit)
	inFlight int // fetches currently running (observability/tests)
	limit    int // current concurrency ceiling
	max      int
	blocked  bool // the dispatcher waited for a slot since the last grow
}

func newRampGate(max int) *rampGate {
	if max < 1 {
		max = 1
	}
	g := &rampGate{sem: make(chan struct{}, max), limit: 1, max: max}
	// One slot is available immediately; the rest stay withheld.
	g.sem <- struct{}{}
	g.held = max - 1
	return g
}

// acquire takes a slot, blocking while the current limit is exhausted. The fast
// path is non-blocking; only the queued path raises the backpressure flag.
func (g *rampGate) acquire(ctx context.Context) bool {
	select {
	case <-g.sem:
		g.mu.Lock()
		g.inFlight++
		g.mu.Unlock()
		return true
	default:
	}
	g.mu.Lock()
	g.blocked = true
	g.mu.Unlock()
	select {
	case <-g.sem:
		g.mu.Lock()
		g.inFlight++
		g.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}

// release hands the slot back. Call it exactly once per successful acquire.
func (g *rampGate) release() {
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
	g.sem <- struct{}{}
}

// maybeGrow hands one withheld slot back if the dispatcher was blocked since the
// last grow. It reports whether the ceiling moved.
func (g *rampGate) maybeGrow() bool {
	g.mu.Lock()
	if !g.blocked || g.held <= 0 || g.limit >= g.max {
		g.mu.Unlock()
		return false
	}
	g.blocked = false
	g.held--
	g.limit++
	g.mu.Unlock()
	g.sem <- struct{}{}
	return true
}

// currentLimit is the concurrency ceiling reached so far.
func (g *rampGate) currentLimit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}

// backpressured reports whether the dispatcher is waiting on the current
// ceiling (test/observability).
func (g *rampGate) backpressured() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.blocked
}
