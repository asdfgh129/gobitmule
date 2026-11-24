package goed2k

import (
	"bytes"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
)

// fakeKADPeer is an in-process KAD UDP peer built on the fork's own
// PacketCombiner. It answers the two opcodes used by a source search:
//   - ReqOp (FindNode)          -> ResOp  echoing Target (marks observer alive)
//   - SearchSrcReqOp            -> SearchResOp with one source entry
//
// It exists as a positive control: if a *local* peer that speaks the exact
// same codec fork uses cannot produce responses>0 / entries>0, then the fault
// is in the fork's encode/RPC/traversal chain, not the remote environment.
type fakeKADPeer struct {
	conn         *net.UDPConn
	combiner     kadproto.PacketCombiner
	selfID       kadproto.ID
	udpVerifyKey uint32
	source       kadproto.SearchEntry

	stopCh chan struct{}
	wg     sync.WaitGroup

	mu      sync.Mutex
	reqSeen int
	srcSeen int
}

func newFakeKADPeer(t *testing.T) *fakeKADPeer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("fake KAD peer listen: %v", err)
	}
	sourceID := kadproto.NewID(protocol.MustHashFromString("DEADBEEFDEADBEEFDEADBEEFDEADBEEF"))
	source := kadproto.SearchEntry{
		ID: sourceID,
		Tags: []kadproto.Tag{
			{Type: kadproto.TagTypeUint32, ID: kadproto.TagSourceIP, UInt64: 0x0100007F}, // 127.0.0.1 (LE uint32)
			{Type: kadproto.TagTypeUint16, ID: kadproto.TagSourcePort, UInt64: 4662},
			{Type: kadproto.TagTypeUint32, ID: kadproto.TagSourceUPort, UInt64: 4672},
			{Type: kadproto.TagTypeUint8, ID: kadproto.TagSourceType, UInt64: 1}, // HighID / TCP
		},
	}
	p := &fakeKADPeer{
		conn:         conn,
		selfID:       kadproto.NewID(protocol.MustHashFromString("11223344556677889900AABBCCDDEEFF")),
		udpVerifyKey: 0x11223344,
		source:       source,
		stopCh:       make(chan struct{}),
	}
	p.wg.Add(1)
	go p.serve()
	return p
}

func (p *fakeKADPeer) addr() *net.UDPAddr { return p.conn.LocalAddr().(*net.UDPAddr) }

func (p *fakeKADPeer) counts() (req, src int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reqSeen, p.srcSeen
}

func (p *fakeKADPeer) close() {
	close(p.stopCh)
	_ = p.conn.Close()
	p.wg.Wait()
}

func (p *fakeKADPeer) serve() {
	defer p.wg.Done()
	buf := make([]byte, 8192)
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}
		_ = p.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, from, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		packet := buf[:n]
		if plain, _, ok := kadproto.DecryptDatagram(packet, p.selfID, p.udpVerifyKey); ok {
			packet = plain
		}
		opcode, msg, err := p.combiner.Unpack(packet)
		if err != nil {
			continue
		}
		var reply any
		switch opcode {
		case kadproto.ReqOp:
			req, ok := msg.(*kadproto.Req)
			if !ok {
				continue
			}
			p.mu.Lock()
			p.reqSeen++
			p.mu.Unlock()
			// Empty contact list is enough: processFindRes still calls
			// observer.done() as long as Target matches the RPC transaction.
			reply = kadproto.Res{Target: req.Target}
		case kadproto.SearchSrcReqOp:
			req, ok := msg.(*kadproto.SearchSourcesReq)
			if !ok {
				continue
			}
			p.mu.Lock()
			p.srcSeen++
			p.mu.Unlock()
			reply = kadproto.SearchRes{Source: p.selfID, Target: req.Target, Results: []kadproto.SearchEntry{p.source}}
		case kadproto.BootstrapReqOp:
			reply = kadproto.BootstrapRes{ID: p.selfID, TCPPort: 4662, Version: kadproto.KademliaVersion}
		default:
			continue
		}
		raw, err := p.combiner.Pack(reply)
		if err != nil {
			continue
		}
		_, _ = p.conn.WriteToUDP(raw, from)
	}
}

// TestKADSourceSearchLocalPositiveControl is the decisive root-cause probe for
// ED-02. It drives the full two-phase source search (find-sources ->
// search-sources) against a local peer that speaks the fork's exact codec.
//
// A PASS proves the wire encoding, RPC target matching, traversal chaining and
// callback release are all correct, so the earlier live-lab responses=0 /
// timeouts=41-46 is attributable to the environment (stale nodes.dat + NAT),
// not a protocol bug. A FAIL here would instead expose a fork-internal defect.
func TestKADSourceSearchLocalPositiveControl(t *testing.T) {
	peer := newFakeKADPeer(t)
	defer peer.close()

	tracker := NewDHTTracker(0, 900*time.Millisecond)
	tracker.rpc.shortTimeout = 200 * time.Millisecond
	tracker.rpc.timeout = 900 * time.Millisecond
	if err := tracker.Start(); err != nil {
		t.Fatalf("tracker Start: %v", err)
	}
	defer tracker.Close()

	// Capture fork debug logs to assert per-traversal response counters.
	var logBuf bytes.Buffer
	SetDebugLog(&logBuf)
	defer SetDebugLog(io.Discard)

	// Inject the local peer as a known, seen routing node so closestNodesLocked
	// (knownOnly=true) selects it for the search.
	tracker.mu.Lock()
	node := tracker.addOrUpdateNodeLocked(peer.selfID, peer.addr(), 4662, kadproto.KademliaVersion, true)
	if node != nil {
		tracker.table.NodeSeen(node)
	}
	tracker.mu.Unlock()
	if node == nil {
		t.Fatal("local KAD peer was not added to routing table")
	}

	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	resultCh := make(chan []kadproto.SearchEntry, 1)
	started := tracker.SearchSources(hash, 1038612780, func(entries []kadproto.SearchEntry) {
		select {
		case resultCh <- entries:
		default:
		}
	})
	if !started {
		t.Fatal("SearchSources did not start")
	}

	var entries []kadproto.SearchEntry
	select {
	case entries = <-resultCh:
	case <-time.After(4 * time.Second):
		t.Fatalf("source search callback not released; debug log:\n%s", logBuf.String())
	}

	if len(entries) == 0 {
		t.Fatalf("expected at least one source entry from local KAD peer; debug log:\n%s", logBuf.String())
	}
	ep, ok := entries[0].SourceEndpoint()
	if !ok {
		t.Fatalf("source entry had no decodable endpoint: %+v", entries[0])
	}
	if uint32(ep.IP()) != 0x0100007F || ep.Port() != 4662 {
		t.Fatalf("unexpected source endpoint: ip=0x%08X port=%d", uint32(ep.IP()), ep.Port())
	}

	reqSeen, srcSeen := peer.counts()
	if reqSeen < 1 {
		t.Fatalf("local peer never received a FindNode Req; debug log:\n%s", logBuf.String())
	}
	if srcSeen < 1 {
		t.Fatalf("local peer never received a SearchSourcesReq; debug log:\n%s", logBuf.String())
	}

	// Assert both traversal phases recorded real responses and no timeouts.
	logs := logBuf.String()
	assertTraversalResponses(t, logs, "find-sources", 1)
	assertTraversalResponses(t, logs, "search-sources", 1)
}

func assertTraversalResponses(t *testing.T, logs, kind string, wantResponses int) {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "kind="+kind) || !strings.Contains(line, "kad traversal done") {
			continue
		}
		if !strings.Contains(line, "responses="+itoa(wantResponses)) {
			t.Fatalf("%s traversal did not record responses=%d: %s", kind, wantResponses, line)
		}
		if !strings.Contains(line, "timeouts=0") {
			t.Fatalf("%s traversal recorded timeouts (expected 0): %s", kind, line)
		}
		return
	}
	t.Fatalf("no %s traversal log line found; debug log:\n%s", kind, logs)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
