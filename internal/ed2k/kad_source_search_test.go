package goed2k

import (
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
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

// TestSearchResReleasesSourceTraversal verifies that a received SEARCH_RES is
// handed to the search immediately instead of waiting for the 12s RPC expiry.
// eMule drives search completion from its own answer count/lifetime; relying
// on RPC expiry made a 50-node lookup take minutes and starved downloads.
func TestSearchResReleasesSourceTraversal(t *testing.T) {
	tracker := NewDHTTracker(0, 1)
	node := newKadNodeImpl(tracker)
	target := protocol.MustHashFromString("00112233445566778899AABBCCDDEEFF")
	addr := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 10), Port: 4662}

	results := make(chan []kadproto.SearchEntry, 1)
	traversal := newKadTraversal(node, kadproto.NewID(target), kadTraversalSearchSources, 1024,
		func(entries []kadproto.SearchEntry) { results <- entries })
	observer := traversal.newObserver(addr, kadproto.NewID(target), 4662, 6)
	traversal.results = append(traversal.results, observer)
	traversal.numTargetNodes = 1
	node.running[traversal.key()] = traversal

	tracker.rpc.Invoke(&kadRPCTransaction{
		endpointKey: addr.String(),
		opcode:      kadproto.SearchResOp,
		target:      &target,
		multi:       true,
		observer:    observer,
	})

	source := kadproto.SearchEntry{
		ID: kadproto.NewID(protocol.MustHashFromString("AABBCCDDEEFF00112233445566778899")),
		Tags: []kadproto.Tag{
			{ID: kadproto.TagSourceType, Type: kadproto.TagTypeUint8, UInt64: 1},
			{ID: kadproto.TagSourceIP, Type: kadproto.TagTypeUint32, UInt64: 0x01020304},
			{ID: kadproto.TagSourcePort, Type: kadproto.TagTypeUint16, UInt64: 4662},
		},
	}
	node.processSearchRes(addr, kadproto.SearchRes{
		Target:  kadproto.NewID(target),
		Results: []kadproto.SearchEntry{source},
	})

	select {
	case entries := <-results:
		if len(entries) != 1 {
			t.Fatalf("callback entries = %d, want 1", len(entries))
		}
		endpoint, ok := entries[0].SourceEndpoint()
		if !ok || endpoint.String() != "1.2.3.4:4662" {
			t.Fatalf("callback source endpoint = %s, ok=%v", endpoint.String(), ok)
		}
	case <-time.After(time.Second):
		t.Fatal("SEARCH_RES did not release the source traversal immediately")
	}
}
