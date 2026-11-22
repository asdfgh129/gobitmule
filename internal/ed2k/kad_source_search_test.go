package goed2k

import (
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/ud/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/ud/internal/ed2k/protocol/kad"
)

// TestSourceSearchCallbackCompletesOnRPCTimeout guards the two-phase KAD
// traversal: when find-sources probes time out, the chained search-sources
// traversal must still finish and release the caller callback.
func TestSourceSearchCallbackCompletesOnRPCTimeout(t *testing.T) {
	tracker := NewDHTTracker(0, 1)
	tracker.rpc.shortTimeout = 100 * time.Millisecond
	tracker.rpc.timeout = 300 * time.Millisecond
	if err := tracker.Start(); err != nil {
		t.Fatalf("tracker Start: %v", err)
	}
	defer tracker.Close()

	nodeID := kadproto.NewID(protocol.MustHashFromString("AABBCCDDEEFF00112233445566778899"))
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	tracker.mu.Lock()
	node := tracker.addOrUpdateNodeLocked(nodeID, addr, 0, 0, true)
	if node != nil {
		tracker.table.NodeSeen(node)
	}
	tracker.mu.Unlock()
	if node == nil {
		t.Fatal("fake KAD node was not added")
	}

	called := make(chan struct{}, 1)
	hash := protocol.MustHashFromString("00112233445566778899AABBCCDDEEFF")
	started := tracker.SearchSources(hash, 1024, func([]kadproto.SearchEntry) {
		select {
		case called <- struct{}{}:
		default:
		}
	})
	if !started {
		t.Fatal("SearchSources did not start")
	}
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("source search callback was not released after RPC timeout")
	}
}
