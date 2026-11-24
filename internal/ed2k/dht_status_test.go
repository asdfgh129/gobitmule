package goed2k

import (
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
)

// A nodes.dat entry is "known" but not "verified": only a real UDP reply may
// flip Bootstrapped. Otherwise the Web console claims KAD works while every
// search times out.
func TestDHTStatusDistinguishesLoadedAndVerifiedNodes(t *testing.T) {
	tracker := NewDHTTracker(4672, time.Second)
	t.Cleanup(tracker.Close)

	first := kadproto.NewID(protocol.MustHashFromString("00000000000000000000000000000001"))
	second := kadproto.NewID(protocol.MustHashFromString("00000000000000000000000000000002"))
	tracker.mu.Lock()
	confirmed := tracker.addOrUpdateNodeLocked(first, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 10), Port: 4672}, 4662, 8, false)
	tracker.addOrUpdateNodeLocked(second, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 11), Port: 4672}, 4662, 8, false)
	tracker.mu.Unlock()

	st := tracker.Status()
	if st.Bootstrapped || st.VerifiedNodes != 0 || st.KnownNodes != 2 {
		t.Fatalf("loaded-only nodes must not be bootstrapped: %+v", st)
	}

	tracker.mu.Lock()
	tracker.confirmNodeLocked(confirmed)
	tracker.mu.Unlock()

	st = tracker.Status()
	if !st.Bootstrapped || st.VerifiedNodes != 1 || st.KnownNodes != 2 {
		t.Fatalf("one real reply must be bootstrapped: %+v", st)
	}
}
