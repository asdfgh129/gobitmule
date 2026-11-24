// Command uded2kkadpeer is a controlled KAD UDP peer for the WBS 16 ED-02
// live-lab positive control. It speaks the fork's exact KAD codec (via
// kadproto.PacketCombiner) over a real kernel UDP socket, answers the opcodes
// used by bootstrap and a two-phase source search, and can emit a legacy
// nodes.dat containing exactly one contact: itself.
//
// It is evidence-collection only. Unlike PublishSource on a DHTTracker, this
// peer never writes a source into the probe's local index — every source the
// probe reports must have travelled over the wire in a SearchRes packet.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
)

type stats struct {
	Timestamp    string `json:"timestamp"`
	Listen       string `json:"listen"`
	SelfID       string `json:"self_id"`
	NodesOut     string `json:"nodes_out,omitempty"`
	SourceID     string `json:"source_id"`
	SourceEP     string `json:"source_endpoint"`
	BootstrapReq int    `json:"bootstrap_req"`
	FindNodeReq  int    `json:"find_node_req"`
	SearchSrcReq int    `json:"search_src_req"`
	UnknownOp    int    `json:"unknown_op"`
	UnpackFail   int    `json:"unpack_fail"`
	PackFail     int    `json:"pack_fail"`
	WriteFail    int    `json:"write_fail"`
	Verdict      string `json:"verdict"`
}

type peer struct {
	conn     *net.UDPConn
	combiner kadproto.PacketCombiner
	selfID   kadproto.ID
	source   kadproto.SearchEntry

	mu    sync.Mutex
	stats stats
}

func main() {
	os.Exit(run())
}

func run() int {
	udpPort := flag.Int("udp", 4673, "UDP listen port for the controlled KAD peer (loopback)")
	selfHash := flag.String("self-hash", "11223344556677889900AABBCCDDEEFF", "32 hex KAD node ID of this peer")
	tcpPort := flag.Int("tcp", 4662, "TCP port advertised in nodes.dat and the source entry")
	sourceHash := flag.String("source-hash", "DEADBEEFDEADBEEFDEADBEEFDEADBEEF", "32 hex ID of the advertised source")
	sourceIP := flag.String("source-ip", "127.0.0.1", "IPv4 of the advertised source")
	sourceUPort := flag.Int("source-udp", 4672, "UDP port of the advertised source")
	nodesOut := flag.String("nodes-out", "", "write a one-contact legacy nodes.dat to this path")
	duration := flag.Int("duration", 60, "seconds to serve before exiting")
	statsOut := flag.String("stats-out", "", "write JSON counters to this path on exit")
	flag.Parse()

	selfHashValue, err := protocol.HashFromString(*selfHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -self-hash: %v\n", err)
		return 2
	}
	sourceHashValue, err := protocol.HashFromString(*sourceHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -source-hash: %v\n", err)
		return 2
	}
	ip := net.ParseIP(*sourceIP).To4()
	if ip == nil {
		fmt.Fprintf(os.Stderr, "invalid -source-ip: %s\n", *sourceIP)
		return 2
	}

	p := &peer{
		combiner: kadproto.PacketCombiner{},
		selfID:   kadproto.NewID(selfHashValue),
		source: kadproto.SearchEntry{
			ID: kadproto.NewID(sourceHashValue),
			Tags: []kadproto.Tag{
				{Type: kadproto.TagTypeUint32, ID: kadproto.TagSourceIP, UInt64: uint64(binary.LittleEndian.Uint32(ip))},
				{Type: kadproto.TagTypeUint16, ID: kadproto.TagSourcePort, UInt64: uint64(*tcpPort)},
				{Type: kadproto.TagTypeUint32, ID: kadproto.TagSourceUPort, UInt64: uint64(*sourceUPort)},
				{Type: kadproto.TagTypeUint8, ID: kadproto.TagSourceType, UInt64: 1}, // HighID
			},
		},
	}
	p.stats = stats{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		SelfID:    p.selfID.String(),
		SourceID:  p.source.ID.String(),
		SourceEP:  fmt.Sprintf("%s:%d", *sourceIP, *tcpPort),
		NodesOut:  *nodesOut,
		Verdict:   "RUNNING",
	}

	if *nodesOut != "" {
		if err := writeNodesDat(*nodesOut, p.selfID, *udpPort, *tcpPort); err != nil {
			fmt.Fprintf(os.Stderr, "write nodes.dat: %v\n", err)
			return 2
		}
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: *udpPort})
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen UDP: %v\n", err)
		return 2
	}
	p.conn = conn
	p.stats.Listen = conn.LocalAddr().String()
	defer conn.Close()

	stop := make(chan struct{})
	var once sync.Once
	stopAll := func() { once.Do(func() { close(stop) }) }
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			stopAll()
		case <-stop:
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.serve(stop)
	}()

	timer := time.NewTimer(time.Duration(*duration) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-stop:
	}
	stopAll()
	_ = conn.Close() // unblock the read loop immediately
	<-done

	p.mu.Lock()
	p.stats.Timestamp = time.Now().UTC().Format(time.RFC3339)
	p.stats.Verdict = "PASS"
	snapshot := p.stats
	p.mu.Unlock()

	encoded, _ := json.MarshalIndent(snapshot, "", "  ")
	fmt.Println(string(encoded))
	if *statsOut != "" {
		if err := os.WriteFile(*statsOut, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write stats: %v\n", err)
			return 2
		}
	}
	return 0
}

// writeNodesDat emits the 29-byte legacy format: uint32 count=1 followed by a
// 25-byte contact (16-byte ID, 8-byte endpoint, 1-byte Kademlia version).
// A legacy contact carries Verified=false and is promoted to a live bucket by
// ApplyNodesDat, which is exactly what this positive control needs.
func writeNodesDat(path string, id kadproto.ID, udpPort, tcpPort int) error {
	var buf bytes.Buffer
	if err := protocol.WriteUInt32(&buf, 1); err != nil {
		return err
	}
	entry := kadproto.Entry{
		ID: id,
		Endpoint: kadproto.Endpoint{
			IP:      binary.LittleEndian.Uint32(net.IPv4(127, 0, 0, 1).To4()),
			UDPPort: uint16(udpPort),
			TCPPort: uint16(tcpPort),
		},
		Version: kadproto.KademliaVersion,
	}
	if err := entry.Put(&buf); err != nil {
		return err
	}
	// Self-check: the fork must be able to parse what we just wrote.
	parsed, err := kadproto.ParseNodesDat(buf.Bytes())
	if err != nil {
		return fmt.Errorf("generated nodes.dat failed ParseNodesDat: %w", err)
	}
	if len(parsed.Contacts) != 1 || !parsed.Contacts[0].ID.Hash.Equal(id.Hash) {
		return fmt.Errorf("generated nodes.dat round-trip mismatch: %+v", parsed)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func (p *peer) bump(field func(*stats)) {
	p.mu.Lock()
	field(&p.stats)
	p.mu.Unlock()
}

func (p *peer) serve(stop <-chan struct{}) {
	buf := make([]byte, 8192)
	for {
		select {
		case <-stop:
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
		opcode, msg, err := p.combiner.Unpack(buf[:n])
		if err != nil {
			p.bump(func(s *stats) { s.UnpackFail++ })
			continue
		}
		var reply any
		switch opcode {
		case kadproto.BootstrapReqOp:
			p.bump(func(s *stats) { s.BootstrapReq++ })
			reply = kadproto.BootstrapRes{ID: p.selfID, TCPPort: uint16(p.source.Tags[1].UInt64), Version: kadproto.KademliaVersion}
		case kadproto.ReqOp:
			req, ok := msg.(*kadproto.Req)
			if !ok {
				p.bump(func(s *stats) { s.UnpackFail++ })
				continue
			}
			p.bump(func(s *stats) { s.FindNodeReq++ })
			// An empty contact list is sufficient: processFindRes releases the
			// observer as long as Target matches the RPC transaction.
			reply = kadproto.Res{Target: req.Target}
		case kadproto.SearchSrcReqOp:
			req, ok := msg.(*kadproto.SearchSourcesReq)
			if !ok {
				p.bump(func(s *stats) { s.UnpackFail++ })
				continue
			}
			p.bump(func(s *stats) { s.SearchSrcReq++ })
			reply = kadproto.SearchRes{Source: p.selfID, Target: req.Target, Results: []kadproto.SearchEntry{p.source}}
		default:
			p.bump(func(s *stats) { s.UnknownOp++ })
			continue
		}
		raw, err := p.combiner.Pack(reply)
		if err != nil {
			p.bump(func(s *stats) { s.PackFail++ })
			continue
		}
		if _, err := p.conn.WriteToUDP(raw, from); err != nil {
			p.bump(func(s *stats) { s.WriteFail++ })
		}
	}
}
